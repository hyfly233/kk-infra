package data

import (
	"database/sql"
	"errors"
	"time"

	"github.com/lib/pq"

	"kk-infra/lib/domain"
)

// PostgresRepository modelregistry 的 Postgres 实现。
// 与 MemoryRepository 实现同一 Repository 接口，可无感切换。
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository 创建 Postgres 仓库
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// mapErr 把 Postgres 错误映射为仓库哨兵错误
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	// 唯一约束冲突
	if isUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

func (r *PostgresRepository) CreateModel(m *domain.Model) error {
	_, err := r.db.Exec(
		`INSERT INTO models (id, name, description, created_at, updated_at, tenant_id) VALUES ($1,$2,$3,$4,$5,$6)`,
		m.ID, m.Name, m.Description, m.CreatedAt, m.UpdatedAt, m.TenantID,
	)
	return mapErr(err)
}

func (r *PostgresRepository) GetModel(id string) (*domain.Model, error) {
	m := &domain.Model{}
	err := r.db.QueryRow(
		`SELECT id, name, description, created_at, updated_at, tenant_id FROM models WHERE id=$1`, id,
	).Scan(&m.ID, &m.Name, &m.Description, &m.CreatedAt, &m.UpdatedAt, &m.TenantID)
	return m, mapErr(err)
}

func (r *PostgresRepository) GetModelByName(name string) (*domain.Model, error) {
	m := &domain.Model{}
	err := r.db.QueryRow(
		`SELECT id, name, description, created_at, updated_at, tenant_id FROM models WHERE name=$1`, name,
	).Scan(&m.ID, &m.Name, &m.Description, &m.CreatedAt, &m.UpdatedAt, &m.TenantID)
	return m, mapErr(err)
}

func (r *PostgresRepository) ListModels() ([]*domain.Model, error) {
	rows, err := r.db.Query(`SELECT id, name, description, created_at, updated_at, tenant_id FROM models ORDER BY created_at DESC`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]*domain.Model, 0)
	for rows.Next() {
		m := &domain.Model{}
		if err := rows.Scan(&m.ID, &m.Name, &m.Description, &m.CreatedAt, &m.UpdatedAt, &m.TenantID); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) DeleteModel(id string) error {
	// 级联删除版本（ON DELETE CASCADE）
	res, err := r.db.Exec(`DELETE FROM models WHERE id=$1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) CreateVersion(v *domain.ModelVersion) error {
	_, err := r.db.Exec(
		`INSERT INTO model_versions (id, model_id, version, artifact_uri, artifact_digest, artifact_size, storage_type, runtime, gpu_type, gpu_count, memory_mb, context_length, status, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		v.ID, v.ModelID, v.Version, v.ArtifactURI, v.ArtifactDigest, v.ArtifactSize, v.StorageType,
		v.Runtime, v.GPUType, v.GPUCount, v.MemoryMB, v.ContextLength, v.Status, v.CreatedAt, v.UpdatedAt,
	)
	return mapErr(err)
}

func (r *PostgresRepository) GetVersion(id string) (*domain.ModelVersion, error) {
	v := &domain.ModelVersion{}
	err := r.db.QueryRow(
		`SELECT id, model_id, version, artifact_uri, artifact_digest, artifact_size, storage_type, artifact_verified_at, runtime, gpu_type, gpu_count, memory_mb, context_length, status, created_at, updated_at
		 FROM model_versions WHERE id=$1`, id,
	).Scan(&v.ID, &v.ModelID, &v.Version, &v.ArtifactURI, &v.ArtifactDigest, &v.ArtifactSize, &v.StorageType, &v.ArtifactVerifiedAt, &v.Runtime, &v.GPUType,
		&v.GPUCount, &v.MemoryMB, &v.ContextLength, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	return v, mapErr(err)
}

func (r *PostgresRepository) GetVersionByModelAndVersion(modelID, version string) (*domain.ModelVersion, error) {
	v := &domain.ModelVersion{}
	err := r.db.QueryRow(
		`SELECT id, model_id, version, artifact_uri, artifact_digest, artifact_size, storage_type, artifact_verified_at, runtime, gpu_type, gpu_count, memory_mb, context_length, status, created_at, updated_at
		 FROM model_versions WHERE model_id=$1 AND version=$2`, modelID, version,
	).Scan(&v.ID, &v.ModelID, &v.Version, &v.ArtifactURI, &v.ArtifactDigest, &v.ArtifactSize, &v.StorageType, &v.ArtifactVerifiedAt, &v.Runtime, &v.GPUType,
		&v.GPUCount, &v.MemoryMB, &v.ContextLength, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	return v, mapErr(err)
}

func (r *PostgresRepository) ListVersions(modelID string) ([]*domain.ModelVersion, error) {
	rows, err := r.db.Query(
		`SELECT id, model_id, version, artifact_uri, artifact_digest, artifact_size, storage_type, artifact_verified_at, runtime, gpu_type, gpu_count, memory_mb, context_length, status, created_at, updated_at
		 FROM model_versions WHERE model_id=$1 ORDER BY created_at DESC`, modelID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := make([]*domain.ModelVersion, 0)
	for rows.Next() {
		v := &domain.ModelVersion{}
		if err := rows.Scan(&v.ID, &v.ModelID, &v.Version, &v.ArtifactURI, &v.ArtifactDigest, &v.ArtifactSize, &v.StorageType, &v.ArtifactVerifiedAt, &v.Runtime, &v.GPUType,
			&v.GPUCount, &v.MemoryMB, &v.ContextLength, &v.Status, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *PostgresRepository) UpdateArtifactMetadata(id, digest string, size int64, verifiedAt time.Time) error {
	res, err := r.db.Exec(`UPDATE model_versions SET artifact_digest=$2, artifact_size=$3, artifact_verified_at=$4, updated_at=$4 WHERE id=$1`, id, digest, size, verifiedAt)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) HasVersionReferences(id string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM deployments WHERE model_version_id=$1)`, id).Scan(&exists)
	return exists, mapErr(err)
}

func (r *PostgresRepository) DeleteVersion(id string) error {
	res, err := r.db.Exec(`DELETE FROM model_versions WHERE id=$1`, id)
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PostgresRepository) UpdateVersionStatus(id, status string) error {
	res, err := r.db.Exec(`UPDATE model_versions SET status=$2, updated_at=$3 WHERE id=$1`, id, status, time.Now())
	if err != nil {
		return mapErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// isUniqueViolation 判断是否唯一约束冲突（lib/pq 错误码 23505）
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}
