package identity

import (
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
)

func TestClusterAgentTokenIsAudienceAndSubjectBound(t *testing.T) {
	const secret = "cluster-agent-test-secret"
	service := NewService(secret)
	now := time.Now()
	token, err := platformauth.IssueAccessToken(secret, "gpu-west", "", platformauth.RolePlatformAdmin, "cluster-agent:gpu-west", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateClusterAgent(token, "gpu-west"); err != nil {
		t.Fatalf("valid agent token rejected: %v", err)
	}
	if _, err := service.AuthenticateClusterAgent(token, "gpu-east"); err == nil {
		t.Fatal("token accepted for another cluster")
	}
	wrongSubject, err := platformauth.IssueAccessToken(secret, "other", "", platformauth.RolePlatformAdmin, "cluster-agent:gpu-west", now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateClusterAgent(wrongSubject, "gpu-west"); err == nil {
		t.Fatal("token with mismatched subject accepted")
	}
}

func TestLoginRefreshLogout(t *testing.T) {
	s := NewService("test-secret")
	if _, err := s.CreateUser("u1", "dev@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember("u1", "tenant-a", platformauth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	login, err := s.Login("dev@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if claims, err := s.Authenticate(login.AccessToken); err != nil || claims.TenantID != "tenant-a" || claims.Role != platformauth.RoleDeveloper {
		t.Fatalf("claims=%+v err=%v", claims, err)
	}
	refreshed, err := s.Refresh(login.RefreshToken)
	if err != nil || refreshed.RefreshToken == login.RefreshToken {
		t.Fatalf("refresh=%+v err=%v", refreshed, err)
	}
	if _, err := s.Refresh(login.RefreshToken); err == nil {
		t.Fatal("rotated refresh token reused")
	}
	s.Logout(refreshed.RefreshToken)
	if _, err := s.Refresh(refreshed.RefreshToken); err == nil {
		t.Fatal("logged-out refresh token reused")
	}
}

func TestBootstrapOnlyOnce(t *testing.T) {
	s := NewService("test-secret")
	if _, err := s.Bootstrap("u1", "admin@example.com", "correct horse battery staple", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login("admin@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if session.Role != platformauth.RolePlatformAdmin {
		t.Fatalf("first account cannot administer clusters: %s", session.Role)
	}
	if _, err := s.Bootstrap("u2", "other@example.com", "correct horse battery staple", "tenant-b"); err == nil {
		t.Fatal("second bootstrap accepted")
	}
}

func TestDisableTenantBlocksTenantState(t *testing.T) {
	s := NewService("test-secret")
	if _, err := s.Bootstrap("u1", "admin@example.com", "correct horse battery staple", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if !s.TenantActive("tenant-a") {
		t.Fatal("new tenant should be active")
	}
	if err := s.DisableTenant("tenant-a"); err != nil {
		t.Fatal(err)
	}
	if s.TenantActive("tenant-a") {
		t.Fatal("disabled tenant accepted")
	}
}

func TestCurrentMembershipRevokesAccessAndRefresh(t *testing.T) {
	for _, change := range []string{"tenant-disabled", "user-disabled", "member-removed", "role-changed"} {
		t.Run(change, func(t *testing.T) {
			s := NewService("secret")
			if _, err := s.CreateUser("u", "u@example.test", "test-password"); err != nil {
				t.Fatal(err)
			}
			if err := s.SetMember("u", "tenant-a", platformauth.RoleTenantAdmin); err != nil {
				t.Fatal(err)
			}
			session, err := s.Login("u@example.test", "test-password", "tenant-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Authenticate(session.AccessToken); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "tenant-disabled":
				if err := s.DisableTenant("tenant-a"); err != nil {
					t.Fatal(err)
				}
			case "user-disabled":
				s.users["u"].Disabled = true
			case "member-removed":
				delete(s.members, memberKey("u", "tenant-a"))
			case "role-changed":
				if err := s.SetMember("u", "tenant-a", platformauth.RoleViewer); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Authenticate(session.AccessToken); err == nil {
				t.Fatal("old access token remains authorized")
			}
			fresh, err := s.Refresh(session.RefreshToken)
			if change == "role-changed" {
				if err != nil || fresh.Role != platformauth.RoleViewer {
					t.Fatalf("refresh preserved old role: %+v %v", fresh, err)
				}
				if _, err := s.Authenticate(fresh.AccessToken); err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("revoked membership refreshed")
				}
				if _, err := s.Login("u@example.test", "test-password", "tenant-a"); err == nil {
					t.Fatal("revoked membership logged in")
				}
			}
		})
	}
}

func TestSignedUnknownPrincipalCannotAuthenticate(t *testing.T) {
	s := NewService("secret")
	token, err := platformauth.IssueAccessToken("secret", "unknown", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); err == nil {
		t.Fatal("signature alone authorized unknown principal")
	}
}
