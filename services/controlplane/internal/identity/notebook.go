package identity

import (
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
	if s.db != nil {
		var disabled, tenantDisabled bool
		var role platformauth.Role
		err := s.db.QueryRow(`SELECT u.disabled,t.disabled,m.role FROM users u JOIN tenant_members m ON m.user_id=u.id JOIN tenants t ON t.id=m.tenant_id WHERE u.id=$1 AND m.tenant_id=$2`, claims.Subject, claims.TenantID).Scan(&disabled, &tenantDisabled, &role)
		if err != nil || disabled || tenantDisabled || role != claims.Role {
			return nil, fmt.Errorf("notebook membership unavailable")
		}
	} else {
		u := s.users[claims.Subject]
		member, ok := s.members[memberKey(claims.Subject, claims.TenantID)]
		if u == nil || u.Disabled || !ok || s.tenants[claims.TenantID] || member.Role != claims.Role {
			return nil, fmt.Errorf("notebook membership unavailable")
		}
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
