// Package data 控制面数据访问层。
// MVP 内存实现；repository 接口化以支持后续切换 PostgreSQL。
package data

import (
	"errors"
	"strings"
	"sync"
	"time"

	"kk-infra/lib/domain"
)

// ErrNotFound 部署不存在
var ErrNotFound = errors.New("deployment not found")

// ErrConflict 部署已存在
var ErrConflict = errors.New("deployment already exists")

// DeploymentRepository 部署仓库接口
type DeploymentRepository interface {
	Create(d *domain.ModelDeployment) error
	Get(id string) (*domain.ModelDeployment, error)
	GetByName(name string) (*domain.ModelDeployment, error)
	Update(d *domain.ModelDeployment) error
	ClaimClusterRebuild(id, fromCluster, toCluster string, at time.Time) (*domain.ModelDeployment, error)
	List(tenantID string) ([]*domain.ModelDeployment, error)
	Delete(id string) error
	// 事件
	AddEvent(e *domain.StatusEvent)
	Events(deploymentID string) []domain.StatusEvent
	AddRevision(*domain.DeploymentRevision) error
	Revisions(deploymentID string) ([]domain.DeploymentRevision, error)
}

// MemoryDeploymentRepository 内存实现
type MemoryDeploymentRepository struct {
	mu        sync.RWMutex
	deploys   map[string]*domain.ModelDeployment
	byName    map[string]string // name → id
	events    map[string][]domain.StatusEvent
	revisions map[string][]domain.DeploymentRevision
	seq       int
}

// NewMemoryDeploymentRepository 创建内存仓库
func NewMemoryDeploymentRepository() *MemoryDeploymentRepository {
	return &MemoryDeploymentRepository{
		deploys:   make(map[string]*domain.ModelDeployment),
		byName:    make(map[string]string),
		events:    make(map[string][]domain.StatusEvent),
		revisions: make(map[string][]domain.DeploymentRevision),
	}
}

func (r *MemoryDeploymentRepository) AddRevision(revision *domain.DeploymentRevision) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.deploys[revision.DeploymentID]; !ok {
		return ErrNotFound
	}
	for _, existing := range r.revisions[revision.DeploymentID] {
		if existing.Generation == revision.Generation {
			return ErrConflict
		}
	}
	r.revisions[revision.DeploymentID] = append(r.revisions[revision.DeploymentID], *revision)
	return nil
}

func (r *MemoryDeploymentRepository) Revisions(deploymentID string) ([]domain.DeploymentRevision, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.deploys[deploymentID]; !ok {
		return nil, ErrNotFound
	}
	source := r.revisions[deploymentID]
	out := make([]domain.DeploymentRevision, len(source))
	for i := range source {
		out[len(source)-1-i] = source[i]
	}
	return out, nil
}

func (r *MemoryDeploymentRepository) Create(d *domain.ModelDeployment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.deploys[d.ID]; exists {
		return ErrConflict
	}
	// 同名已存在：仅当旧记录已删除时才允许重建（更新 byName 指向新 ID）
	if oldID, exists := r.byName[d.Name]; exists {
		old, ok := r.deploys[oldID]
		if !ok || old.Status != domain.DeploymentStatusDeleted {
			return ErrConflict
		}
	}
	r.seq++
	if d.ID == "" {
		d.ID = "d" + itoa(r.seq)
	}
	r.deploys[d.ID] = d
	r.byName[d.Name] = d.ID
	return nil
}

func (r *MemoryDeploymentRepository) Get(id string) (*domain.ModelDeployment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.deploys[id]
	if !ok {
		return nil, ErrNotFound
	}
	return d, nil
}

func (r *MemoryDeploymentRepository) GetByName(name string) (*domain.ModelDeployment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byName[name]
	if !ok {
		return nil, ErrNotFound
	}
	return r.deploys[id], nil
}

func (r *MemoryDeploymentRepository) Update(d *domain.ModelDeployment) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.deploys[d.ID]; !ok {
		return ErrNotFound
	}
	r.deploys[d.ID] = d
	return nil
}

func (r *MemoryDeploymentRepository) ClaimClusterRebuild(id, fromCluster, toCluster string, at time.Time) (*domain.ModelDeployment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.deploys[id]
	if !ok {
		return nil, ErrNotFound
	}
	if d.ClusterID != fromCluster || d.Status != domain.DeploymentStatusFailed || !strings.HasPrefix(d.Diagnostics, "目标集群不可用:") {
		return nil, ErrConflict
	}
	updated := *d
	updated.ClusterID, updated.Status, updated.Diagnostics = toCluster, domain.DeploymentStatusSubmitting, ""
	updated.Endpoint, updated.StableEndpoint, updated.CanaryEndpoint, updated.RolloutStatus = "", "", "", ""
	updated.Generation++
	updated.UpdatedAt = at
	r.deploys[id] = &updated
	return &updated, nil
}

func (r *MemoryDeploymentRepository) List(tenantID string) ([]*domain.ModelDeployment, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.ModelDeployment, 0)
	for _, d := range r.deploys {
		if tenantID == "" || d.TenantID == tenantID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (r *MemoryDeploymentRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.deploys[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.deploys, id)
	delete(r.byName, d.Name)
	return nil
}

func (r *MemoryDeploymentRepository) AddEvent(e *domain.StatusEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events[e.DeploymentID] = append(r.events[e.DeploymentID], *e)
	// 控制事件数量
	if evs := r.events[e.DeploymentID]; len(evs) > 100 {
		r.events[e.DeploymentID] = evs[len(evs)-100:]
	}
}

func (r *MemoryDeploymentRepository) Events(deploymentID string) []domain.StatusEvent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.StatusEvent, len(r.events[deploymentID]))
	copy(out, r.events[deploymentID])
	return out
}

// 简单整数转字符串
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
