package identity

import (
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
)

func TestNotebookIdentityRechecksMembershipAndAudience(t *testing.T) {
	s := NewService("notebook-test-secret")
	if _, err := s.CreateUser("u1", "dev@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMember("u1", "tenant-a", platformauth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	session, err := s.Login("dev@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.NotebookIdentity(session.AccessToken, "controlplane")
	if err != nil {
		t.Fatal(err)
	}
	grant, err := s.NotebookSpawnToken(claims)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotebookIdentity(grant, "notebook-spawn"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotebookIdentity(grant, "controlplane"); err == nil {
		t.Fatal("spawn grant accepted as access token")
	}
	if _, err := s.NotebookIdentity(session.AccessToken, "notebook-spawn"); err == nil {
		t.Fatal("access token accepted as spawn grant")
	}
	if err := s.SetMember("u1", "tenant-a", platformauth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotebookIdentity(grant, "notebook-spawn"); err == nil {
		t.Fatal("stale role accepted")
	}
	if err := s.SetMember("u1", "tenant-a", platformauth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	if err := s.DisableTenant("tenant-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotebookIdentity(session.AccessToken, "controlplane"); err == nil {
		t.Fatal("disabled tenant accepted")
	}
}

func TestNotebookHubServiceTokenCannotBecomeUser(t *testing.T) {
	s := NewService("notebook-test-secret")
	token, _, err := s.IssueNotebookHubToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AuthenticateNotebookHub(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NotebookIdentity(token, "controlplane"); err == nil {
		t.Fatal("service became user")
	}
	wrong, err := platformauth.IssueAccessToken(s.secret, "other-service", "", platformauth.RoleViewer, "notebook-hub", time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AuthenticateNotebookHub(wrong); err == nil {
		t.Fatal("wrong service subject accepted")
	}
	if _, err := s.NotebookSpawnToken(&platformauth.Claims{Role: platformauth.RoleViewer}); err == nil {
		t.Fatal("viewer received spawn grant")
	}
}
