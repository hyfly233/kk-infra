package data

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/lib/pq"

	"kk-infra/lib/domain"
)

// PostgresDeploymentRepository controlplane 部署仓库的 Postgres 实现。
// 与 MemoryDeploymentRepository 实现同一接口，可无感切换。
type PostgresDeploymentRepository struct {
	db *sql.DB
}

// NewPostgresDeploymentRepository 创建 Postgres 仓库
func NewPostgresDeploymentRepository(db *sql.DB) *PostgresDeploymentRepository {
	return &PostgresDeploymentRepository{db: db}
}

// mapDeployErr Postgres 错误 → 仓库哨兵错误
func mapDeployErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if isPqUniqueViolation(err) {
		return ErrConflict
	}
	return err
}

// scanDeployment 从行扫描部署对象
func scanDeployment(row interface{ Scan(...any) error }) (*domain.ModelDeployment, error) {
	d := &domain.ModelDeployment{}
	var startupArgs string
	if err := row.Scan(
		&d.ID, &d.Name, &d.ModelID, &d.ModelVersionID, &d.ModelName, &d.ModelVersion,
		&d.TenantID, &d.Namespace, &d.ClusterID, &d.Replicas,
		&d.Resource.GPUType, &d.Resource.GPUCount, &d.Resource.MemoryMB,
		&d.Runtime, &d.ServingMode, &startupArgs, &d.Endpoint, &d.StableEndpoint, &d.CanaryEndpoint, &d.RolloutStatus, &d.Status, &d.Generation,
		&d.Diagnostics, &d.CreatedAt, &d.UpdatedAt,
	); err != nil {
		return nil, err
	}
	// startupArgs 是 JSON 数组
	_ = json.Unmarshal([]byte(startupArgs), &d.StartupArgs)
	return d, nil
}

const deploymentCols = `id, name, model_id, model_version_id, model_name, model_version,
	tenant_id, namespace, cluster_id, replicas, gpu_type, gpu_count, memory_mb,
	runtime, serving_mode, startup_args, endpoint, stable_endpoint, canary_endpoint, rollout_status, status, generation, diagnostics, created_at, updated_at`

// args 序列化部署对象为 SQL 参数
func (r *PostgresDeploymentRepository) args(d *domain.ModelDeployment) []interface{} {
	argsJSON, _ := json.Marshal(d.StartupArgs)
	return []interface{}{
		d.ID, d.Name, d.ModelID, d.ModelVersionID, d.ModelName, d.ModelVersion,
		d.TenantID, d.Namespace, d.ClusterID, d.Replicas,
		d.Resource.GPUType, d.Resource.GPUCount, d.Resource.MemoryMB,
		d.Runtime, d.ServingMode, string(argsJSON), d.Endpoint, d.StableEndpoint, d.CanaryEndpoint, d.RolloutStatus, d.Status, d.Generation,
		d.Diagnostics, d.CreatedAt, d.UpdatedAt,
	}
}

