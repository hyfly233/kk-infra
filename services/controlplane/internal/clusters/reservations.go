package clusters

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// GPUReservation remains until explicit workload deletion. A heartbeat offsets
// it only with assigned Pods of the same deployment, tenant and template.
type GPUReservation struct {
	DeploymentID       string `json:"deploymentId"`
	TenantID           string `json:"tenantId"`
	Namespace          string `json:"namespace"`
	ModelVersionID     string `json:"modelVersionId,omitempty"`
	TemplateGeneration int64  `json:"templateGeneration"`
	GPUType            string `json:"gpuType"`
	GPUCount           int32  `json:"gpuCount"`
}

func availableGPU(c Cluster, gpuType string) int64 {
	var available int64
	for _, capacity := range c.GPUCapacity {
		if capacity.GPUType == gpuType {
			available += int64(capacity.Allocatable) - int64(capacity.Used)
		}
	}
	for _, reservation := range c.GPUReservations {
		if reservation.GPUType != gpuType {
			continue
		}
		var observed int64
		for _, pod := range c.DeploymentGPU {
			if pod.DeploymentID == reservation.DeploymentID && pod.TenantID == reservation.TenantID && pod.Namespace == reservation.Namespace && pod.GPUType == gpuType && pod.TemplateGeneration != nil && *pod.TemplateGeneration == reservation.TemplateGeneration {
				observed += int64(pod.GPUCount)
			}
		}
		available -= max(int64(reservation.GPUCount)-observed, 0)
	}
	return available
}

func reserveGPU(c *Cluster, request GPUReservation, runtime string, now time.Time) error {
	if request.DeploymentID == "" || request.TenantID == "" || request.Namespace == "" || request.GPUType == "" || request.GPUCount < 0 || request.TemplateGeneration < 0 {
		return fmt.Errorf("invalid GPU reservation")
	}
	for _, existing := range c.GPUReservations {
		if existing.DeploymentID == request.DeploymentID && existing.TemplateGeneration == request.TemplateGeneration {
			if existing == request {
				return nil
			}
			return fmt.Errorf("reservation identity already has a different request")
		}
	}
	if c.HealthStatus != "healthy" || c.LastHeartbeat == nil || now.Sub(*c.LastHeartbeat) > 90*time.Second || !contains(c.SupportedRuntimes, runtime) || !tenantAllowed(c.AllowedTenants, request.TenantID) {
		return fmt.Errorf("cluster is unavailable for reservation")
	}
	if availableGPU(*c, request.GPUType) < int64(request.GPUCount) {
		return fmt.Errorf("insufficient unreserved GPU capacity")
	}
	c.GPUReservations = append(c.GPUReservations, request)
	return nil
}

func (s *Service) ReserveCapacity(id string, request GPUReservation, runtime string) error {
	return s.repo.ReserveCapacity(id, request, runtime, s.now().UTC())
}

func (s *Service) ReleaseCapacity(id, deploymentID string) error {
	return s.repo.ReleaseCapacity(id, deploymentID)
}

func (r *MemoryRepository) ReserveCapacity(id string, request GPUReservation, runtime string, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	if err := reserveGPU(&c, request, runtime, now); err != nil {
		return err
	}
	r.clusters[id] = c
	return nil
}

func (r *MemoryRepository) ReleaseCapacity(id, deploymentID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	keep := []GPUReservation{}
	for _, item := range c.GPUReservations {
		if item.DeploymentID != deploymentID {
			keep = append(keep, item)
		}
	}
	c.GPUReservations = keep
	r.clusters[id] = c
	return nil
}

func (r *PostgresRepository) ReserveCapacity(id string, request GPUReservation, runtime string, now time.Time) error {
	return r.mutateReservations(id, func(c *Cluster) error { return reserveGPU(c, request, runtime, now) })
}

