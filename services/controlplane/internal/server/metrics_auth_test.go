package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/data"
	"kk-infra/services/controlplane/internal/identity"
)

func TestDeploymentMetricsRejectsOtherTenantBeforeQuery(t *testing.T) {
	r := data.NewMemoryDeploymentRepository()
	if err := r.Create(&domain.ModelDeployment{ID: "d1", TenantID: "tenant-a"}); err != nil {
		t.Fatal(err)
	}
	s := NewServer(biz.NewDeploymentUseCase(r, nil, nil), nil, nil, nil, r, slog.Default())
	s.SetIdentityService(identity.NewService("secret"))
	for _, tc := range []struct {
		tenant string
		role   platformauth.Role
		status int
	}{
		{"tenant-a", platformauth.RoleViewer, 200},
		{"tenant-b", platformauth.RoleTenantAdmin, 404},
		{"tenant-b", platformauth.RolePlatformAdmin, 200},
	} {
		token, _ := platformauth.IssueAccessToken("secret", "u", tc.tenant, tc.role, "controlplane", time.Now(), time.Minute)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/d1/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("%s/%s: %d %s", tc.tenant, tc.role, w.Code, w.Body.String())
		}
	}
}

func TestInternalGPUQueryRejectsUserAndWrongService(t *testing.T) {
	s := NewServer(nil, biz.NewResourceUseCase(newMockKube()), nil, nil, nil, slog.Default())
	s.SetIdentityService(identity.NewService("secret"))
	valid, _ := platformauth.IssueServiceToken("secret", "observability", "controlplane")
	wrong, _ := platformauth.IssueServiceToken("secret", "gateway", "controlplane")
	user, _ := platformauth.IssueAccessToken("secret", "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	for _, bearer := range []string{"", wrong, user, valid} {
		r := httptest.NewRequest("GET", "/internal/resources/gpus", nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		want := 401
		if bearer == valid {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("internal GPU query: %d %s", w.Code, w.Body.String())
		}
	}
}
