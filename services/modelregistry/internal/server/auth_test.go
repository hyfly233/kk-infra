package server

import (
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/services/modelregistry/internal/biz"
	"kk-infra/services/modelregistry/internal/data"
)

func TestModelRegistryEnforcesTenantOwnershipAndViewerRedaction(t *testing.T) {
	repo := data.NewMemoryRepository()
	registry := biz.NewRegistry(repo)
	model, err := registry.CreateModel(&apitypes.CreateModelRequest{Name: "owned", TenantID: "tenant-a"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := registry.CreateVersion(model.ID, &apitypes.CreateModelVersionRequest{Version: "v1", ArtifactURI: "s3://private/weights", GPUType: "A100", GPUCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateModel(&domain.Model{ID: "legacy", Name: "legacy"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(registry, slog.Default())
	s.SetAuthSecret("secret")
	token := func(tenant string, role platformauth.Role) string {
		value, _ := platformauth.IssueAccessToken("secret", "user", tenant, role, "controlplane", time.Now(), time.Minute)
		return value
	}
	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	dev, viewer, other, admin := token("tenant-a", platformauth.RoleDeveloper), token("tenant-a", platformauth.RoleViewer), token("tenant-b", platformauth.RoleTenantAdmin), token("tenant-a", platformauth.RolePlatformAdmin)
	for _, path := range []string{"/api/v1/models/" + model.ID, "/api/v1/models/" + model.ID + "/versions", "/api/v1/versions/" + version.ID} {
		if w := call("GET", path, other, ""); w.Code != 404 {
			t.Fatalf("cross-tenant read: %d %s", w.Code, w.Body.String())
		}
	}
	for _, tc := range []struct{ method, path, body string }{
		{"DELETE", "/api/v1/models/" + model.ID, ""},
		{"POST", "/api/v1/models/" + model.ID + "/versions", `{}`},
		{"POST", "/api/v1/versions/" + version.ID + "/validate", ""},
		{"DELETE", "/api/v1/models/" + model.ID + "/versions/v1", ""},
	} {
		if w := call(tc.method, tc.path, other, tc.body); w.Code != 404 {
			t.Fatalf("cross-tenant mutation: %d %s", w.Code, w.Body.String())
		}
	}
	if w := call("GET", "/api/v1/models/legacy", viewer, ""); w.Code != 404 {
		t.Fatal("legacy ownership exposed")
	}
	if w := call("GET", "/api/v1/models/legacy", admin, ""); w.Code != 200 {
		t.Fatal("admin cannot inspect quarantine")
	}
	if w := call("DELETE", "/api/v1/models/legacy", admin, ""); w.Code != 409 {
		t.Fatalf("quarantine mutation: %d", w.Code)
	}
	for _, path := range []string{"/api/v1/versions/" + version.ID, "/api/v1/models/" + model.ID + "/versions"} {
		w := call("GET", path, viewer, "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "s3://private") {
			t.Fatalf("viewer leak: %s", w.Body.String())
		}
	}
	w := call("GET", "/api/v1/versions/"+version.ID, dev, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "s3://private") {
		t.Fatal("redaction corrupted stored version")
	}
	w = call("POST", "/api/v1/models", dev, `{"name":"new","tenantId":"tenant-b"}`)
	var result struct {
		Data domain.Model `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil || w.Code != 200 || result.Data.TenantID != "tenant-a" {
		t.Fatalf("client chose foreign tenant: %s %v", w.Body.String(), err)
	}
	if w := call("POST", "/api/v1/models", viewer, `{"name":"viewer-model"}`); w.Code != 401 {
		t.Fatal("viewer mutation accepted")
	}
	w = call("GET", "/api/v1/models", other, "")
	if strings.Contains(w.Body.String(), model.ID) || strings.Contains(w.Body.String(), "legacy") {
		t.Fatalf("list leak: %s", w.Body.String())
	}
}

func TestModelRegistryServicePrivilegesAndReleaseGate(t *testing.T) {
	repo := data.NewMemoryRepository()
	registry := biz.NewRegistry(repo)
	model, _ := registry.CreateModel(&apitypes.CreateModelRequest{Name: "owned", TenantID: "tenant-a"})
	version, _ := registry.CreateVersion(model.ID, &apitypes.CreateModelVersionRequest{Version: "v1", ArtifactURI: "s3://private/weights", GPUType: "A100", GPUCount: 1})
	s := NewServer(registry, slog.Default())
	s.SetAuthSecret("secret")
	cp, _ := platformauth.IssueServiceToken("secret", "controlplane", "modelregistry")
	pipe, _ := platformauth.IssueServiceToken("secret", "pipeline", "modelregistry")
	wrong, _ := platformauth.IssueServiceToken("secret", "pipeline", "gateway")
	user, _ := platformauth.IssueAccessToken("secret", "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	call := func(method, path, token string) int {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-Pipeline-Token", "legacy-secret")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w.Code
	}
	path := "/api/v1/versions/" + version.ID
	for _, token := range []string{"", "forged", wrong} {
		if status := call("GET", path, token); status != 401 {
			t.Fatalf("bad identity: %d", status)
		}
	}
	if call("POST", path+"/release", user) != 401 || call("POST", path+"/release", cp) != 401 {
		t.Fatal("release bypassed pipeline")
	}
	if call("POST", "/api/v1/models", pipe) != 401 {
		t.Fatal("pipeline obtained model CRUD")
	}
	if call("GET", path, cp) != 200 {
		t.Fatal("controlplane read rejected")
	}
	if call("POST", path+"/release", pipe) != 409 {
		t.Fatal("release bypassed validation stage")
	}
	if call("POST", path+"/validate", pipe) != 200 || call("POST", path+"/validate", pipe) != 200 {
		t.Fatal("pipeline cannot recheck validated artifact")
	}
	if call("POST", path+"/validate", user) != 409 {
		t.Fatal("user obtained pipeline revalidation privilege")
	}
}
