package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/gateway/internal/auth"
	"kk-infra/services/gateway/internal/router"
)

func TestKeyManagementTenantIsolationAndImmutableRotation(t *testing.T) {
	keys := auth.NewManager()
	owned, _ := keys.Issue("tenant-a")
	other, _ := keys.Issue("tenant-b")
	s := NewServer(keys, router.NewTable(), nil, slog.Default())
	s.SetAuthSecret("secret")
	token := func(role platformauth.Role) string {
		value, err := platformauth.IssueAccessToken("secret", "admin", "tenant-a", role, "controlplane", time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	admin := token(platformauth.RoleTenantAdmin)
	if w := call("POST", "/api/v1/keys", admin, `{"tenantId":`); w.Code != 400 {
		t.Fatalf("malformed create: %d", w.Code)
	}
	for _, bearer := range []string{"", "forged", token(platformauth.RoleViewer), token(platformauth.RoleDeveloper)} {
		if w := call("GET", "/api/v1/keys", bearer, ""); w.Code != 401 {
			t.Fatalf("unauthorized list: %d", w.Code)
		}
	}
	w := call("GET", "/api/v1/keys", admin, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), other.KeyID) || strings.Contains(w.Body.String(), "keyHash") {
		t.Fatalf("list leak: %s", w.Body.String())
	}
	for _, suffix := range []string{"disable", "rotate", "models"} {
		if w := call("POST", "/api/v1/keys/"+other.KeyID+"/"+suffix, admin, `{"models":[]}`); w.Code != 404 {
			t.Fatalf("cross-tenant %s: %d", suffix, w.Code)
		}
	}
	if w := call("POST", "/api/v1/keys", admin, `{"tenantId":"tenant-b"}`); w.Code != 401 {
		t.Fatalf("cross-tenant create: %d", w.Code)
	}
	if w := call("POST", "/api/v1/keys/"+owned.KeyID+"/rotate?tenant=tenant-b", admin, ""); w.Code != 400 {
		t.Fatalf("ownership-changing rotation: %d", w.Code)
	}
	if _, err := keys.Authenticate(owned.Key); err != nil {
		t.Fatal("rejected rotation disabled original key")
	}
	w = call("POST", "/api/v1/keys/"+owned.KeyID+"/rotate", admin, "")
	var result struct {
		Data auth.IssueResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Data.Tenant != "tenant-a" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("rotate=%s err=%v", w.Body.String(), err)
	}
	if _, err := keys.Authenticate(owned.Key); err == nil {
		t.Fatal("old key remains active")
	}
	if tenant, err := keys.Authenticate(result.Data.Key); err != nil || tenant != "tenant-a" {
		t.Fatalf("new owner %s %v", tenant, err)
	}
}

func TestInternalRoutesRequireControlplaneServiceJWT(t *testing.T) {
	s := NewServer(auth.NewManager(), router.NewTable(), nil, slog.Default())
	s.SetAuthSecret("secret")
	valid, _ := platformauth.IssueServiceToken("secret", "controlplane", "gateway")
	wrongAudience, _ := platformauth.IssueServiceToken("secret", "controlplane", "observability")
	wrongCaller, _ := platformauth.IssueServiceToken("secret", "pipeline", "gateway")
	user, _ := platformauth.IssueAccessToken("secret", "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	for _, bearer := range []string{"", wrongAudience, wrongCaller, user} {
		r := httptest.NewRequest("POST", "/internal/routes", strings.NewReader(`{"model":"m","endpoint":"http://backend","tenantId":"tenant-a"}`))
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("internal write accepted %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/internal/routes", strings.NewReader(`{"model":"m","endpoint":"http://backend","tenantId":"tenant-a"}`))
	r.Header.Set("Authorization", "Bearer "+valid)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("service write rejected: %s", w.Body.String())
	}
	// The same service identity must not enter the user key-management surface.
	r = httptest.NewRequest("GET", "/api/v1/keys", nil)
	r.Header.Set("Authorization", "Bearer "+valid)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("service became administrator")
	}
}
