package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/identity"
)

type notebookProvisioner struct{ tenant string }

func (p *notebookProvisioner) ProvisionTenant(_ context.Context, tenant string) error {
	p.tenant = tenant
	return nil
}

func TestNotebookHTTPStartDeleteContract(t *testing.T) {
	id := identity.NewService("notebook-lifecycle-test")
	if _, err := id.CreateUser("user-a", "a@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := id.SetMember("user-a", "tenant-a", platformauth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	session, err := id.Login("a@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	name := domain.NotebookUsername("tenant-a", "user-a")
	state := ""
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token hub-secret" {
			t.Error("missing Hub service credential")
		}
		if r.URL.Path != "/hub/api/users/"+name && r.URL.Path != "/hub/api/users/"+name+"/servers/workspace" {
			t.Errorf("unscoped owner path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			json.NewEncoder(w).Encode(map[string]any{"name": name, "servers": map[string]any{"workspace": map[string]any{"ready": state == "running", "pending": state}}})
		case http.MethodPost:
			var body struct {
				Token string `json:"platform_spawn_token"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			claims, err := id.NotebookIdentity(body.Token, "notebook-spawn")
			if err != nil || claims.Subject != "user-a" || claims.TenantID != "tenant-a" {
				t.Errorf("invalid spawn identity: %+v %v", claims, err)
			}
			state = "spawn"
			w.WriteHeader(202)
		case http.MethodDelete:
			state = "stop"
			w.WriteHeader(202)
		}
	}))
	defer hub.Close()
	client, err := clients.NewNotebookHubClient(hub.URL, "https://notebooks.example", "hub-secret")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(id)
	s.SetNotebookHub(client)
	p := &notebookProvisioner{}
	s.SetTenantProvisioner(p)
	call := func(method, want string) {
		r := httptest.NewRequest(method, "/api/v1/notebooks/workspace", bytes.NewBufferString(`{"tenantId":"tenant-b","ownerId":"user-b"}`))
		r.Header.Set("Authorization", "Bearer "+session.AccessToken)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		var result struct {
			Data clients.NotebookWorkspace `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Data.Status != want || result.Data.TenantID != "tenant-a" || result.Data.OwnerID != "user-a" {
			t.Fatalf("%s: %d %s, err=%v", method, w.Code, w.Body.String(), err)
		}
	}
	call("POST", "STARTING")
	if p.tenant != "tenant-a" {
		t.Fatalf("provisioned injected tenant %s", p.tenant)
	}
	state = "running"
	call("GET", "RUNNING")
	call("DELETE", "STOPPING")
}

func TestNotebookHTTPScopesOwnerAndRequiresServiceIdentity(t *testing.T) {
	id := identity.NewService("notebook-http-test")
	if _, err := id.CreateUser("user-a", "a@example.com", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	if err := id.SetMember("user-a", "tenant-a", platformauth.RoleDeveloper); err != nil {
		t.Fatal(err)
	}
	session, err := id.Login("a@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	name := domain.NotebookUsername("tenant-a", "user-a")
	hubCalls := 0
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubCalls++
		if r.URL.Path != "/hub/api/users/"+name {
			t.Errorf("wrong owner %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{"name": name, "servers": map[string]any{}})
	}))
	defer hub.Close()
	hubClient, err := clients.NewNotebookHubClient(hub.URL, "https://notebooks.example", "hub-secret")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(id)
	s.SetNotebookHub(hubClient)
	call := func(method, path, token, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/api/v1/notebooks/workspace?tenantId=tenant-b&ownerId=user-b", session.AccessToken, ""); w.Code != 200 {
		t.Fatalf("get=%d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/internal/notebooks/introspect", session.AccessToken, `{"token":"`+session.AccessToken+`"}`); w.Code != 401 {
		t.Fatalf("user accessed internal service: %d", w.Code)
	}
	serviceToken, _, err := id.IssueNotebookHubToken()
	if err != nil {
		t.Fatal(err)
	}
	w := call("POST", "/internal/notebooks/introspect", serviceToken, `{"token":"`+session.AccessToken+`"}`)
	var result struct {
		Active bool   `json:"active"`
		Tenant string `json:"tenantId"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || !result.Active || result.Tenant != "tenant-a" {
		t.Fatalf("introspection=%s err=%v", w.Body.String(), err)
	}
	if err := id.SetMember("user-a", "tenant-a", platformauth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/v1/notebooks/workspace", session.AccessToken, ""); w.Code != 401 {
		t.Fatalf("stale membership accepted: %d", w.Code)
	}
	viewer, err := id.Login("a@example.com", "correct horse battery staple", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/v1/notebooks/workspace", viewer.AccessToken, `{"tenantId":"tenant-b","ownerId":"user-b"}`); w.Code != 401 {
		t.Fatalf("viewer mutation accepted: %d", w.Code)
	}
	if hubCalls != 1 {
		t.Fatalf("unauthorized request reached Hub, calls=%d", hubCalls)
	}
}
