package server

import (
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/observability/internal/metrics"
	"kk-infra/services/observability/internal/usage"
)

func TestObservabilityRestrictsCallerByOperation(t *testing.T) {
	s := NewServer(metrics.NewStore(time.Hour), slog.Default())
	s.SetUsageStore(usage.NewMemoryStore())
	s.SetAuthSecret("secret")
	token := func(caller string) string {
		value, _ := platformauth.IssueServiceToken("secret", caller, "observability")
		return value
	}
	user, _ := platformauth.IssueAccessToken("secret", "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	wrongAudience, _ := platformauth.IssueServiceToken("secret", "controlplane", "gateway")
	for _, tc := range []struct {
		method, path, bearer, body string
		status                     int
	}{
		{"GET", "/internal/billing?tenantId=tenant-a", "", "", 401},
		{"GET", "/internal/billing?tenantId=tenant-a", user, "", 401},
		{"GET", "/internal/billing?tenantId=tenant-a", token("gateway"), "", 401},
		{"GET", "/internal/billing?tenantId=tenant-a", wrongAudience, "", 401},
		{"GET", "/internal/billing?tenantId=tenant-a", token("controlplane"), "", 200},
		{"GET", "/api/v1/deployments/d1/metrics", user, "", 401},
		{"GET", "/api/v1/deployments/d1/metrics", token("controlplane"), "", 200},
		{"POST", "/api/v1/metrics/requests", token("controlplane"), `{}`, 401},
		{"POST", "/api/v1/metrics/requests", token("gateway"), `{"tenantId":"tenant-a","deploymentId":"d1","model":"m","latencyMs":1}`, 200},
		{"POST", "/api/v1/metrics/gpu", token("gateway"), `{}`, 401},
		{"PUT", "/internal/rate-cards/tenant-a", token("gateway"), `{}`, 401},
		{"GET", "/metrics", "", "", 200},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.bearer)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
