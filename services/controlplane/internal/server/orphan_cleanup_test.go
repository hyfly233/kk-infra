package server

import (
	"bytes"
	"context"
	"errors"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type orphanCleanupKube struct {
	*clusterLifecycleKube
	calls int
	err   error
}

func (c *orphanCleanupKube) DeleteManagedDeploymentForCluster(_ context.Context, cluster, name, namespace, id string) error {
	if cluster != "gpu-west" || name != "model" || namespace != "tenant-a" || id != "d1" {
		return errors.New("incorrect resource identity")
	}
	c.calls++
	return c.err
}

func TestOrphanCleanupHTTPKeepsCurrentPlacementAndRetriesSafely(t *testing.T) {
	const secret = "orphan-cleanup-test"
	r := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "d1", Name: "model", Namespace: "tenant-a", TenantID: "tenant-a", ClusterID: "gpu-east", Status: domain.DeploymentStatusRunning}
	if err := r.Create(d); err != nil {
		t.Fatal(err)
	}
	cs := mustClusterService(t)
	for _, id := range []string{"gpu-west", "gpu-east"} {
		if _, err := cs.Register(id, id, "https://k8s.test", "http://adapter:8082", "secret", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := cs.Report(id, "healthy", []clusters.Capacity{{GPUType: "A100", Total: 4, Allocatable: 4}}); err != nil {
			t.Fatal(err)
		}
		if err := cs.ReserveCapacity(id, clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, GPUType: "A100", GPUCount: 1}, domain.RuntimeVLLM); err != nil {
			t.Fatal(err)
		}
	}
	kube := &orphanCleanupKube{clusterLifecycleKube: &clusterLifecycleKube{kube: newMockKube()}, err: errors.New("terminating Pod")}
	uc := biz.NewDeploymentUseCase(r, nil, kube.kube)
	uc.SetClusterPlacement(cs, kube)
	quota := data.NewMemoryQuotaStore()
	if err := quota.AddUsed("default", "A100", 3); err != nil {
		t.Fatal(err)
	}
	uc.SetQuota(biz.NewQuotaUseCase(quota, slog.Default()))
	audit := biz.NewAuditUseCase(data.NewMemoryAuditStore(), slog.Default())
	uc.SetAudit(audit)
	s := NewServer(uc, nil, nil, nil, nil, slog.Default())
	s.SetIdentityService(managementIdentity(t, secret))
	s.SetClusterService(cs)
	admin, _ := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	viewer, _ := platformauth.IssueAccessToken(secret, "viewer", "tenant-a", platformauth.RoleViewer, "controlplane", time.Now(), time.Hour)
	call := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/d1/orphan-cleanup", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		return res
	}
	body := `{"clusterId":"gpu-west","acknowledgeDelete":true}`
	if res := call(viewer, body); res.Code != http.StatusUnauthorized {
		t.Fatalf("viewer cleanup: %d", res.Code)
	}
	if res := call(admin, `{"clusterId":"gpu-west"}`); res.Code != http.StatusBadRequest {
		t.Fatalf("missing acknowledgement: %d", res.Code)
	}
	if res := call(admin, `{"clusterId":"gpu-east","acknowledgeDelete":true}`); res.Code == http.StatusOK {
		t.Fatal("cleaned current cluster")
	}
	if kube.calls != 0 {
		t.Fatal("invalid request called adapter")
	}
	if res := call(admin, body); res.Code == http.StatusOK {
		t.Fatal("unconfirmed delete succeeded")
	}
	old, _ := cs.Get("gpu-west")
	pin, running, _ := r.OrphanCleanupState(d.ID)
	if len(old.GPUReservations) != 1 || pin != "gpu-west" || running {
		t.Fatalf("failed cleanup lost pin/reservation: %+v %s %v", old, pin, running)
	}
	kube.err = nil
	if res := call(admin, body); res.Code != http.StatusOK {
		t.Fatalf("retry: %d %s", res.Code, res.Body.String())
	}
	if res := call(admin, body); res.Code != http.StatusOK || kube.calls != 2 {
		t.Fatal("repeat cleanup was not idempotent")
	}
	old, _ = cs.Get("gpu-west")
	current, _ := cs.Get("gpu-east")
	stored, _ := r.Get(d.ID)
	pin, running, _ = r.OrphanCleanupState(d.ID)
	q, _ := quota.Get("default", "A100")
	if len(old.GPUReservations) != 0 || len(current.GPUReservations) != 1 || stored.ClusterID != "gpu-east" || stored.Status != domain.DeploymentStatusRunning || q.Used != 3 || pin != "" || running {
		t.Fatal("cleanup changed current placement/quota or leaked pin")
	}
	entries, _ := audit.List(10)
	if len(entries) != 4 || entries[1].Action != "deployment.orphan.cleanup.failed" || entries[3].Action != "deployment.orphan.cleanup.completed" {
		t.Fatalf("missing cleanup audit: %+v", entries)
	}
}
