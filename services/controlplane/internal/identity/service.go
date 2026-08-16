// Package identity 管理控制面用户会话与租户成员关系。
package identity

import (
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
	secret  string
	now     func() time.Time
}

func NewService(secret string) *Service {
	return &Service{users: map[string]*User{}, byEmail: map[string]string{}, members: map[string]Member{}, refresh: map[string]*RefreshToken{}, secret: secret, now: time.Now}
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
	s.members[memberKey(userID, tenantID)] = Member{UserID: userID, TenantID: tenantID, Role: role}
	return nil
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
	m, ok := s.members[memberKey(id, tenantID)]
	if !ok {
		return nil, fmt.Errorf("tenant access denied")
	}
	return s.issueLocked(u.ID, m)
}

func (s *Service) Refresh(token string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.refresh[platformauth.HashRefreshToken(token)]
	if !ok || r.Revoked || !r.ExpiresAt.After(s.now()) {
		return nil, fmt.Errorf("invalid refresh token")
	}
	r.Revoked = true
	return s.issueLocked(r.UserID, Member{TenantID: r.TenantID, UserID: r.UserID, Role: r.Role})
}
func (s *Service) Logout(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.refresh[platformauth.HashRefreshToken(token)]; r != nil {
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
	return &Session{AccessToken: access, RefreshToken: refresh, TenantID: m.TenantID, Role: m.Role, ExpiresAt: now.Add(15 * time.Minute)}, nil
}

func (s *Service) Authenticate(token string) (*platformauth.Claims, error) {
	return platformauth.ParseAccessToken(s.secret, token, "controlplane")
}
