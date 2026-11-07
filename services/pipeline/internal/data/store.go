package data

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"time"

	"kk-infra/services/pipeline"
)

var ErrNotFound = errors.New("release record not found")

type Store interface {
	Create(*pipeline.ReleaseRecord) error
	Get(string) (*pipeline.ReleaseRecord, error)
	List(int, string) ([]*pipeline.ReleaseRecord, error)
	Update(*pipeline.ReleaseRecord) error
}

type MemoryStore struct {
	mu      sync.RWMutex
	records map[string]*pipeline.ReleaseRecord
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{records: map[string]*pipeline.ReleaseRecord{}}
}

func clone(record *pipeline.ReleaseRecord) *pipeline.ReleaseRecord {
	b, _ := json.Marshal(record)
	var out pipeline.ReleaseRecord
	_ = json.Unmarshal(b, &out)
	return &out
}

func (s *MemoryStore) Create(record *pipeline.ReleaseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[record.ID] = clone(record)
	return nil
}
func (s *MemoryStore) Get(id string) (*pipeline.ReleaseRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	return clone(record), nil
}
func (s *MemoryStore) List(limit int, versionID string) ([]*pipeline.ReleaseRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out := make([]*pipeline.ReleaseRecord, 0, limit)
	for _, record := range s.records {
		if versionID == "" || record.ModelVersionID == versionID {
			out = append(out, clone(record))
		}
	}
	slices.SortFunc(out, func(a, b *pipeline.ReleaseRecord) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (s *MemoryStore) Update(record *pipeline.ReleaseRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[record.ID]; !ok {
		return ErrNotFound
	}
	s.records[record.ID] = clone(record)
	return nil
}

type PostgresStore struct{ db *sql.DB }

func NewPostgresStore(db *sql.DB) *PostgresStore { return &PostgresStore{db: db} }
func (s *PostgresStore) Create(r *pipeline.ReleaseRecord) error {
	stages, _ := json.Marshal(r.StageResults)
	benchmark, _ := json.Marshal(r.Benchmark)
	_, err := s.db.Exec(`INSERT INTO release_records (id,model_version_id,operator,status,stage_results,benchmark,approved_by,approval_message,created_at,updated_at,released_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, r.ID, r.ModelVersionID, r.Operator, r.Status, stages, nullableJSON(r.Benchmark, benchmark), r.ApprovedBy, r.ApprovalMessage, r.CreatedAt, r.UpdatedAt, r.ReleasedAt)
	return err
}
func (s *PostgresStore) Get(id string) (*pipeline.ReleaseRecord, error) {
	r := &pipeline.ReleaseRecord{}
	var stages []byte
	var benchmark []byte
	err := s.db.QueryRow(`SELECT id,model_version_id,operator,status,stage_results,COALESCE(benchmark,'null'),approved_by,approval_message,created_at,updated_at,released_at FROM release_records WHERE id=$1`, id).Scan(&r.ID, &r.ModelVersionID, &r.Operator, &r.Status, &stages, &benchmark, &r.ApprovedBy, &r.ApprovalMessage, &r.CreatedAt, &r.UpdatedAt, &r.ReleasedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(stages, &r.StageResults)
	if string(benchmark) != "null" {
		_ = json.Unmarshal(benchmark, &r.Benchmark)
	}
	return r, nil
}
func (s *PostgresStore) List(limit int, versionID string) ([]*pipeline.ReleaseRecord, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `SELECT id,model_version_id,operator,status,stage_results,COALESCE(benchmark,'null'),approved_by,approval_message,created_at,updated_at,released_at FROM release_records`
	args := []any{}
	if versionID != "" {
		query += ` WHERE model_version_id=$1 ORDER BY created_at DESC LIMIT $2`
		args = append(args, versionID, limit)
	} else {
		query += ` ORDER BY created_at DESC LIMIT $1`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*pipeline.ReleaseRecord, 0)
	for rows.Next() {
		r := &pipeline.ReleaseRecord{}
		var stages, benchmark []byte
		if err := rows.Scan(&r.ID, &r.ModelVersionID, &r.Operator, &r.Status, &stages, &benchmark, &r.ApprovedBy, &r.ApprovalMessage, &r.CreatedAt, &r.UpdatedAt, &r.ReleasedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(stages, &r.StageResults)
		if string(benchmark) != "null" {
			_ = json.Unmarshal(benchmark, &r.Benchmark)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *PostgresStore) Update(r *pipeline.ReleaseRecord) error {
	stages, _ := json.Marshal(r.StageResults)
	benchmark, _ := json.Marshal(r.Benchmark)
	res, err := s.db.Exec(`UPDATE release_records SET status=$2,stage_results=$3,benchmark=$4,approved_by=$5,approval_message=$6,updated_at=$7,released_at=$8 WHERE id=$1`, r.ID, r.Status, stages, nullableJSON(r.Benchmark, benchmark), r.ApprovedBy, r.ApprovalMessage, r.UpdatedAt, r.ReleasedAt)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func nullableJSON(value any, encoded []byte) any {
	if value == nil {
		return nil
	}
	return encoded
}

type AuditEntry struct {
	Action    string
	Actor     string
	Resource  string
	RequestID string
	Detail    string
	CreatedAt time.Time
}

type AuditStore interface {
	Write(AuditEntry) error
}

type MemoryAuditStore struct {
	mu      sync.Mutex
	Entries []AuditEntry
}

func (s *MemoryAuditStore) Write(entry AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Entries = append(s.Entries, entry)
	return nil
}

type PostgresAuditStore struct{ db *sql.DB }

func NewPostgresAuditStore(db *sql.DB) *PostgresAuditStore { return &PostgresAuditStore{db: db} }

func (s *PostgresAuditStore) Write(entry AuditEntry) error {
	_, err := s.db.Exec(`INSERT INTO audit_logs (action, actor, resource, request_id, detail, created_at)
		VALUES ($1,$2,$3,$4,$5,$6)`, entry.Action, entry.Actor, entry.Resource, entry.RequestID, entry.Detail, entry.CreatedAt)
	return err
}
