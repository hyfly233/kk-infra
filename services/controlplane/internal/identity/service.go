// Package identity 管理控制面用户会话与租户成员关系。
package identity

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	platformauth "kk-infra/lib/auth"
)

type User struct {
	ID, Email, PasswordHash string
	Disabled                bool
}
type Member struct {
	TenantID, UserID string
	Role             platformauth.Role
}
type RefreshToken struct {
	Hash, UserID, TenantID string
	Role                   platformauth.Role
	ExpiresAt              time.Time
	Revoked                bool
}
type Session struct {
	AccessToken  string            `json:"accessToken"`
	RefreshToken string            `json:"refreshToken"`
	TenantID     string            `json:"tenantId"`
	Role         platformauth.Role `json:"role"`
	ExpiresAt    time.Time         `json:"expiresAt"`
}

// Service 是线程安全的身份用例；持久化实现将复用相同的操作边界。
type Service struct {
	mu      sync.Mutex
	users   map[string]*User
	byEmail map[string]string
	members map[string]Member
	refresh map[string]*RefreshToken
	tenants map[string]bool
	secret  string
	now     func() time.Time
	db      *sql.DB
}

func NewService(secret string) *Service {
	return &Service{users: map[string]*User{}, byEmail: map[string]string{}, members: map[string]Member{}, refresh: map[string]*RefreshToken{}, tenants: map[string]bool{}, secret: secret, now: time.Now}
}

func (s *Service) AuthenticateService(token, caller string) error {
	return platformauth.AuthenticateService(s.secret, token, "controlplane", caller)
}

// NewPostgresService 启动时恢复用户和租户成员；refresh token 在请求时再验证数据库状态。
func NewPostgresService(db *sql.DB, secret string) (*Service, error) {
	s := NewService(secret)
	s.db = db
	rows, err := db.Query(`SELECT id, email, password_hash, disabled FROM users`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u := &User{}
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Disabled); err != nil {
			return nil, err
		}
		s.users[u.ID] = u
		s.byEmail[u.Email] = u.ID
	}
	members, err := db.Query(`SELECT tenant_id, user_id, role FROM tenant_members`)
	if err != nil {
		return nil, err
	}
	defer members.Close()
	for members.Next() {
		var m Member
		if err := members.Scan(&m.TenantID, &m.UserID, &m.Role); err != nil {
			return nil, err
		}
		s.members[memberKey(m.UserID, m.TenantID)] = m
	}
	tenantRows, err := db.Query(`SELECT id, disabled FROM tenants`)
	if err != nil {
		return nil, err
	}
	defer tenantRows.Close()
	for tenantRows.Next() {
		var id string
		var disabled bool
		if err := tenantRows.Scan(&id, &disabled); err != nil {
			return nil, err
		}
		s.tenants[id] = disabled
	}
	return s, rows.Err()
}

