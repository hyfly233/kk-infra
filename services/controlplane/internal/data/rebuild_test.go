package data

import (
	"testing"
	"time"

	"kk-infra/lib/domain"
)

func TestClaimClusterRebuildIsAtomic(t *testing.T) {
	repo := NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "deployment-a", Name: "model-a", ClusterID: "gpu-west", Status: domain.DeploymentStatusFailed, Diagnostics: "目标集群不可用: heartbeat stale"}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ClusterID != "gpu-east" || claimed.Status != domain.DeploymentStatusSubmitting || claimed.Generation != 1 || claimed.Endpoint != "" {
		t.Fatalf("bad rebuild state: %+v", claimed)
	}
	if _, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", time.Now()); err != ErrConflict {
		t.Fatalf("duplicate rebuild accepted: %v", err)
	}
	if d.ClusterID != "gpu-west" {
		t.Fatal("claim mutated previously read deployment pointer")
	}
}
