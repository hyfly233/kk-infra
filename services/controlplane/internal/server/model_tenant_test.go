package server

import (
	"context"
	"testing"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/data"
)

type tenantModelClient struct{ tenant string }

func (m *tenantModelClient) GetVersion(ctx context.Context, id string) (*domain.ModelVersion, error) {
	v, err := (&mockModelClient{}).GetVersion(ctx, id)
	v.TenantID = m.tenant
	return v, err
}

func TestDeploymentRejectsForeignAndUnownedVersionsBeforeMutation(t *testing.T) {
	for _, tenant := range []string{"tenant-b", ""} {
		repo := data.NewMemoryDeploymentRepository()
		models := &tenantModelClient{tenant: tenant}
		kube := newMockKube()
		uc := biz.NewDeploymentUseCase(repo, models, kube)
		uc.RequireModelTenant(true)
		if _, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{IdempotencyKey: "new", Name: "new", ModelVersionID: "v1", TenantID: "tenant-a", Replicas: 1}); err == nil {
			t.Fatalf("accepted foreign/unowned model: %q", tenant)
		}
		rows, err := repo.List("tenant-a")
		if err != nil || len(rows) != 0 || kube.lastSpec != nil {
			t.Fatalf("rejected create changed state: %d %v", len(rows), err)
		}
		d := &domain.ModelDeployment{ID: "d1", TenantID: "tenant-a", ModelVersionID: "v1", Status: domain.DeploymentStatusRunning, Replicas: 1}
		if err := repo.Create(d); err != nil {
			t.Fatal(err)
		}
		if _, err := uc.UpgradeDeployment(context.Background(), "d1", "v2"); err == nil {
			t.Fatalf("foreign/unowned upgrade accepted: %q", tenant)
		}
		stored, _ := repo.Get("d1")
		if stored.Status != domain.DeploymentStatusRunning || stored.ModelVersionID != "v1" || kube.lastSpec != nil {
			t.Fatal("rejected upgrade changed state")
		}
	}
}

func TestDeploymentIdempotencyCannotReturnAnotherTenantsRecord(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "secret-id", Name: "shared-name", TenantID: "tenant-a", Status: domain.DeploymentStatusRunning}); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewDeploymentUseCase(repo, nil, nil)
	if result, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{Name: "shared-name", TenantID: "tenant-b"}); err == nil || result != nil {
		t.Fatal("name idempotency leaked another tenant")
	}
	if result, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{Name: "shared-name", TenantID: "tenant-a"}); err != nil || result.ID != "secret-id" {
		t.Fatalf("same-tenant idempotency broken: %+v %v", result, err)
	}
}

func TestRebuildRejectsForeignVersionBeforeClaim(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "failed", TenantID: "tenant-a", ModelVersionID: "v1", ClusterID: "old", Generation: 3, Status: domain.DeploymentStatusFailed, Diagnostics: "目标集群不可用: old"}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewDeploymentUseCase(repo, &tenantModelClient{tenant: "tenant-b"}, newMockKube())
	uc.RequireModelTenant(true)
	if _, err := uc.RebuildDeployment(context.Background(), d.ID, "new", "admin", true); err == nil {
		t.Fatal("foreign version accepted for rebuild")
	}
	stored, _ := repo.Get(d.ID)
	if stored.ClusterID != "old" || stored.Generation != 3 || stored.Status != domain.DeploymentStatusFailed {
		t.Fatal("rejected rebuild changed placement or generation")
	}
}
