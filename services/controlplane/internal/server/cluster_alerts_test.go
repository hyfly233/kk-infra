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
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/identity"
)

func TestClusterMonitorTokenHasOnlyMetricsAuthority(t *testing.T) {
	const secret = "cluster-monitor-contract-secret"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(identity.NewService(secret))
	srv.SetClusterService(mustClusterService(t))
	if _, err := srv.clusters.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter:8082", "secret", nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := srv.clusters.ReportSnapshot("gpu-west", "healthy", nil, nil, &domain.ClusterTelemetry{Status: "unhealthy", CollectedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	admin, _ := platformauth.IssueAccessToken(secret, "admin", "", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	viewer, _ := platformauth.IssueAccessToken(secret, "viewer", "tenant-a", platformauth.RoleViewer, "controlplane", time.Now(), time.Hour)
	call := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}
	issuePath := "/api/v1/clusters/monitor-token"
	if res := call(http.MethodPost, issuePath, viewer); res.Code != http.StatusUnauthorized {
		t.Fatal("tenant viewer issued monitor token")
	}
	issued := call(http.MethodPost, issuePath, admin)
	var body struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if issued.Code != http.StatusOK || body.Data.Token == "" || issued.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("issuance failed: %d", issued.Code)
	}
	monitor := body.Data.Token
	expired, _ := platformauth.IssueAccessToken(secret, "cluster-monitor", "", platformauth.RoleViewer, "cluster-monitor", time.Now().Add(-25*time.Hour), 24*time.Hour)
	wrongSubject, _ := platformauth.IssueAccessToken(secret, "other-monitor", "", platformauth.RoleViewer, "cluster-monitor", time.Now(), time.Hour)
	if _, err := srv.identity.Authenticate(monitor); err == nil {
		t.Fatal("monitor authenticated as management user")
	}
	if _, err := srv.identity.AuthenticateClusterAgent(monitor, "gpu-west"); err == nil {
		t.Fatal("monitor authenticated as cluster agent")
	}
	for _, token := range []string{"", admin, viewer, "invalid", expired, wrongSubject} {
		if res := call(http.MethodGet, "/internal/clusters/metrics", token); res.Code != http.StatusUnauthorized {
			t.Fatalf("wrong audience accepted: %d", res.Code)
		}
	}
	metrics := call(http.MethodGet, "/internal/clusters/metrics", monitor)
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), `carrot_cluster_alert{cluster_id="gpu-west",kind="telemetry"} 1`) || !strings.HasPrefix(metrics.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("metrics failed: %d %s", metrics.Code, metrics.Body.String())
	}
	if res := call(http.MethodGet, "/api/v1/clusters/alerts", monitor); res.Code != http.StatusUnauthorized {
		t.Fatal("monitor accessed management alert API")
	}
	if res := call(http.MethodGet, "/api/v1/clusters/alerts", viewer); res.Code != http.StatusUnauthorized {
		t.Fatal("tenant viewer accessed cluster alerts")
	}
	if res := call(http.MethodGet, "/api/v1/clusters/alerts", admin); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "gpu-west/telemetry") {
		t.Fatalf("admin alert query failed: %s", res.Body.String())
	}
}