func (s *Service) saveTenant(id string, disabled bool) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO tenants (id,disabled) VALUES ($1,$2) ON CONFLICT (id) DO UPDATE SET disabled=EXCLUDED.disabled,updated_at=now()`, id, disabled)
	return err
}

func (s *Service) TenantActive(tenantID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	disabled, ok := s.tenants[tenantID]
	return !ok || !disabled
}

// TenantServingActive does not allow unknown tenants or stale PostgreSQL snapshots.
func (s *Service) TenantServingActive(ctx context.Context, tenantID string) (bool, error) {
	if tenantID == "" {
		return false, nil
	}
	if s.db != nil {
		var disabled bool
		err := s.db.QueryRowContext(ctx, `SELECT disabled FROM tenants WHERE id=$1`, tenantID).Scan(&disabled)
		if err == sql.ErrNoRows {
			return false, nil
		}
		return !disabled && err == nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	disabled, exists := s.tenants[tenantID]
	return exists && !disabled, nil
}
func (s *Service) DisableTenant(tenantID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.saveTenant(tenantID, true); err != nil {
		return err
	}
	s.tenants[tenantID] = true
	return nil
}

func (s *Service) saveUser(u *User) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO users (id,email,password_hash,disabled) VALUES ($1,$2,$3,$4)`, u.ID, u.Email, u.PasswordHash, u.Disabled)
	return err
}
func (s *Service) saveMember(m Member) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO tenant_members (tenant_id,user_id,role) VALUES ($1,$2,$3) ON CONFLICT (tenant_id,user_id) DO UPDATE SET role=EXCLUDED.role`, m.TenantID, m.UserID, m.Role)
	return err
}
func (s *Service) saveRefresh(r *RefreshToken) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO refresh_tokens (id,user_id,token_hash,expires_at,revoked_at) VALUES ($1,$2,$3,$4,NULL)`, r.Hash, r.UserID, r.Hash, r.ExpiresAt)
	return err
}
func (s *Service) revokeRefresh(hash string) error {
	if s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`UPDATE refresh_tokens SET revoked_at=now() WHERE token_hash=$1 AND revoked_at IS NULL`, hash)
	return err
}
func (s *Service) loadRefresh(hash string) (*RefreshToken, error) {
	if r := s.refresh[hash]; r != nil {
		return r, nil
	}
	if s.db == nil {
		return nil, nil
	}
	r := &RefreshToken{Hash: hash}
	var revokedAt sql.NullTime
	err := s.db.QueryRow(`SELECT user_id, expires_at, revoked_at FROM refresh_tokens WHERE token_hash=$1`, hash).Scan(&r.UserID, &r.ExpiresAt, &revokedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Revoked = revokedAt.Valid
	for _, m := range s.members {
		if m.UserID == r.UserID {
			r.TenantID = m.TenantID
			r.Role = m.Role
			break
		}
	}
	if r.TenantID == "" {
		return nil, fmt.Errorf("refresh token tenant membership missing")
	}
	s.refresh[hash] = r
	return r, nil
}

// Bootstrap 创建首个平台管理员；只允许在完全空的身份仓库中执行一次。
func (s *Service) Bootstrap(id, email, password, tenantID string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) != 0 {
		return nil, fmt.Errorf("bootstrap is already complete")
	}
	if id == "" || email == "" || tenantID == "" {
		return nil, fmt.Errorf("id, email and tenant are required")
	}
	hash, err := platformauth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{ID: id, Email: email, PasswordHash: hash}
	if err := s.saveUser(u); err != nil {
		return nil, err
	}
	m := Member{UserID: id, TenantID: tenantID, Role: platformauth.RolePlatformAdmin}
	if err := s.saveTenant(tenantID, false); err != nil {
		return nil, err
	}
	if err := s.saveMember(m); err != nil {
		return nil, err
	}
	s.users[id] = u
	s.byEmail[email] = id
	s.members[memberKey(id, tenantID)] = m
	s.tenants[tenantID] = false
	return u, nil
}

func memberKey(userID, tenantID string) string { return userID + "\x00" + tenantID }

func (s *Service) CreateUser(id, email, password string) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id == "" || email == "" {
		return nil, fmt.Errorf("id and email are required")
	}
	if _, ok := s.byEmail[email]; ok {
		return nil, fmt.Errorf("email already exists")
	}
	hash, err := platformauth.HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{ID: id, Email: email, PasswordHash: hash}
	if err := s.saveUser(u); err != nil {
		return nil, err
	}
	s.users[id] = u
	s.byEmail[email] = id
	return u, nil
}

func (s *Service) SetMember(userID, tenantID string, role platformauth.Role) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[userID]; !ok {
		return fmt.Errorf("user not found")
	}
	if role != platformauth.RolePlatformAdmin && role != platformauth.RoleTenantAdmin && role != platformauth.RoleDeveloper && role != platformauth.RoleViewer {
		return fmt.Errorf("invalid role")
	}
	m := Member{UserID: userID, TenantID: tenantID, Role: role}
	if tenantID == "" {
		return fmt.Errorf("tenant required")
	}
	if _, exists := s.tenants[tenantID]; !exists {
		if s.db != nil {
			if _, err := s.db.Exec(`INSERT INTO tenants(id,disabled) VALUES($1,false) ON CONFLICT(id) DO NOTHING`, tenantID); err != nil {
				return err
			}
		}
		s.tenants[tenantID] = false
	}
	if err := s.saveMember(m); err != nil {
		return err
	}
	s.members[memberKey(userID, tenantID)] = m
	return nil
}

func (s *Service) Members(tenantID string) []Member {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Member, 0)
	for _, member := range s.members {
		if tenantID == "" || member.TenantID == tenantID {
			result = append(result, member)
		}
	}
	return result
}

func (s *Service) Login(email, password, tenantID string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byEmail[email]
	if !ok {
		return nil, fmt.Errorf("invalid credentials")
	}
	u := s.users[id]
	if u.Disabled || !platformauth.VerifyPassword(u.PasswordHash, password) {
		return nil, fmt.Errorf("invalid credentials")
	}
	m, err := s.currentMemberLocked(context.Background(), id, tenantID)
	if err != nil {
		return nil, fmt.Errorf("tenant access denied")
	}
	return s.issueLocked(u.ID, m)
}

