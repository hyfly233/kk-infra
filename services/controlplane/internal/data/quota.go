package data

import (
	"database/sql"
	"errors"
	"sync"

	"kk-infra/lib/domain"
)

// QuotaStore 租户配额存储接口（内存 / Postgres 可插拔）
type QuotaStore interface {
	// Get 查询租户某 GPU 型号配额
	Get(tenantID, gpuType string) (*domain.TenantQuota, error)
	// Set 设置配额上限
	Set(tenantID, gpuType string, quota int32) error
	// AddUsed 增加已用量（创建部署时），返回更新后的已用量
	AddUsed(tenantID, gpuType string, delta int32) error
	ReleaseDeployment(deploymentID, tenantID, gpuType string, count int32) error
	// List 列出全部配额
	List() ([]*domain.TenantQuota, error)
}

// ErrQuotaNotFound 配额不存在
var ErrQuotaNotFound = errors.New("quota not found")
var ErrQuotaExceeded = errors.New("quota exceeded")

// MemoryQuotaStore 内存实现（默认 default 租户 16 GPU 配额）
type MemoryQuotaStore struct {
	mu       sync.RWMutex
	quotas   map[string]*domain.TenantQuota // tenantID/gpuType → quota
	releases map[string]quotaRelease
}

// NewMemoryQuotaStore 创建内存配额存储
func NewMemoryQuotaStore() *MemoryQuotaStore {
	s := &MemoryQuotaStore{quotas: make(map[string]*domain.TenantQuota), releases: make(map[string]quotaRelease)}
	// 默认配额：default 租户 A100 16 卡（与 Fake 集群容量一致）
	s.quotas["default/A100"] = &domain.TenantQuota{TenantID: "default", GPUType: "A100", Quota: 16, Used: 0}
	return s
}

func quotaKey(tenantID, gpuType string) string { return tenantID + "/" + gpuType }

func (s *MemoryQuotaStore) Get(tenantID, gpuType string) (*domain.TenantQuota, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q, ok := s.quotas[quotaKey(tenantID, gpuType)]
	if !ok {
		return nil, ErrQuotaNotFound
	}
	copy := *q
	return &copy, nil
}

func (s *MemoryQuotaStore) Set(tenantID, gpuType string, quota int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := quotaKey(tenantID, gpuType)
	if q, ok := s.quotas[key]; ok {
		q.Quota = quota
	} else {
		s.quotas[key] = &domain.TenantQuota{TenantID: tenantID, GPUType: gpuType, Quota: quota}
	}
	return nil
}

func (s *MemoryQuotaStore) AddUsed(tenantID, gpuType string, delta int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := quotaKey(tenantID, gpuType)
	q, ok := s.quotas[key]
	if !ok {
		return ErrQuotaNotFound
	}
	if delta > 0 && int64(q.Used)+int64(delta) > int64(q.Quota) {
		return ErrQuotaExceeded
	}
	q.Used += delta
	if q.Used < 0 {
		q.Used = 0
	}
	return nil
}

func (s *MemoryQuotaStore) List() ([]*domain.TenantQuota, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*domain.TenantQuota, 0, len(s.quotas))
	for _, q := range s.quotas {
		copy := *q
		out = append(out, &copy)
	}
	return out, nil
}

// PostgresQuotaStore Postgres 实现
type PostgresQuotaStore struct {
	db *sql.DB
}

// NewPostgresQuotaStore 创建 Postgres 配额存储
func NewPostgresQuotaStore(db *sql.DB) *PostgresQuotaStore {
	return &PostgresQuotaStore{db: db}
}

func (s *PostgresQuotaStore) Get(tenantID, gpuType string) (*domain.TenantQuota, error) {
	q := &domain.TenantQuota{}
	err := s.db.QueryRow(
		`SELECT tenant_id, gpu_type, quota, used FROM tenant_quotas WHERE tenant_id=$1 AND gpu_type=$2`,
		tenantID, gpuType,
	).Scan(&q.TenantID, &q.GPUType, &q.Quota, &q.Used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrQuotaNotFound
	}
	if err != nil {
		return nil, err
	}
	return q, nil
}

func (s *PostgresQuotaStore) Set(tenantID, gpuType string, quota int32) error {
	_, err := s.db.Exec(
		`INSERT INTO tenant_quotas (tenant_id, gpu_type, quota, used) VALUES ($1,$2,$3,0)
		 ON CONFLICT (tenant_id, gpu_type) DO UPDATE SET quota=$3`,
		tenantID, gpuType, quota,
	)
	return err
}

func (s *PostgresQuotaStore) AddUsed(tenantID, gpuType string, delta int32) error {
	res, err := s.db.Exec(
		`UPDATE tenant_quotas SET used = GREATEST(used::bigint + $3, 0)
		 WHERE tenant_id=$1 AND gpu_type=$2 AND ($3 <= 0 OR used::bigint + $3 <= quota)`,
		tenantID, gpuType, delta,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		if _, err := s.Get(tenantID, gpuType); err != nil {
			return err
		}
		return ErrQuotaExceeded
	}
	return nil
}

func (s *PostgresQuotaStore) List() ([]*domain.TenantQuota, error) {
	rows, err := s.db.Query(`SELECT tenant_id, gpu_type, quota, used FROM tenant_quotas ORDER BY tenant_id, gpu_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*domain.TenantQuota, 0)
	for rows.Next() {
		q := &domain.TenantQuota{}
		if err := rows.Scan(&q.TenantID, &q.GPUType, &q.Quota, &q.Used); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}
