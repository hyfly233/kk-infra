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
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
	"kk-infra/services/controlplane/internal/identity"
)

func TestOrphanReservationInventoryRequiresAdminAndDoesNotRelease(t *testing.T) {
	const secret = "orphan-inventory-test"
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "deployment", TenantID: "tenant-a", ClusterID: "gpu-east", Status: domain.DeploymentStatusRunning}); err != nil {
		t.Fatal(err)
	}
	cs := mustClusterService(t)
	for _, id := range []string{"gpu-west", "gpu-east"} {
		if _, err := cs.Register(id, id, "https://k8s.test", "http://adapter:8082", "private-kubeconfig", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := cs.Report(id, "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
			t.Fatal(err)
		}
		for _, deployment := range []string{"deployment", "unrelated"} {
			if err := cs.ReserveCapacity(id, clusters.GPUReservation{DeploymentID: deployment, TenantID: "tenant-a", Namespace: "tenant-a", ModelVersionID: "v1", TemplateGeneration: 2, GPUType: "A100", GPUCount: 2}, domain.RuntimeVLLM); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := cs.Report("gpu-west", "unhealthy", nil); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(biz.NewDeploymentUseCase(repo, nil, newMockKube()), nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(identity.NewService(secret))
	srv.SetClusterService(cs)
	admin, _ := platformauth.IssueAccessToken(secret, "admin", "", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	viewer, _ := platformauth.IssueAccessToken(secret, "viewer", "tenant-a", platformauth.RoleViewer, "controlplane", time.Now(), time.Hour)
	call := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id+"/orphan-reservations", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}
	for _, token := range []string{"", "invalid", viewer} {
		if got := call("deployment", token); got.Code != http.StatusUnauthorized {
			t.Fatalf("non-admin accessed inventory: %d", got.Code)
		}
	}
	if got := call("missing", admin); got.Code != http.StatusNotFound {
		t.Fatalf("missing deployment: %d", got.Code)
	}
	got := call("deployment", admin)
	var body struct {
		Data struct {
			CurrentClusterID string                  `json:"currentClusterId"`
			Reservations     []orphanReservationView `json:"reservations"`
		} `json:"data"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if got.Code != http.StatusOK || body.Data.CurrentClusterID != "gpu-east" || len(body.Data.Reservations) != 1 || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bad inventory: %d %s", got.Code, got.Body.String())
	}
	item := body.Data.Reservations[0]
	if item.ClusterID != "gpu-west" || item.ClusterHealth != "unhealthy" || item.Reservation.DeploymentID != "deployment" || item.Reservation.TemplateGeneration != 2 {
		t.Fatalf("bad reservation: %+v", item)
	}
	if strings.Contains(got.Body.String(), "private-kubeconfig") || strings.Contains(got.Body.String(), "adapterUrl") {
		t.Fatal("inventory leaked cluster connection data")
	}
	for _, id := range []string{"gpu-west", "gpu-east"} {
		c, _ := cs.Get(id)
		if len(c.GPUReservations) != 2 {
			t.Fatal("read-only inventory released reservations")
		}
	}
	if err := cs.ReleaseCapacity("gpu-west", "deployment"); err != nil {
		t.Fatal(err)
	}
	got = call("deployment", admin)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"reservations":[]`) {
		t.Fatalf("empty inventory is not an array: %s", got.Body.String())
	}
}
