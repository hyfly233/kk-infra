// Package clusters manages registered Kubernetes clusters and their reported capacity.
package clusters

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"kk-infra/lib/domain"
)

var ErrNotFound = errors.New("cluster not found")
var clusterIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
var serviceNamePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

type Capacity struct {
	GPUType     string `json:"gpuType"`
	Total       int32  `json:"total"`
	Allocatable int32  `json:"allocatable"`
	Used        int32  `json:"used"`
}

type Cluster struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Endpoint           string            `json:"endpoint"`
	AdapterURL         string            `json:"adapterUrl"`
	ServingURLTemplate string            `json:"servingUrlTemplate,omitempty"`
	Labels             map[string]string `json:"labels"`
	SupportedRuntimes  []string          `json:"supportedRuntimes"`
	AllowedTenants     []string          `json:"allowedTenants,omitempty"`
	GPUCapacity        []Capacity        `json:"gpuCapacity"`
	HealthStatus       string            `json:"healthStatus"`
	LastHeartbeat      *time.Time        `json:"lastHeartbeat,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	UpdatedAt          time.Time         `json:"updatedAt"`
	Kubeconfig         string            `json:"-"`
}

type Repository interface {
	Create(*Cluster, []byte) error
	List() ([]Cluster, error)
	Get(string) (Cluster, []byte, error)
	Report(string, string, []Capacity, time.Time) error
	SetServingURLTemplate(string, string) error
	UpdatePolicy(string, map[string]string, []string, []string) error
	RotateCredentials(string, []byte) error
	Delete(string) error
}

func (s *Service) UpdatePolicy(id string, labels map[string]string, runtimes, allowedTenants []string) error {
	runtimes = unique(runtimes)
	if len(runtimes) == 0 {
		return fmt.Errorf("supportedRuntimes cannot be empty")
	}
	for key := range labels {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("label keys cannot be empty")
		}
	}
	return s.repo.UpdatePolicy(id, cloneMap(labels), runtimes, unique(allowedTenants))
}

func (s *Service) RotateCredentials(id, kubeconfig string) error {
	if strings.TrimSpace(kubeconfig) == "" {
		return fmt.Errorf("kubeconfig is required")
	}
	if _, err := s.Get(id); err != nil {
		return err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	return s.repo.RotateCredentials(id, s.aead.Seal(nonce, nonce, []byte(kubeconfig), []byte(id)))
}

func (s *Service) Delete(id string) error { return s.repo.Delete(id) }

type Service struct {
	repo Repository
	aead cipher.AEAD
	now  func() time.Time
}

func NewService(repo Repository, key []byte) (*Service, error) {
	if repo == nil {
		return nil, fmt.Errorf("cluster repository is required")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("CLUSTER_ENCRYPTION_KEY must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cluster encryption key must be 16, 24, or 32 bytes: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{repo: repo, aead: aead, now: time.Now}, nil
}

func (s *Service) Register(id, name, endpoint, adapterURL, kubeconfig string, labels map[string]string, runtimes, allowedTenants []string, servingURLTemplate string) (Cluster, error) {
	id, name, endpoint = strings.TrimSpace(id), strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(endpoint), "/")
	adapterURL = strings.TrimRight(strings.TrimSpace(adapterURL), "/")
	if !clusterIDPattern.MatchString(id) || name == "" || endpoint == "" || adapterURL == "" || kubeconfig == "" {
		return Cluster{}, fmt.Errorf("id, name, endpoint, adapterUrl and kubeconfig are required")
	}
	if !strings.HasPrefix(endpoint, "https://") {
		return Cluster{}, fmt.Errorf("cluster endpoint must use https")
	}
	if !strings.HasPrefix(adapterURL, "https://") && !strings.HasPrefix(adapterURL, "http://") {
		return Cluster{}, fmt.Errorf("adapterUrl must use http or https")
	}
	if err := validateServingURLTemplate(servingURLTemplate); err != nil {
		return Cluster{}, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Cluster{}, err
	}
	ciphertext := s.aead.Seal(nonce, nonce, []byte(kubeconfig), []byte(id))
	now := s.now().UTC()
	cluster := Cluster{ID: id, Name: name, Endpoint: endpoint, AdapterURL: adapterURL, ServingURLTemplate: servingURLTemplate, Labels: cloneMap(labels), SupportedRuntimes: unique(runtimes), AllowedTenants: unique(allowedTenants), HealthStatus: "unknown", CreatedAt: now, UpdatedAt: now}
	if err := s.repo.Create(&cluster, ciphertext); err != nil {
		return Cluster{}, err
	}
	return cluster, nil
}

func (s *Service) List() ([]Cluster, error) {
	items, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	for i := range items {
		s.markStale(&items[i])
	}
	return items, nil
}

func (s *Service) markStale(cluster *Cluster) {
	if cluster.HealthStatus == "healthy" && cluster.LastHeartbeat != nil && s.now().Sub(*cluster.LastHeartbeat) > 90*time.Second {
		cluster.HealthStatus = "stale"
	}
}

func validateServingURLTemplate(template string) error {
	if template == "" {
		return nil
	}
	if !strings.Contains(template, "{service}") || !strings.Contains(template, "{namespace}") {
		return fmt.Errorf("servingUrlTemplate requires {service} and {namespace}")
	}
	sample := strings.ReplaceAll(strings.ReplaceAll(template, "{service}", "model"), "{namespace}", "tenant")
	if strings.ContainsAny(sample, "{}") {
		return fmt.Errorf("servingUrlTemplate contains unknown placeholder")
	}
	u, err := url.Parse(sample)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("servingUrlTemplate must be an absolute HTTPS base URL without credentials, query or fragment")
	}
	return nil
}

func (s *Service) SetServingURLTemplate(id, template string) error {
	if err := validateServingURLTemplate(template); err != nil {
		return err
	}
	if template == "" {
		return fmt.Errorf("servingUrlTemplate cannot be empty")
	}
	return s.repo.SetServingURLTemplate(id, template)
}

func (s *Service) ResolveServingEndpoint(clusterID, serviceName, namespace string) (string, error) {
	if !serviceNamePattern.MatchString(serviceName) || !serviceNamePattern.MatchString(namespace) {
		return "", fmt.Errorf("invalid service or namespace name")
	}
	cluster, err := s.Get(clusterID)
	if err != nil {
		return "", err
	}
	if cluster.ServingURLTemplate == "" {
		return "", fmt.Errorf("cluster %s has no gateway serving URL template", clusterID)
	}
	return strings.ReplaceAll(strings.ReplaceAll(cluster.ServingURLTemplate, "{service}", serviceName), "{namespace}", namespace), nil
}

func (s *Service) Get(id string) (Cluster, error) {
	cluster, _, err := s.repo.Get(id)
	if err == nil {
		s.markStale(&cluster)
	}
	return cluster, err
}

func (s *Service) Select(resource domain.Resource, runtime, tenantID string) (Cluster, error) {
	return s.selectCluster(resource, runtime, tenantID, false)
}

func (s *Service) SelectWithServingRoute(resource domain.Resource, runtime, tenantID string) (Cluster, error) {
	return s.selectCluster(resource, runtime, tenantID, true)
}

func (s *Service) selectCluster(resource domain.Resource, runtime, tenantID string, requireServingRoute bool) (Cluster, error) {
	all, err := s.repo.List()
	if err != nil {
		return Cluster{}, err
	}
	var selected *Cluster
	bestAvailable := int32(0)
	now := s.now()
	for i := range all {
		candidate := &all[i]
		if candidate.HealthStatus != "healthy" || candidate.LastHeartbeat == nil || now.Sub(*candidate.LastHeartbeat) > 90*time.Second || !contains(candidate.SupportedRuntimes, runtime) || !tenantAllowed(candidate.AllowedTenants, tenantID) || (requireServingRoute && candidate.ServingURLTemplate == "") {
			continue
		}
		available := int32(0)
		for _, cap := range candidate.GPUCapacity {
			if cap.GPUType == resource.GPUType {
				available += cap.Allocatable - cap.Used
			}
		}
		if available < resource.GPUCount {
			continue
		}
		if selected == nil || available < bestAvailable || (available == bestAvailable && candidate.ID < selected.ID) {
			selected, bestAvailable = candidate, available
		}
	}
	if selected == nil {
		return Cluster{}, fmt.Errorf("no healthy cluster matches runtime, tenant policy, and GPU request")
	}
	return *selected, nil
}

func (s *Service) CheckCapacity(id string, resource domain.Resource) error {
	cluster, err := s.healthy(id)
	if err != nil {
		return err
	}
	available := int32(0)
	for _, capacity := range cluster.GPUCapacity {
		if capacity.GPUType == resource.GPUType {
			available += capacity.Allocatable - capacity.Used
		}
	}
	if available < resource.GPUCount {
		return fmt.Errorf("cluster %s has %d available %s GPUs; %d required", id, available, resource.GPUType, resource.GPUCount)
	}
	return nil
}

func (s *Service) CheckHealth(id string) error {
	_, err := s.healthy(id)
	return err
}

func (s *Service) healthy(id string) (Cluster, error) {
	cluster, err := s.Get(id)
	if err != nil {
		return Cluster{}, err
	}
	if cluster.HealthStatus != "healthy" || cluster.LastHeartbeat == nil || s.now().Sub(*cluster.LastHeartbeat) > 90*time.Second {
		return Cluster{}, fmt.Errorf("cluster %s is not healthy or its capacity report is stale", id)
	}
	return cluster, nil
}

func (s *Service) CheckRuntime(id, runtime, tenantID string) error {
	cluster, err := s.Get(id)
	if err != nil {
		return err
	}
	if !contains(cluster.SupportedRuntimes, runtime) {
		return fmt.Errorf("cluster %s does not support runtime %s", id, runtime)
	}
	if !tenantAllowed(cluster.AllowedTenants, tenantID) {
		return fmt.Errorf("tenant %s is not allowed on cluster %s", tenantID, id)
	}
	if cluster.HealthStatus != "healthy" || cluster.LastHeartbeat == nil || s.now().Sub(*cluster.LastHeartbeat) > 90*time.Second {
		return fmt.Errorf("cluster %s is not healthy or its capacity report is stale", id)
	}
	return nil
}

func (s *Service) Report(id, status string, capacity []Capacity) error {
	if status != "healthy" && status != "unhealthy" {
		return fmt.Errorf("healthStatus must be healthy or unhealthy")
	}
	for _, item := range capacity {
		if item.GPUType == "" || item.Total < 0 || item.Allocatable < 0 || item.Used < 0 || item.Allocatable > item.Total || item.Used > item.Allocatable {
			return fmt.Errorf("invalid GPU capacity report")
		}
	}
	return s.repo.Report(id, status, capacity, s.now().UTC())
}

func (s *Service) DecryptCredentials(id string) (string, error) {
	cluster, ciphertext, err := s.repo.Get(id)
	if err != nil {
		return "", err
	}
	if len(ciphertext) < s.aead.NonceSize() {
		return "", fmt.Errorf("invalid encrypted cluster credentials")
	}
	nonce, data := ciphertext[:s.aead.NonceSize()], ciphertext[s.aead.NonceSize():]
	plain, err := s.aead.Open(nil, nonce, data, []byte(cluster.ID))
	if err != nil {
		return "", fmt.Errorf("decrypt cluster credentials: %w", err)
	}
	return string(plain), nil
}

type MemoryRepository struct {
	mu       sync.RWMutex
	clusters map[string]Cluster
	secrets  map[string][]byte
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{clusters: map[string]Cluster{}, secrets: map[string][]byte{}}
}

func (r *MemoryRepository) Create(cluster *Cluster, ciphertext []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.clusters {
		if existing.Name == cluster.Name {
			return fmt.Errorf("cluster already exists")
		}
	}
	if _, ok := r.clusters[cluster.ID]; ok {
		return fmt.Errorf("cluster already exists")
	}
	r.clusters[cluster.ID] = *cluster
	r.secrets[cluster.ID] = append([]byte(nil), ciphertext...)
	return nil
}

func (r *MemoryRepository) List() ([]Cluster, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Cluster, 0, len(r.clusters))
	for _, cluster := range r.clusters {
		out = append(out, cloneCluster(cluster))
	}
	return out, nil
}

func (r *MemoryRepository) Get(id string) (Cluster, []byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cluster, ok := r.clusters[id]
	if !ok {
		return Cluster{}, nil, ErrNotFound
	}
	return cloneCluster(cluster), append([]byte(nil), r.secrets[id]...), nil
}

func (r *MemoryRepository) Report(id, status string, capacity []Capacity, at time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cluster, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	cluster.HealthStatus, cluster.GPUCapacity, cluster.UpdatedAt = status, append([]Capacity(nil), capacity...), at
	cluster.LastHeartbeat = &at
	r.clusters[id] = cluster
	return nil
}

func (r *MemoryRepository) SetServingURLTemplate(id, template string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	c.ServingURLTemplate = template
	c.UpdatedAt = time.Now().UTC()
	r.clusters[id] = c
	return nil
}

func (r *MemoryRepository) UpdatePolicy(id string, labels map[string]string, runtimes, tenants []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.clusters[id]
	if !ok {
		return ErrNotFound
	}
	c.Labels, c.SupportedRuntimes, c.AllowedTenants = cloneMap(labels), append([]string(nil), runtimes...), append([]string(nil), tenants...)
	c.UpdatedAt = time.Now().UTC()
	r.clusters[id] = c
	return nil
}

func (r *MemoryRepository) RotateCredentials(id string, ciphertext []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clusters[id]; !ok {
		return ErrNotFound
	}
	r.secrets[id] = append([]byte(nil), ciphertext...)
	return nil
}

func (r *MemoryRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.clusters[id]; !ok {
		return ErrNotFound
	}
	delete(r.clusters, id)
	delete(r.secrets, id)
	return nil
}

type PostgresRepository struct{ db *sql.DB }

func NewPostgresRepository(db *sql.DB) *PostgresRepository { return &PostgresRepository{db: db} }

func (r *PostgresRepository) Create(c *Cluster, encrypted []byte) error {
	labels, _ := json.Marshal(c.Labels)
	runtimes, _ := json.Marshal(c.SupportedRuntimes)
	tenants, _ := json.Marshal(c.AllowedTenants)
	_, err := r.db.Exec(`INSERT INTO clusters(id,name,endpoint,adapter_url,serving_url_template,kubeconfig_ciphertext,labels,supported_runtimes,allowed_tenants,health_status,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'unknown',$10,$10)`, c.ID, c.Name, c.Endpoint, c.AdapterURL, c.ServingURLTemplate, encrypted, string(labels), string(runtimes), string(tenants), c.CreatedAt)
	return err
}

func (r *PostgresRepository) List() ([]Cluster, error) {
	rows, err := r.db.Query(`SELECT id,name,endpoint,adapter_url,serving_url_template,labels,supported_runtimes,allowed_tenants,gpu_capacity,health_status,last_heartbeat,created_at,updated_at FROM clusters ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Cluster{}
	for rows.Next() {
		var c Cluster
		var labels, runtimes, tenants, capacity []byte
		var heartbeat sql.NullTime
		if err := rows.Scan(&c.ID, &c.Name, &c.Endpoint, &c.AdapterURL, &c.ServingURLTemplate, &labels, &runtimes, &tenants, &capacity, &c.HealthStatus, &heartbeat, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(labels, &c.Labels)
		_ = json.Unmarshal(runtimes, &c.SupportedRuntimes)
		_ = json.Unmarshal(tenants, &c.AllowedTenants)
		_ = json.Unmarshal(capacity, &c.GPUCapacity)
		if heartbeat.Valid {
			c.LastHeartbeat = &heartbeat.Time
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func (r *PostgresRepository) Get(id string) (Cluster, []byte, error) {
	var c Cluster
	var ciphertext, labels, runtimes, tenants, capacity []byte
	var heartbeat sql.NullTime
	err := r.db.QueryRow(`SELECT id,name,endpoint,adapter_url,serving_url_template,kubeconfig_ciphertext,labels,supported_runtimes,allowed_tenants,gpu_capacity,health_status,last_heartbeat,created_at,updated_at FROM clusters WHERE id=$1`, id).Scan(&c.ID, &c.Name, &c.Endpoint, &c.AdapterURL, &c.ServingURLTemplate, &ciphertext, &labels, &runtimes, &tenants, &capacity, &c.HealthStatus, &heartbeat, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return Cluster{}, nil, ErrNotFound
	}
	if err != nil {
		return Cluster{}, nil, err
	}
	_ = json.Unmarshal(labels, &c.Labels)
	_ = json.Unmarshal(runtimes, &c.SupportedRuntimes)
	_ = json.Unmarshal(tenants, &c.AllowedTenants)
	_ = json.Unmarshal(capacity, &c.GPUCapacity)
	if heartbeat.Valid {
		c.LastHeartbeat = &heartbeat.Time
	}
	return c, ciphertext, nil
}

func (r *PostgresRepository) Report(id, status string, capacity []Capacity, at time.Time) error {
	data, _ := json.Marshal(capacity)
	result, err := r.db.Exec(`UPDATE clusters SET health_status=$2,gpu_capacity=$3,last_heartbeat=$4,updated_at=$4 WHERE id=$1`, id, status, string(data), at)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (r *PostgresRepository) SetServingURLTemplate(id, template string) error {
	result, err := r.db.Exec(`UPDATE clusters SET serving_url_template=$2,updated_at=now() WHERE id=$1`, id, template)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (r *PostgresRepository) UpdatePolicy(id string, labels map[string]string, runtimes, tenants []string) error {
	l, _ := json.Marshal(labels)
	rt, _ := json.Marshal(runtimes)
	t, _ := json.Marshal(tenants)
	result, err := r.db.Exec(`UPDATE clusters SET labels=$2,supported_runtimes=$3,allowed_tenants=$4,updated_at=now() WHERE id=$1`, id, string(l), string(rt), string(t))
	return affected(result, err)
}

func (r *PostgresRepository) RotateCredentials(id string, ciphertext []byte) error {
	result, err := r.db.Exec(`UPDATE clusters SET kubeconfig_ciphertext=$2,updated_at=now() WHERE id=$1`, id, ciphertext)
	return affected(result, err)
}

func (r *PostgresRepository) Delete(id string) error {
	result, err := r.db.Exec(`DELETE FROM clusters WHERE id=$1 AND NOT EXISTS (SELECT 1 FROM deployments WHERE cluster_id=$1 AND status <> 'DELETED')`, id)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 0 {
		return nil
	}
	var exists bool
	if err := r.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM clusters WHERE id=$1)`, id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("cluster has active deployments")
	}
	return ErrNotFound
}

func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func cloneMap(value map[string]string) map[string]string {
	copy := map[string]string{}
	for k, v := range value {
		copy[k] = v
	}
	return copy
}

func cloneCluster(c Cluster) Cluster {
	c.Labels = cloneMap(c.Labels)
	c.SupportedRuntimes = append([]string(nil), c.SupportedRuntimes...)
	c.GPUCapacity = append([]Capacity(nil), c.GPUCapacity...)
	if c.LastHeartbeat != nil {
		t := *c.LastHeartbeat
		c.LastHeartbeat = &t
	}
	return c
}

func unique(values []string) []string {
	seen := map[string]bool{}
	result := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}
func tenantAllowed(allowed []string, tenant string) bool {
	return len(allowed) == 0 || contains(allowed, tenant)
}