func (r *PostgresDeploymentRepository) Create(d *domain.ModelDeployment) error {
	// 同名已存在且为 DELETED 终态时，物理删除旧记录允许重建（对齐内存 repo 语义）
	var oldStatus string
	err := r.db.QueryRow(`SELECT status FROM deployments WHERE name=$1`, d.Name).Scan(&oldStatus)
	if err == nil && oldStatus == domain.DeploymentStatusDeleted {
		if _, err := r.db.Exec(`DELETE FROM deployments WHERE name=$1`, d.Name); err != nil {
			return mapDeployErr(err)
		}
	}
	_, err = r.db.Exec(
		`INSERT INTO deployments (`+deploymentCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`,
		r.args(d)...,
	)
	return mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) Get(id string) (*domain.ModelDeployment, error) {
	row := r.db.QueryRow(`SELECT `+deploymentCols+` FROM deployments WHERE id=$1`, id)
	d, err := scanDeployment(row)
	return d, mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) GetByName(name string) (*domain.ModelDeployment, error) {
	row := r.db.QueryRow(`SELECT `+deploymentCols+` FROM deployments WHERE name=$1`, name)
	d, err := scanDeployment(row)
	return d, mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) Update(d *domain.ModelDeployment) error {
	// 更新除 id/name/created_at 外的全部字段
	_, err := r.db.Exec(
		`UPDATE deployments SET model_id=$2, model_version_id=$3, model_name=$4, model_version=$5,
		 tenant_id=$6, namespace=$7, cluster_id=$8, replicas=$9, gpu_type=$10, gpu_count=$11, memory_mb=$12,
		 runtime=$13, serving_mode=$14, startup_args=$15, endpoint=$16, stable_endpoint=$17, canary_endpoint=$18, rollout_status=$19, status=$20, generation=$21, diagnostics=$22, updated_at=$23
		 WHERE id=$1`,
		d.ID, d.ModelID, d.ModelVersionID, d.ModelName, d.ModelVersion,
		d.TenantID, d.Namespace, d.ClusterID, d.Replicas,
		d.Resource.GPUType, d.Resource.GPUCount, d.Resource.MemoryMB,
		d.Runtime, d.ServingMode, mustJSON(d.StartupArgs), d.Endpoint, d.StableEndpoint, d.CanaryEndpoint, d.RolloutStatus, d.Status, d.Generation,
		d.Diagnostics, time.Now(),
	)
	return mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) ClaimClusterRebuild(id, fromCluster, toCluster string, at time.Time) (*domain.ModelDeployment, error) {
	row := r.db.QueryRow(`UPDATE deployments SET cluster_id=$3,status='SUBMITTING',diagnostics='',endpoint='',stable_endpoint='',canary_endpoint='',rollout_status='',generation=generation+1,updated_at=$4 WHERE id=$1 AND cluster_id=$2 AND status='FAILED' AND diagnostics LIKE '目标集群不可用:%' RETURNING `+deploymentCols, id, fromCluster, toCluster, at)
	d, err := scanDeployment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrConflict
	}
	return d, mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) List(tenantID string) ([]*domain.ModelDeployment, error) {
	query := `SELECT ` + deploymentCols + ` FROM deployments`
	var args []interface{}
	if tenantID != "" {
		query += ` WHERE tenant_id=$1`
		args = append(args, tenantID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, mapDeployErr(err)
	}
	defer rows.Close()
	out := make([]*domain.ModelDeployment, 0)
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (r *PostgresDeploymentRepository) Delete(id string) error {
	res, err := r.db.Exec(`DELETE FROM deployments WHERE id=$1`, id)
	if err != nil {
		return mapDeployErr(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddEvent 记录状态事件（异步审计，错误仅记日志不影响主流程）
func (r *PostgresDeploymentRepository) AddEvent(e *domain.StatusEvent) {
	_, _ = r.db.Exec(
		`INSERT INTO deployment_events (deployment_id, from_status, to_status, reason, request_id, diagnostics, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.DeploymentID, e.From, e.To, e.Reason, e.RequestID, e.Diagnostics, e.At,
	)
}

func (r *PostgresDeploymentRepository) Events(deploymentID string) []domain.StatusEvent {
	rows, err := r.db.Query(
		`SELECT deployment_id, from_status, to_status, reason, request_id, diagnostics, created_at
		 FROM deployment_events WHERE deployment_id=$1 ORDER BY id`, deploymentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]domain.StatusEvent, 0)
	for rows.Next() {
		var e domain.StatusEvent
		if err := rows.Scan(&e.DeploymentID, &e.From, &e.To, &e.Reason, &e.RequestID, &e.Diagnostics, &e.At); err != nil {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (r *PostgresDeploymentRepository) AddRevision(v *domain.DeploymentRevision) error {
	_, err := r.db.Exec(`INSERT INTO deployment_revisions (id,deployment_id,generation,model_version_id,model_version,created_at) VALUES ($1,$2,$3,$4,$5,$6)`, v.ID, v.DeploymentID, v.Generation, v.ModelVersionID, v.ModelVersion, v.CreatedAt)
	return mapDeployErr(err)
}

func (r *PostgresDeploymentRepository) Revisions(deploymentID string) ([]domain.DeploymentRevision, error) {
	rows, err := r.db.Query(`SELECT id,deployment_id,generation,model_version_id,model_version,created_at FROM deployment_revisions WHERE deployment_id=$1 ORDER BY generation DESC`, deploymentID)
	if err != nil {
		return nil, mapDeployErr(err)
	}
	defer rows.Close()
	out := []domain.DeploymentRevision{}
	for rows.Next() {
		var v domain.DeploymentRevision
		if err := rows.Scan(&v.ID, &v.DeploymentID, &v.Generation, &v.ModelVersionID, &v.ModelVersion, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// mustJSON 序列化 JSON，失败返回 "[]"
func mustJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// isPqUniqueViolation 判断唯一约束冲突（lib/pq 错误码 23505）
func isPqUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}