func (r *PostgresRepository) mutateReservations(id string, mutate func(*Cluster) error) error {
	tx, err := r.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var c Cluster
	var capacity, assigned, reservations, runtimes, tenants []byte
	// Heartbeat UPDATE and concurrent control planes serialize on this row.
	err = tx.QueryRow(`SELECT health_status,last_heartbeat,gpu_capacity,deployment_gpu,gpu_reservations,supported_runtimes,allowed_tenants FROM clusters WHERE id=$1 FOR UPDATE`, id).Scan(&c.HealthStatus, &c.LastHeartbeat, &capacity, &assigned, &reservations, &runtimes, &tenants)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	for _, field := range []struct {
		raw    []byte
		target any
	}{{capacity, &c.GPUCapacity}, {assigned, &c.DeploymentGPU}, {reservations, &c.GPUReservations}, {runtimes, &c.SupportedRuntimes}, {tenants, &c.AllowedTenants}} {
		if err := json.Unmarshal(field.raw, field.target); err != nil {
			return err
		}
	}
	if err := mutate(&c); err != nil {
		return err
	}
	data, err := json.Marshal(c.GPUReservations)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE clusters SET gpu_reservations=$2 WHERE id=$1`, id, string(data)); err != nil {
		return err
	}
	return tx.Commit()
}

// Growth is monotonic: shrinking and uncertain failures must not release GPU
// requests that may still exist. Legacy deployments without a ledger fail closed.
func growGPU(c *Cluster, deploymentID, gpuType, modelVersionID string, count int32, now time.Time) error {
	if count <= 0 {
		return fmt.Errorf("GPU reservation growth requires a positive total")
	}
	index := -1
	for i, item := range c.GPUReservations {
		if item.DeploymentID == deploymentID && item.GPUType == gpuType && item.ModelVersionID == modelVersionID && (index < 0 || item.TemplateGeneration > c.GPUReservations[index].TemplateGeneration) {
			index = i
		}
	}
	if index < 0 {
		return fmt.Errorf("deployment has no GPU reservation; recreate or reconcile it before growing")
	}
	item := c.GPUReservations[index]
	if count <= item.GPUCount {
		return nil
	}
	if c.HealthStatus != "healthy" || c.LastHeartbeat == nil || now.Sub(*c.LastHeartbeat) > 90*time.Second {
		return fmt.Errorf("cluster capacity is unavailable")
	}
	// Copy before replacing so a rejected mutation never aliases the saved ledger.
	updated := *c
	updated.GPUReservations = append([]GPUReservation(nil), c.GPUReservations...)
	updated.GPUReservations[index].GPUCount = count
	if availableGPU(updated, item.GPUType) < 0 {
		return fmt.Errorf("insufficient unreserved GPU capacity")
	}
	c.GPUReservations = updated.GPUReservations
	return nil
}

func (s *Service) GrowCapacity(id, deploymentID, gpuType, modelVersionID string, count int32) error {
	return s.repo.GrowCapacity(id, deploymentID, gpuType, modelVersionID, count, s.now().UTC())
}

func (r *MemoryRepository) GrowCapacity(id, deploymentID, gpuType, modelVersionID string, count int32, now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	if err := growGPU(&c, deploymentID, gpuType, modelVersionID, count, now); err != nil {
		return err
	}
	r.clusters[id] = c
	return nil
}

func (r *PostgresRepository) GrowCapacity(id, deploymentID, gpuType, modelVersionID string, count int32, now time.Time) error {
	return r.mutateReservations(id, func(c *Cluster) error { return growGPU(c, deploymentID, gpuType, modelVersionID, count, now) })
}

func (r *PostgresRepository) ReleaseCapacity(id, deploymentID string) error {
	result, err := r.db.Exec(`UPDATE clusters SET gpu_reservations=COALESCE((SELECT jsonb_agg(item) FROM jsonb_array_elements(gpu_reservations) item WHERE item->>'deploymentId' <> $2),'[]'::jsonb) WHERE id=$1`, id, deploymentID)
	return affected(result, err)
}
