package identity

import (
	"context"
	"fmt"
	platformauth "kk-infra/lib/auth"
	"regexp"
	"time"
)

// Leave seven characters for the tenant- Namespace prefix.
var notebookTenant = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,54}[a-z0-9])?$`)

// NotebookIdentity rechecks membership rather than trusting a still-valid JWT
// after a tenant is disabled or the user's role changes.
func (s *Service) NotebookIdentity(token, audience string) (*platformauth.Claims, error) {
	if audience != "controlplane" && audience != "notebook-spawn" {
		return nil, fmt.Errorf("invalid notebook audience")
	}
	claims, err := platformauth.ParseAccessToken(s.secret, token, audience)
	if err != nil || !notebookTenant.MatchString(claims.TenantID) {
		return nil, fmt.Errorf("invalid notebook identity")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	member, err := s.currentMemberLocked(context.Background(), claims.Subject, claims.TenantID)
	if err != nil || member.Role != claims.Role {
		return nil, fmt.Errorf("notebook membership unavailable")
	}
	return claims, nil
}

func (s *Service) NotebookSpawnToken(claims *platformauth.Claims) (string, error) {
	if claims.Role == platformauth.RoleViewer {
		return "", fmt.Errorf("read-only notebook identity")
	}
	return platformauth.IssueAccessToken(s.secret, claims.Subject, claims.TenantID, claims.Role, "notebook-spawn", s.now(), 2*time.Minute)
}

func (s *Service) IssueNotebookHubToken() (string, time.Time, error) {
	now := s.now()
	expires := now.Add(24 * time.Hour)
	token, err := platformauth.IssueAccessToken(s.secret, "notebook-hub", "", platformauth.RoleViewer, "notebook-hub", now, 24*time.Hour)
	return token, expires, err
}

func (s *Service) AuthenticateNotebookHub(token string) error {
	c, err := platformauth.ParseAccessToken(s.secret, token, "notebook-hub")
	if err != nil || c.Subject != "notebook-hub" || c.Role != platformauth.RoleViewer {
		return fmt.Errorf("invalid notebook hub identity")
	}
	return nil
}
