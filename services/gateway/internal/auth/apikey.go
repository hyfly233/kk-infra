// Package auth API Key 管理。
// 安全要求：Key 明文只在创建时返回一次，存储只保留 SHA-256 哈希。
// 存储抽象为 Store 接口：内存（默认）或 Postgres 可插拔。
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// ErrInvalidKey API Key 无效
var ErrInvalidKey = errors.New("invalid api key")

// ErrKeyDisabled Key 已禁用
var ErrKeyDisabled = errors.New("api key disabled")

// APIKey 存储模型
type APIKey struct {
	ID         string    `json:"id"`
	KeyHash    string    `json:"-"` // SHA-256，不返回管理 API
	TenantID   string    `json:"tenantId"`
	Models     []string  `json:"models"` // 可访问模型白名单（空 = 全部）
	CreatedAt  time.Time `json:"createdAt"`
	LastUsedAt time.Time `json:"lastUsedAt,omitempty"`
	Disabled   bool      `json:"disabled"`
}

// CanAccessModel 校验 Key 是否有权访问某模型（白名单为空 = 全部放行）
func (k *APIKey) CanAccessModel(model string) bool {
	if k == nil || len(k.Models) == 0 {
		return true
	}
	for _, m := range k.Models {
		if m == model {
			return true
		}
	}
	return false
}

// Store API Key 存储接口（内存 / Postgres 可插拔）
type Store interface {
	// Save 保存新 Key（keyHash 唯一）
	Save(k *APIKey) error
	// GetByHash 按哈希查询
	GetByHash(hash string) (*APIKey, error)
	// GetByID 按 ID 查询
	GetByID(id string) (*APIKey, error)
	// Update 更新 Key（禁用/最近调用时间）
	Update(k *APIKey) error
	// List 列出全部 Key（元数据）
	List() ([]APIKey, error)
}

// MemoryStore 内存实现（默认）
type MemoryStore struct {
	mu   sync.RWMutex
	keys map[string]*APIKey // keyHash → key
	byID map[string]string  // id → keyHash
	seq  int
}

// NewMemoryStore 创建内存存储
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{keys: make(map[string]*APIKey), byID: make(map[string]string)}
}

func (s *MemoryStore) Save(k *APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	if k.ID == "" {
		k.ID = "k" + itoa(s.seq)
	}
	s.keys[k.KeyHash] = k
	s.byID[k.ID] = k.KeyHash
	return nil
}

func (s *MemoryStore) GetByHash(hash string) (*APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.keys[hash]
	if !ok {
		return nil, ErrInvalidKey
	}
	return k, nil
}

func (s *MemoryStore) GetByID(id string) (*APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hash, ok := s.byID[id]
	if !ok {
		return nil, ErrInvalidKey
	}
	return s.keys[hash], nil
}

func (s *MemoryStore) Update(k *APIKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	old, ok := s.keys[k.KeyHash]
	if !ok {
		// 按 ID 更新（keyHash 可能不变，仅改 disabled/lastUsed）
		hash, idOk := s.byID[k.ID]
		if !idOk {
			return ErrInvalidKey
		}
		old = s.keys[hash]
		_ = old
	}
	s.keys[k.KeyHash] = k
	s.byID[k.ID] = k.KeyHash
	return nil
}

func (s *MemoryStore) List() ([]APIKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]APIKey, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, *k)
	}
	return out, nil
}

// PostgresStore Postgres 实现
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore 创建 Postgres 存储
func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

func (s *PostgresStore) Save(k *APIKey) error {
	modelsJSON, _ := json.Marshal(k.Models)
	_, err := s.db.Exec(
		`INSERT INTO api_keys (id, key_hash, tenant_id, models, created_at, last_used_at, disabled)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		k.ID, k.KeyHash, k.TenantID, string(modelsJSON), k.CreatedAt, nullTime(k.LastUsedAt), k.Disabled,
	)
	return err
}

func (s *PostgresStore) GetByHash(hash string) (*APIKey, error) {
	k := &APIKey{}
	var last sql.NullTime
	var modelsJSON string
	err := s.db.QueryRow(
		`SELECT id, key_hash, tenant_id, models, created_at, last_used_at, disabled FROM api_keys WHERE key_hash=$1`, hash,
	).Scan(&k.ID, &k.KeyHash, &k.TenantID, &modelsJSON, &k.CreatedAt, &last, &k.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(modelsJSON), &k.Models)
	if last.Valid {
		k.LastUsedAt = last.Time
	}
	return k, nil
}

func (s *PostgresStore) GetByID(id string) (*APIKey, error) {
	k := &APIKey{}
	var last sql.NullTime
	var modelsJSON string
	err := s.db.QueryRow(
		`SELECT id, key_hash, tenant_id, models, created_at, last_used_at, disabled FROM api_keys WHERE id=$1`, id,
	).Scan(&k.ID, &k.KeyHash, &k.TenantID, &modelsJSON, &k.CreatedAt, &last, &k.Disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(modelsJSON), &k.Models)
	if last.Valid {
		k.LastUsedAt = last.Time
	}
	return k, nil
}

func (s *PostgresStore) Update(k *APIKey) error {
	modelsJSON, _ := json.Marshal(k.Models)
	_, err := s.db.Exec(
		`UPDATE api_keys SET tenant_id=$2, models=$3, last_used_at=$4, disabled=$5 WHERE id=$1`,
		k.ID, k.TenantID, string(modelsJSON), nullTime(k.LastUsedAt), k.Disabled,
	)
	return err
}

func (s *PostgresStore) List() ([]APIKey, error) {
	rows, err := s.db.Query(`SELECT id, key_hash, tenant_id, models, created_at, last_used_at, disabled FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]APIKey, 0)
	for rows.Next() {
		var k APIKey
		var last sql.NullTime
		var modelsJSON string
		if err := rows.Scan(&k.ID, &k.KeyHash, &k.TenantID, &modelsJSON, &k.CreatedAt, &last, &k.Disabled); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(modelsJSON), &k.Models)
		if last.Valid {
			k.LastUsedAt = last.Time
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// nullTime time.Time → sql.NullTime
func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t, Valid: true}
}

