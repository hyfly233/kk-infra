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
	claimed, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", d.Generation, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ClusterID != "gpu-east" || claimed.Status != domain.DeploymentStatusSubmitting || claimed.Generation != 1 || claimed.Endpoint != "" {
		t.Fatalf("bad rebuild state: %+v", claimed)
	}
	if _, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", d.Generation, time.Now()); err != ErrConflict {
		t.Fatalf("duplicate rebuild accepted: %v", err)
	}
	if d.ClusterID != "gpu-west" {
		t.Fatal("claim mutated previously read deployment pointer")
	}
}

func TestAbortClusterRebuildRestoresSourceAndRejectsStaleAttempt(t *testing.T) {
	repo := NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "rebuild", ClusterID: "gpu-west", Status: domain.DeploymentStatusFailed, Diagnostics: "目标集群不可用: offline"}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", 0, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AbortClusterRebuild(d.ID, "gpu-east", "gpu-west", first.Generation, d.Diagnostics, time.Now()); err != nil {
		t.Fatal(err)
	}
	restored, _ := repo.Get(d.ID)
	if restored.ClusterID != "gpu-west" || restored.Generation != 1 || restored.Status != domain.DeploymentStatusFailed {
		t.Fatalf("bad restored deployment: %+v", restored)
	}
	second, err := repo.ClaimClusterRebuild(d.ID, "gpu-west", "gpu-east", 1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AbortClusterRebuild(d.ID, "gpu-east", "gpu-west", first.Generation, d.Diagnostics, time.Now()); err != ErrConflict {
		t.Fatalf("stale abort accepted: %v", err)
	}
	current, _ := repo.Get(d.ID)
	if current.ClusterID != second.ClusterID || current.Generation != second.Generation || current.Status != domain.DeploymentStatusSubmitting {
		t.Fatalf("stale abort overwrote new attempt: %+v", current)
	}
}
