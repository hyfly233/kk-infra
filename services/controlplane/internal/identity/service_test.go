package identity

import (
	"testing"

	platformauth "kk-infra/lib/auth"
)

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