// Manager API Key 管理器（基于可插拔 Store）
type Manager struct {
	store Store
	mu    sync.Mutex // 保护并发 Issue 的 ID 生成
}

// NewManager 创建管理器（内存存储）
func NewManager() *Manager {
	return &Manager{store: NewMemoryStore()}
}

// NewManagerWithStore 使用指定存储创建管理器
func NewManagerWithStore(s Store) *Manager {
	return &Manager{store: s}
}

// IssueResult 创建结果（明文只出现一次）
type IssueResult struct {
	Key    string `json:"key"` // 明文 Key，仅此一次
	KeyID  string `json:"keyId"`
	Tenant string `json:"tenant"`
}

// Issue 创建新 API Key
func (m *Manager) Issue(tenantID string) (*IssueResult, error) {
	plain, err := generateKey()
	if err != nil {
		return nil, err
	}
	hash := hashKey(plain)
	m.mu.Lock()
	defer m.mu.Unlock()
	k := &APIKey{
		ID:        "k-" + randHex(10),
		KeyHash:   hash,
		TenantID:  tenantID,
		CreatedAt: time.Now(),
	}
	if err := m.store.Save(k); err != nil {
		return nil, err
	}
	return &IssueResult{Key: plain, KeyID: k.ID, Tenant: tenantID}, nil
}

// Authenticate 校验 Key，返回租户
func (m *Manager) Authenticate(plain string) (string, error) {
	if plain == "" {
		return "", ErrInvalidKey
	}
	hash := hashKey(plain)
	k, err := m.store.GetByHash(hash)
	if err != nil {
		return "", ErrInvalidKey
	}
	if k.Disabled {
		return "", ErrKeyDisabled
	}
	// 更新最近调用时间（异步，不影响主路径）
	k.LastUsedAt = time.Now()
	_ = m.store.Update(k)
	return k.TenantID, nil
}

// Disable 禁用 Key
func (m *Manager) Disable(keyID string) error {
	k, err := m.store.GetByID(keyID)
	if err != nil {
		return ErrInvalidKey
	}
	k.Disabled = true
	return m.store.Update(k)
}

// TenantForKey reads immutable ownership without exposing credential hashes.
func (m *Manager) TenantForKey(keyID string) (string, error) {
	k, err := m.store.GetByID(keyID)
	if err != nil {
		return "", err
	}
	return k.TenantID, nil
}

// SetModels 设置 Key 的模型白名单（空 = 全部模型可访问）
func (m *Manager) SetModels(keyID string, models []string) error {
	k, err := m.store.GetByID(keyID)
	if err != nil {
		return ErrInvalidKey
	}
	k.Models = models
	return m.store.Update(k)
}

// CanAccess 校验 Key 是否有权访问某模型（需先通过 Authenticate）
func (m *Manager) CanAccess(plain, model string) bool {
	if plain == "" {
		return false
	}
	hash := hashKey(plain)
	k, err := m.store.GetByHash(hash)
	if err != nil {
		return false
	}
	return k.CanAccessModel(model)
}

// Rotate 轮换 Key：禁用旧的并签发新的
func (m *Manager) Rotate(oldKeyID, tenantID string) (*IssueResult, error) {
	if err := m.Disable(oldKeyID); err != nil {
		return nil, err
	}
	return m.Issue(tenantID)
}

// List 列出 Key（不含哈希明文，仅元数据）
func (m *Manager) List() []APIKey {
	out, _ := m.store.List()
	return out
}

// generateKey 生成 sk-carrot-<hex> 格式 Key
func generateKey() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sk-carrot-" + hex.EncodeToString(b), nil
}

// randHex 生成 n 字节随机 hex（用于 Key ID）
func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// hashKey SHA-256 哈希
func hashKey(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

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