func (s *Service) Refresh(token string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := platformauth.HashRefreshToken(token)
	r, err := s.loadRefresh(hash)
	if err != nil || r == nil || r.Revoked || !r.ExpiresAt.After(s.now()) {
		return nil, fmt.Errorf("invalid refresh token")
	}
	m, err := s.currentMemberLocked(context.Background(), r.UserID, r.TenantID)
	if err != nil {
		return nil, err
	}
	if err := s.revokeRefresh(hash); err != nil {
		return nil, err
	}
	r.Revoked = true
	return s.issueLocked(r.UserID, m)
}
func (s *Service) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash := platformauth.HashRefreshToken(token)
	if r, _ := s.loadRefresh(hash); r != nil {
		_ = s.revokeRefresh(hash)
		r.Revoked = true
	}
}

func (s *Service) issueLocked(userID string, m Member) (*Session, error) {
	now := s.now()
	access, err := platformauth.IssueAccessToken(s.secret, userID, m.TenantID, m.Role, "controlplane", now, 15*time.Minute)
	if err != nil {
		return nil, err
	}
	refresh, hash, err := platformauth.NewRefreshToken()
	if err != nil {
		return nil, err
	}
	exp := now.Add(30 * 24 * time.Hour)
	s.refresh[hash] = &RefreshToken{Hash: hash, UserID: userID, TenantID: m.TenantID, Role: m.Role, ExpiresAt: exp}
	if err := s.saveRefresh(s.refresh[hash]); err != nil {
		return nil, err
	}
	return &Session{AccessToken: access, RefreshToken: refresh, TenantID: m.TenantID, Role: m.Role, ExpiresAt: now.Add(15 * time.Minute)}, nil
}

func (s *Service) Authenticate(token string) (*platformauth.Claims, error) {
	claims, err := platformauth.ParseAccessToken(s.secret, token, "controlplane")
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.currentMemberLocked(context.Background(), claims.Subject, claims.TenantID)
	if err != nil || m.Role != claims.Role {
		return nil, fmt.Errorf("current membership unavailable or role changed")
	}
	return claims, nil
}

// Caller holds s.mu. A bounded live query prevents multi-process role snapshots
// from keeping a revoked or downgraded identity authorized.
func (s *Service) currentMemberLocked(ctx context.Context, userID, tenantID string) (Member, error) {
	m := Member{UserID: userID, TenantID: tenantID}
	if s.db != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		var disabled, tenantDisabled bool
		err := s.db.QueryRowContext(ctx, `SELECT u.disabled,t.disabled,m.role FROM users u JOIN tenant_members m ON m.user_id=u.id JOIN tenants t ON t.id=m.tenant_id WHERE u.id=$1 AND m.tenant_id=$2`, userID, tenantID).Scan(&disabled, &tenantDisabled, &m.Role)
		if err != nil || disabled || tenantDisabled {
			return Member{}, fmt.Errorf("current membership unavailable")
		}
		return m, nil
	}
	u := s.users[userID]
	m, ok := s.members[memberKey(userID, tenantID)]
	disabled, exists := s.tenants[tenantID]
	if u == nil || u.Disabled || !ok || !exists || disabled {
		return Member{}, fmt.Errorf("current membership unavailable")
	}
	return m, nil
}

func (s *Service) IssueClusterMonitorToken() (string, time.Time, error) {
	now := s.now()
	token, err := platformauth.IssueAccessToken(s.secret, "cluster-monitor", "", platformauth.RoleViewer, "cluster-monitor", now, 24*time.Hour)
	return token, now.Add(24 * time.Hour), err
}

func (s *Service) AuthenticateClusterMonitor(token string) error {
	claims, err := platformauth.ParseAccessToken(s.secret, token, "cluster-monitor")
	if err != nil || claims.Subject != "cluster-monitor" || claims.Role != platformauth.RoleViewer {
		return fmt.Errorf("invalid cluster monitor token")
	}
	return nil
}

func (s *Service) AuthenticateClusterAgent(token, clusterID string) (*platformauth.Claims, error) {
	claims, err := platformauth.ParseAccessToken(s.secret, token, "cluster-agent:"+clusterID)
	if err != nil || claims.Subject != clusterID {
		return nil, fmt.Errorf("invalid cluster agent token")
	}
	return claims, nil
}

// IssueClusterAgentToken limits the credential to one cluster's heartbeat API.
func (s *Service) IssueClusterAgentToken(clusterID string) (string, time.Time, error) {
	now := s.now()
	expires := now.Add(24 * time.Hour)
	token, err := platformauth.IssueAccessToken(s.secret, clusterID, "", platformauth.RoleViewer, "cluster-agent:"+clusterID, now, 24*time.Hour)
	return token, expires, err
}
