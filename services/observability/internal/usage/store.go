// Package usage 持久化网关请求用量，作为账单与日聚合的原始账本。
package usage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync"
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
	SetRateCard(context.Context, RateCard) error
	ListDaily(context.Context, string, time.Time, time.Time) ([]DailyUsage, error)
	AccrueGPU(context.Context, time.Time, time.Duration) error
}
type RateCard struct {
	TenantID              string  `json:"tenantId"`
	GPUType               string  `json:"gpuType"`
	InputTokenPerMillion  float64 `json:"inputTokenPerMillion"`
	OutputTokenPerMillion float64 `json:"outputTokenPerMillion"`
	GPUHour               float64 `json:"gpuHour"`
}
type DailyUsage struct {
	Date              time.Time `json:"date"`
	TenantID          string    `json:"tenantId"`
	DeploymentID      string    `json:"deploymentId"`
	InputTokens       int64     `json:"inputTokens"`
	OutputTokens      int64     `json:"outputTokens"`
	RequestCount      int64     `json:"requestCount"`
	FailedCount       int64     `json:"failedCount"`
	GPUReplicaSeconds int64     `json:"gpuReplicaSeconds"`
	EstimatedCost     float64   `json:"estimatedCost"`
}
type PostgresStore struct{ db *sql.DB }
type MemoryStore struct {
	mu    sync.Mutex
	cards map[string]RateCard
	daily map[string]*DailyUsage
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{cards: map[string]RateCard{}, daily: map[string]*DailyUsage{}}
}
func memoryKey(day time.Time, tenant, deployment string) string {
	return day.UTC().Format("2006-01-02") + "\x00" + tenant + "\x00" + deployment
}
func (m *MemoryStore) Record(_ context.Context, r Record) error {
	if r.TenantID == "" || r.DeploymentID == "" {
		return fmt.Errorf("tenant and deployment are required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	day := r.CreatedAt.UTC().Truncate(24 * time.Hour)
	key := memoryKey(day, r.TenantID, r.DeploymentID)
	d := m.daily[key]
	if d == nil {
		d = &DailyUsage{Date: day, TenantID: r.TenantID, DeploymentID: r.DeploymentID}
		m.daily[key] = d
	}
	d.InputTokens += int64(r.InputTokens)
	d.OutputTokens += int64(r.OutputTokens)
	d.RequestCount++
	if r.Failed {
		d.FailedCount++
	}
	d.EstimatedCost += TokenCost(int64(r.InputTokens), int64(r.OutputTokens), m.cards[r.TenantID+"\x00default"])
	return nil
}
func (m *MemoryStore) SetRateCard(_ context.Context, r RateCard) error {
	if r.TenantID == "" || r.GPUType == "" || r.InputTokenPerMillion < 0 || r.OutputTokenPerMillion < 0 || r.GPUHour < 0 {
		return fmt.Errorf("invalid rate card")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cards[r.TenantID+"\x00"+r.GPUType] = r
	return nil
}
func (m *MemoryStore) ListDaily(_ context.Context, tenant string, from, to time.Time) ([]DailyUsage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]DailyUsage, 0)
	for _, d := range m.daily {
		if d.TenantID == tenant && !d.Date.Before(from.UTC().Truncate(24*time.Hour)) && !d.Date.After(to.UTC().Truncate(24*time.Hour)) {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date.Equal(out[j].Date) {
			return out[i].DeploymentID < out[j].DeploymentID
		}
		return out[i].Date.Before(out[j].Date)
	})
	return out, nil
}
func (m *MemoryStore) AccrueGPU(context.Context, time.Time, time.Duration) error { return nil }

func TokenCost(inputTokens, outputTokens int64, card RateCard) float64 {
	return float64(inputTokens)*card.InputTokenPerMillion/1_000_000 + float64(outputTokens)*card.OutputTokenPerMillion/1_000_000
}
func GPUCost(replicaSeconds int64, card RateCard) float64 {
	return float64(replicaSeconds) / 3600 * card.GPUHour
}

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }
func (s *PostgresStore) Record(ctx context.Context, r Record) error {
	if r.TenantID == "" || r.DeploymentID == "" {
		return fmt.Errorf("tenant and deployment are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT INTO usage_records (tenant_id,deployment_id,model_id,input_tokens,output_tokens,latency_ms,failed,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, r.TenantID, r.DeploymentID, r.ModelID, r.InputTokens, r.OutputTokens, r.LatencyMs, r.Failed, r.CreatedAt); err != nil {
		return err
	}
	failed := 0
	if r.Failed {
		failed = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daily_usage (usage_date,tenant_id,deployment_id,input_tokens,output_tokens,request_count,failed_count,estimated_cost) VALUES ($1,$2,$3,$4,$5,1,$6,COALESCE((SELECT ($4*input_token_per_million+$5*output_token_per_million)/1000000 FROM rate_cards WHERE tenant_id=$2 AND gpu_type='default'),0)) ON CONFLICT (usage_date,tenant_id,deployment_id) DO UPDATE SET input_tokens=daily_usage.input_tokens+EXCLUDED.input_tokens,output_tokens=daily_usage.output_tokens+EXCLUDED.output_tokens,request_count=daily_usage.request_count+1,failed_count=daily_usage.failed_count+EXCLUDED.failed_count,estimated_cost=daily_usage.estimated_cost+EXCLUDED.estimated_cost`, r.CreatedAt.UTC().Truncate(24*time.Hour), r.TenantID, r.DeploymentID, r.InputTokens, r.OutputTokens, failed)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *PostgresStore) SetRateCard(ctx context.Context, r RateCard) error {
	if r.TenantID == "" || r.GPUType == "" || r.InputTokenPerMillion < 0 || r.OutputTokenPerMillion < 0 || r.GPUHour < 0 {
		return fmt.Errorf("invalid rate card")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO rate_cards (tenant_id,gpu_type,input_token_per_million,output_token_per_million,gpu_hour) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (tenant_id,gpu_type) DO UPDATE SET input_token_per_million=EXCLUDED.input_token_per_million,output_token_per_million=EXCLUDED.output_token_per_million,gpu_hour=EXCLUDED.gpu_hour`, r.TenantID, r.GPUType, r.InputTokenPerMillion, r.OutputTokenPerMillion, r.GPUHour)
	return err
}

func (s *PostgresStore) ListDaily(ctx context.Context, tenantID string, from, to time.Time) ([]DailyUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT usage_date,tenant_id,deployment_id,input_tokens,output_tokens,request_count,failed_count,gpu_replica_seconds,estimated_cost FROM daily_usage WHERE tenant_id=$1 AND usage_date BETWEEN $2 AND $3 ORDER BY usage_date, deployment_id`, tenantID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DailyUsage
	for rows.Next() {
		var d DailyUsage
		if err := rows.Scan(&d.Date, &d.TenantID, &d.DeploymentID, &d.InputTokens, &d.OutputTokens, &d.RequestCount, &d.FailedCount, &d.GPUReplicaSeconds, &d.EstimatedCost); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *PostgresStore) AccrueGPU(ctx context.Context, at time.Time, interval time.Duration) error {
	seconds := int64(interval / time.Second)
	if seconds <= 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO daily_usage (usage_date,tenant_id,deployment_id,gpu_replica_seconds,estimated_cost) SELECT $1,d.tenant_id,d.id,(d.replicas*d.gpu_count*$2)::bigint,COALESCE((d.replicas*d.gpu_count*$2/3600.0)*r.gpu_hour,0) FROM deployments d LEFT JOIN rate_cards r ON r.tenant_id=d.tenant_id AND r.gpu_type=d.gpu_type WHERE d.status='RUNNING' ON CONFLICT (usage_date,tenant_id,deployment_id) DO UPDATE SET gpu_replica_seconds=daily_usage.gpu_replica_seconds+EXCLUDED.gpu_replica_seconds,estimated_cost=daily_usage.estimated_cost+EXCLUDED.estimated_cost`, at.UTC().Truncate(24*time.Hour), seconds)
	return err
}
