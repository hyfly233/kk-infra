package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/data"
)

type countingKube struct {
	biz.K8sClient
	gets int
}

type pendingDeleteKube struct {
	biz.K8sClient
	pending bool
	calls   int
}

func (c *pendingDeleteKube) DeleteDeployment(context.Context, string, string) error {
	c.calls++
	if c.pending {
		return errors.New("Pod still terminating")
	}
	return nil
}

func TestReconcilerRetriesPendingDeleteAndReleasesQuotaOnce(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "pending", Name: "pending", TenantID: "default", Status: domain.DeploymentStatusDeleting, Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, UpdatedAt: time.Now().Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	quota := data.NewMemoryQuotaStore()
	if err := quota.AddUsed("default", "A100", 5); err != nil {
		t.Fatal(err)
	}
	kube := &pendingDeleteKube{pending: true}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	uc := biz.NewDeploymentUseCase(repo, nil, kube)
	uc.SetQuota(biz.NewQuotaUseCase(quota, logger))
	r := NewReconciler(repo, uc, logger, time.Second)
	r.reconcileAll(context.Background())
	d, _ := repo.Get("pending")
	q, _ := quota.Get("default", "A100")
	if d.Status != domain.DeploymentStatusDeleting || q.Used != 5 || kube.calls != 1 {
		t.Fatalf("pending delete lost intent/quota: %+v %+v calls=%d", d, q, kube.calls)
	}
	kube.pending = false
	r.reconcileAll(context.Background())
	r.reconcileAll(context.Background())
	d, _ = repo.Get("pending")
	q, _ = quota.Get("default", "A100")
	if d.Status != domain.DeploymentStatusDeleted || q.Used != 3 || kube.calls != 2 {
		t.Fatalf("delete retry did not converge: %+v %+v calls=%d", d, q, kube.calls)
	}
}

type orphanKube struct {
	biz.K8sClient
	items   []*clients.K8sDeploymentResult
	scanned string
}

func (c *orphanKube) ListDeployments(_ context.Context, namespace string) ([]*clients.K8sDeploymentResult, error) {
	c.scanned = namespace
	return c.items, nil
}

func TestOrphanScanUsesFullIdentityAndAuditsOnce(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "known", Name: "shared", Namespace: "tenant-a", Status: domain.DeploymentStatusRunning}); err != nil {
		t.Fatal(err)
	}
	kube := &orphanKube{items: []*clients.K8sDeploymentResult{{Name: "shared", Namespace: "tenant-a"}, {Name: "shared", Namespace: "tenant-b"}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audit := data.NewMemoryAuditStore()
	uc := biz.NewDeploymentUseCase(repo, nil, kube)
	uc.SetAudit(biz.NewAuditUseCase(audit, logger))
	r := NewReconciler(repo, uc, logger, time.Second)
	r.reconcileOrphans(context.Background())
	r.reconcileOrphans(context.Background())
	entries, _ := audit.List(100)
	if kube.scanned != "*" || len(entries) != 1 || entries[0].Action != "deployment.orphan.detected" || entries[0].Resource != "/tenant-b/shared" {
		t.Fatalf("scan=%s audit=%+v", kube.scanned, entries)
	}
	kube.items = nil
	r.reconcileOrphans(context.Background())
	kube.items = []*clients.K8sDeploymentResult{{Name: "shared", Namespace: "tenant-b"}}
	r.reconcileOrphans(context.Background())
	entries, _ = audit.List(100)
	if len(entries) != 2 {
		t.Fatal("reappearing orphan was not recorded")
	}
}

func (c *countingKube) GetDeployment(context.Context, string, string) (*clients.K8sDeploymentResult, error) {
	c.gets++
	return &clients.K8sDeploymentResult{
		Endpoint: "model.tenant-a.svc.cluster.local",
		Status:   &clients.K8sDeploymentStatus{Condition: "Available", Replicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
	}, nil
}

func TestReconcilerChecksRunningDeployment(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "d1", Name: "model", Namespace: "tenant-a", Status: domain.DeploymentStatusRunning, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	kube := &countingKube{}
	useCase := biz.NewDeploymentUseCase(repo, nil, kube)
	r := NewReconciler(repo, useCase, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	r.reconcileAll(context.Background())
	if kube.gets != 1 {
		t.Fatalf("running deployment skipped: get count=%d", kube.gets)
	}
	stored, err := repo.Get("d1")
	if err != nil || stored.Endpoint != "http://model.tenant-a.svc.cluster.local" {
		t.Fatalf("running deployment not synchronized: %+v %v", stored, err)
	}
}

func TestInterruptedScaleFailsWithoutAssumingResourcesReleased(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "interrupted", Name: "model", Status: domain.DeploymentStatusScaling, Replicas: 5, UpdatedAt: time.Now().Add(-10 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	kube := &countingKube{}
	uc := biz.NewDeploymentUseCase(repo, nil, kube)
	r := NewReconciler(repo, uc, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Second)
	r.reconcileAll(context.Background())
	d, _ := repo.Get("interrupted")
	if d.Status != domain.DeploymentStatusFailed || d.Replicas != 5 || kube.gets != 0 {
		t.Fatalf("interrupted operation: %+v gets=%d", d, kube.gets)
	}
}
