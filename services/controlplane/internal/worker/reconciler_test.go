package worker

import (
	"context"
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
