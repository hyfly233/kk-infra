package server

import (
	"encoding/json"
	"net/http"
	"sort"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/services/controlplane/internal/clusters"
)

func (s *Server) handleOrphanCleanup(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	var request struct {
		ClusterID   string `json:"clusterId"`
		Acknowledge bool   `json:"acknowledgeDelete"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	err := s.deployments.CleanupOrphan(r.Context(), r.PathValue("id"), request.ClusterID, claimsFrom(r.Context()).Subject, request.Acknowledge)
	apitypes.WriteResult(w, r, map[string]bool{"cleaned": err == nil}, err)
}

type orphanReservationView struct {
	ClusterID     string                  `json:"clusterId"`
	ClusterHealth string                  `json:"clusterHealth"`
	Reservation   clusters.GPUReservation `json:"reservation"`
}

// This inventory is not proof of resource absence or permission to release it.
func (s *Server) handleOrphanReservations(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	d, err := s.deployments.GetDeployment(r.PathValue("id"))
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	items, err := s.clusters.List()
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "读取集群预留失败", err))
		return
	}
	reservations := []orphanReservationView{}
	for _, cluster := range items {
		if cluster.ID == d.ClusterID {
			continue
		}
		for _, reservation := range cluster.GPUReservations {
			if reservation.DeploymentID == d.ID {
				reservations = append(reservations, orphanReservationView{ClusterID: cluster.ID, ClusterHealth: cluster.HealthStatus, Reservation: reservation})
			}
		}
	}
	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].ClusterID != reservations[j].ClusterID {
			return reservations[i].ClusterID < reservations[j].ClusterID
		}
		return reservations[i].Reservation.TemplateGeneration < reservations[j].Reservation.TemplateGeneration
	})
	pinned, running, err := s.deployments.OrphanCleanupState(d.ID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "读取清理认领失败", err))
		return
	}
	apitypes.WriteResult(w, r, struct {
		DeploymentID     string                  `json:"deploymentId"`
		CurrentClusterID string                  `json:"currentClusterId"`
		Reservations     []orphanReservationView `json:"reservations"`
		CleanupClusterID string                  `json:"cleanupClusterId"`
		CleanupRunning   bool                    `json:"cleanupRunning"`
	}{d.ID, d.ClusterID, reservations, pinned, running}, nil)
}
