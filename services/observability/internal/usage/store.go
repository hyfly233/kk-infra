// Package usage 持久化网关请求用量，作为账单与日聚合的原始账本。
package usage

import (
	"context"
	"database/sql"
	"time"
)

type Record struct {
	TenantID, DeploymentID, ModelID string
	InputTokens, OutputTokens       int
	LatencyMs                       int64
	Failed                          bool
	CreatedAt                       time.Time
}
type Store interface {
	Record(context.Context, Record) error
}
type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }
func (s *PostgresStore) Record(ctx context.Context, r Record) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage_records (tenant_id,deployment_id,model_id,input_tokens,output_tokens,latency_ms,failed,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, r.TenantID, r.DeploymentID, r.ModelID, r.InputTokens, r.OutputTokens, r.LatencyMs, r.Failed, r.CreatedAt)
	return err
}
