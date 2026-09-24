// Package data 模型注册中心数据访问层。
// MVP 使用内存实现，接口化以支持后续切换 PostgreSQL。
package data

import (
	"errors"
	"sync"
	"time"

	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
)

// ErrNotFound 内部哨兵错误
var ErrNotFound = errors.New("not found")

// ErrConflict 冲突哨兵错误
var ErrConflict = errors.New("conflict")

// Repository 模型仓库接口
type Repository interface {
	CreateModel(m *domain.Model) error
	GetModel(id string) (*domain.Model, error)
	GetModelByName(name string) (*domain.Model, error)
	ListModels() ([]*domain.Model, error)
	DeleteModel(id string) error

	CreateVersion(v *domain.ModelVersion) error
	GetVersion(id string) (*domain.ModelVersion, error)
	GetVersionByModelAndVersion(modelID, version string) (*domain.ModelVersion, error)
	ListVersions(modelID string) ([]*domain.ModelVersion, error)
	DeleteVersion(id string) error
	UpdateVersionStatus(id, status string) error
	UpdateArtifactMetadata(id, digest string, size int64, verifiedAt time.Time) error
	HasVersionReferences(id string) (bool, error)
}

// MemoryRepository 内存实现（线程安全）
type MemoryRepository struct {
	mu       sync.RWMutex
	models   map[string]*domain.Model        // id → model
	versions map[string]*domain.ModelVersion // id → version
	// 名称 → id 索引
	modelNameIndex map[string]string
	// (modelID, version) → versionID 索引
	versionIndex map[string]string
	modelSeq     int
	versionSeq   int
}

// NewMemoryRepository 创建内存仓库
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		models:         make(map[string]*domain.Model),
		versions:       make(map[string]*domain.ModelVersion),
		modelNameIndex: make(map[string]string),
		versionIndex:   make(map[string]string),
	}
}

func (r *MemoryRepository) CreateModel(m *domain.Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modelNameIndex[m.Name]; exists {
		return ErrConflict
	}
	r.modelSeq++
	if m.ID == "" {
		m.ID = "m" + itoa(r.modelSeq)
	}
	r.models[m.ID] = m
	r.modelNameIndex[m.Name] = m.ID
	return nil
}

func (r *MemoryRepository) GetModel(id string) (*domain.Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[id]
	if !ok {
		return nil, ErrNotFound
	}
	return m, nil
}

func (r *MemoryRepository) GetModelByName(name string) (*domain.Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.modelNameIndex[name]
	if !ok {
		return nil, ErrNotFound
	}
	return r.models[id], nil
}

func (r *MemoryRepository) ListModels() ([]*domain.Model, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.Model, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}
	return out, nil
}

func (r *MemoryRepository) DeleteModel(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.models[id]
	if !ok {
		return ErrNotFound
	}
	// 删除关联版本
	for _, v := range r.versions {
		if v.ModelID == id {
			delete(r.versions, v.ID)
			delete(r.versionIndex, v.ModelID+"/"+v.Version)
		}
	}
	delete(r.modelNameIndex, m.Name)
	delete(r.models, id)
	return nil
}

func (r *MemoryRepository) CreateVersion(v *domain.ModelVersion) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.versionIndex[v.ModelID+"/"+v.Version]; exists {
		return ErrConflict
	}
	r.versionSeq++
	if v.ID == "" {
		v.ID = "v" + itoa(r.versionSeq)
	}
	r.versions[v.ID] = v
	r.versionIndex[v.ModelID+"/"+v.Version] = v.ID
	return nil
}

func (r *MemoryRepository) GetVersion(id string) (*domain.ModelVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.versions[id]
	if !ok {
		return nil, ErrNotFound
	}
	return v, nil
}

func (r *MemoryRepository) GetVersionByModelAndVersion(modelID, version string) (*domain.ModelVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.versionIndex[modelID+"/"+version]
	if !ok {
		return nil, ErrNotFound
	}
	return r.versions[id], nil
}

func (r *MemoryRepository) ListVersions(modelID string) ([]*domain.ModelVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*domain.ModelVersion, 0)
	for _, v := range r.versions {
		if v.ModelID == modelID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (r *MemoryRepository) DeleteVersion(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.versions[id]
	if !ok {
		return ErrNotFound
	}
	delete(r.versions, id)
	delete(r.versionIndex, v.ModelID+"/"+v.Version)
	return nil
}

func (r *MemoryRepository) UpdateVersionStatus(id, status string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.versions[id]
	if !ok {
		return ErrNotFound
	}
	v.Status = status
	return nil
}

func (r *MemoryRepository) UpdateArtifactMetadata(id, digest string, size int64, verifiedAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.versions[id]
	if !ok {
		return ErrNotFound
	}
	v.ArtifactDigest = digest
	v.ArtifactSize = size
	v.ArtifactVerifiedAt = &verifiedAt
	v.UpdatedAt = verifiedAt
	return nil
}

func (r *MemoryRepository) HasVersionReferences(string) (bool, error) { return false, nil }

// 简单整数转字符串，避免引入 strconv 依赖以外的额外包
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

// ToErrCode 把仓库错误转换为业务错误码
func ToErrCode(err error) *errcode.Error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotFound):
		return errcode.New(errcode.ErrNotFound, "资源不存在")
	case errors.Is(err, ErrConflict):
		return errcode.New(errcode.ErrConflict, "资源已存在")
	default:
		return errcode.Wrap(errcode.ErrInternal, "存储错误", err)
	}
}
