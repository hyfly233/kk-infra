package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformauth "kk-infra/lib/auth"
)

func TestControlplaneRejectsOldRoleAndRefreshesCurrentRole(t *testing.T) {
	ids := managementIdentity(t, "secret")
	s := NewServer(nil, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(ids)
	session, err := ids.Login("admin@example.test", "test-password", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	call := func(token string, want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/v1/tenants/tenant-a/members", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	call(session.AccessToken, 200)
	if err := ids.SetMember("admin", "tenant-a", platformauth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	call(session.AccessToken, 401)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", strings.NewReader(`{"refreshToken":"`+session.RefreshToken+`"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var result struct {
		Data struct {
			AccessToken string `json:"accessToken"`
			Role        string `json:"role"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Data.Role != "viewer" {
		t.Fatalf("refresh status=%d body=%s", w.Code, w.Body.String())
	}
	call(result.Data.AccessToken, 401)
}

func TestInternalIntrospectionRequiresAllowedServiceAndCurrentMember(t *testing.T) {
	ids := managementIdentity(t, "secret")
	s := NewServer(nil, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(ids)
	cp := httptest.NewServer(s.Handler())
	defer cp.Close()
	session, err := ids.Login("admin@example.test", "test-password", "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"gateway", "modelregistry", "pipeline"} {
		v := platformauth.NewUserVerifier(cp.URL, "secret", caller)
		if _, err := v.Verify(context.Background(), session.AccessToken); err != nil {
			t.Fatalf("%s: %v", caller, err)
		}
	}
	if _, err := platformauth.NewUserVerifier(cp.URL, "secret", "k8sadapter").Verify(context.Background(), session.AccessToken); err == nil {
		t.Fatal("unlisted caller accepted")
	}
	if err := ids.SetMember("admin", "tenant-a", platformauth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	for _, caller := range []string{"gateway", "modelregistry", "pipeline"} {
		if _, err := platformauth.NewUserVerifier(cp.URL, "secret", caller).Verify(context.Background(), session.AccessToken); err == nil {
			t.Fatalf("%s accepted old role", caller)
		}
	}
}
