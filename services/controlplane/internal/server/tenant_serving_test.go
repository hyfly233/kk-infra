package server

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/controlplane/internal/identity"
)

func TestTenantServingStatusRequiresGatewayAndReflectsDisable(t *testing.T) {
	ids := identity.NewService("secret")
	if _, err := ids.Bootstrap("admin", "admin@example.test", "Strong-password-123", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	s := NewServer(nil, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(ids)
	valid, _ := platformauth.IssueServiceToken("secret", "gateway", "controlplane")
	wrong, _ := platformauth.IssueServiceToken("secret", "observability", "controlplane")
	call := func(token, tenant string, want int, active string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/internal/tenants/"+tenant+"/serving-status", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != want || (active != "" && !strings.Contains(w.Body.String(), `"active":`+active)) {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	call("", "tenant-a", 401, "")
	call(wrong, "tenant-a", 401, "")
	call(valid, "missing", 200, "false")
	call(valid, "tenant-a", 200, "true")
	if err := ids.DisableTenant("tenant-a"); err != nil {
		t.Fatal(err)
	}
	call(valid, "tenant-a", 200, "false")
	if active, err := ids.TenantServingActive(context.Background(), ""); active || err != nil {
		t.Fatalf("empty tenant: %v %v", active, err)
	}
}
