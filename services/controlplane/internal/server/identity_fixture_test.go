package server

import (
	platformauth "kk-infra/lib/auth"
	"kk-infra/services/controlplane/internal/identity"
	"testing"
)

// Management tests need registered principals, not just correctly signed JWTs.
func managementIdentity(t *testing.T, secret string) *identity.Service {
	t.Helper()
	s := identity.NewService(secret)
	if _, err := s.Bootstrap("admin", "admin@example.test", "test-password", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"viewer", "user-1", "user", "u"} {
		if _, err := s.CreateUser(id, id+"@example.test", "test-password"); err != nil {
			t.Fatal(err)
		}
		role := platformauth.RoleDeveloper
		if id == "viewer" {
			role = platformauth.RoleViewer
		}
		if err := s.SetMember(id, "tenant-a", role); err != nil {
			t.Fatal(err)
		}
	}
	return s
}
