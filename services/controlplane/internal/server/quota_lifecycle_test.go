package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
)

type clusterLifecycleKube struct {
	biz.ClusterDeploymentClient
	kube         *mockKubeClient
	deleteErr    error
	scaleEntered chan struct{}
	scaleResume  chan struct{}
	scaleErr     error
	scaleCalls   int
	updateCalls  int
	updateErr    error
}

type rejectingRebuildPlacement struct {
	*clusters.Service
}

func (s rejectingRebuildPlacement) ReserveCapacity(string, clusters.GPUReservation, string) error {
	return errors.New("capacity consumed after precheck")
}

func TestRejectedRebuildRestoresSourceAndCanRetry(t *testing.T) {
	s := mustClusterService(t)
	for _, id := range []string{"gpu-west", "gpu-east"} {
		if _, err := s.Register(id, id, "https://k8s.test", "http://adapter:8082", "private", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
			t.Fatal(err)
		}
		if err := s.Report(id, "healthy", []clusters.Capacity{{GPUType: "A100", Total: 4, Allocatable: 4}}); err != nil {
			t.Fatal(err)
		}
	}
	d := &domain.ModelDeployment{ID: "rebuild", Name: "rebuild", TenantID: "default", Namespace: "tenant-default", ClusterID: "gpu-west", ModelVersionID: "v1", Runtime: domain.RuntimeVLLM, Replicas: 1, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusFailed, Diagnostics: "目标集群不可用: offline"}
	if err := s.ReserveCapacity("gpu-west", clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, ModelVersionID: "v1", GPUType: "A100", GPUCount: 1}, d.Runtime); err != nil {
		t.Fatal(err)
	}
	if err := s.Report("gpu-west", "unhealthy", nil); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	kube := newMockKube()
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	audit := biz.NewAuditUseCase(data.NewMemoryAuditStore(), slog.Default())
	uc.SetAudit(audit)
	// No cluster adapter is configured: a rejected pre-submit attempt must not use it.
	uc.SetClusterPlacement(rejectingRebuildPlacement{s}, nil)
	for attempt := int64(1); attempt <= 2; attempt++ {
		if _, err := uc.RebuildDeployment(context.Background(), d.ID, "gpu-east", "admin", true); err == nil {
			t.Fatal("expected reservation rejection")
		}
		restored, _ := repo.Get(d.ID)
		if restored.ClusterID != "gpu-west" || restored.Generation != attempt || restored.Status != domain.DeploymentStatusFailed {
			t.Fatalf("attempt %d cannot safely retry: %+v", attempt, restored)
		}
	}
	old, _ := s.Get("gpu-west")
	target, _ := s.Get("gpu-east")
	if len(old.GPUReservations) != 1 || len(target.GPUReservations) != 0 {
		t.Fatalf("unexpected ledger: old=%+v target=%+v", old.GPUReservations, target.GPUReservations)
	}
	if kube.lastSpec != nil {
		t.Fatal("rejected rebuild submitted a workload")
	}
	entries, err := audit.List(10)
	if err != nil || len(entries) != 4 {
		t.Fatalf("missing claim/rejection audit: %+v %v", entries, err)
	}
	for i, entry := range entries {
		want := "deployment.cluster_rebuild"
		if i%2 == 1 {
			want += ".rejected"
		}
		if entry.Action != want || entry.Actor != "admin" || entry.Resource != d.ID {
			t.Fatalf("unexpected audit: %+v", entry)
		}
	}
}

func (c *clusterLifecycleKube) UpdateDeploymentForCluster(ctx context.Context, clusterID string, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	c.updateCalls++
	if c.updateErr != nil {
		return nil, c.updateErr
	}
	return c.kube.UpdateDeployment(ctx, spec)
}

func TestUpgradeReservesNewTemplateBeforeAdapterAndKeepsOld(t *testing.T) {
	for _, capacity := range []int32{2, 4} {
		t.Run(fmt.Sprint(capacity), func(t *testing.T) {
			s := mustClusterService(t)
			if _, err := s.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
				t.Fatal(err)
			}
			if err := s.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: capacity, Allocatable: capacity}}); err != nil {
				t.Fatal(err)
			}
			repo := data.NewMemoryDeploymentRepository()
			d := &domain.ModelDeployment{ID: "upgrade", Name: "upgrade", TenantID: "default", Namespace: "tenant-default", ClusterID: "gpu-west", ModelVersionID: "v1", Runtime: domain.RuntimeVLLM, Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
			if err := repo.Create(d); err != nil {
				t.Fatal(err)
			}
			if err := s.ReserveCapacity("gpu-west", clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, ModelVersionID: "v1", GPUType: "A100", GPUCount: 2}, d.Runtime); err != nil {
				t.Fatal(err)
			}
			kube := &clusterLifecycleKube{kube: newMockKube(), updateErr: errors.New("uncertain update response")}
			uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube.kube)
			uc.SetClusterPlacement(s, kube)
			if _, err := uc.UpgradeDeployment(context.Background(), d.ID, "v2"); err == nil {
				t.Fatal("expected capacity or adapter error")
			}
			c, _ := s.Get("gpu-west")
			want := 1
			if capacity == 4 {
				want = 2
			}
			if len(c.GPUReservations) != want || kube.updateCalls != want-1 || c.GPUReservations[0].ModelVersionID != "v1" || c.GPUReservations[0].GPUCount != 2 {
				t.Fatalf("reservations=%+v adapterCalls=%d", c.GPUReservations, kube.updateCalls)
			}
			if want == 2 && (c.GPUReservations[1].TemplateGeneration != 1 || c.GPUReservations[1].ModelVersionID != "v2") {
				t.Fatal("new version not bound to new template")
			}
		})
	}
}

func (c *clusterLifecycleKube) ScaleDeploymentForCluster(ctx context.Context, clusterID, name, namespace string, replicas int32) (*clients.K8sDeploymentResult, error) {
	c.scaleCalls++
	if c.scaleEntered != nil {
		close(c.scaleEntered)
		<-c.scaleResume
	}
	if c.scaleErr != nil {
		return nil, c.scaleErr
	}
	return c.kube.ScaleDeployment(ctx, name, namespace, replicas)
}

func TestRejectedGrowthRestoresQuotaWithoutCallingAdapter(t *testing.T) {
	clusterService := mustClusterService(t)
	if _, err := clusterService.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 4, Allocatable: 4}}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "reject", Name: "reject", TenantID: "default", Namespace: "tenant-default", ClusterID: "gpu-west", Runtime: domain.RuntimeVLLM, Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.ReserveCapacity("gpu-west", clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, GPUType: "A100", GPUCount: 2}, d.Runtime); err != nil {
		t.Fatal(err)
	}
	kube := &clusterLifecycleKube{kube: newMockKube()}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube.kube)
	uc.SetClusterPlacement(clusterService, kube)
	quota := data.NewMemoryQuotaStore()
	if err := quota.AddUsed("default", "A100", 2); err != nil {
		t.Fatal(err)
	}
	uc.SetQuota(biz.NewQuotaUseCase(quota, slog.Default()))
	if _, err := uc.ScaleDeployment(context.Background(), d.ID, 5); err == nil {
		t.Fatal("oversized growth accepted")
	}
	q, _ := quota.Get("default", "A100")
	c, _ := clusterService.Get("gpu-west")
	current, _ := repo.Get(d.ID)
	if kube.scaleCalls != 0 || q.Used != 2 || c.GPUReservations[0].GPUCount != 2 || current.Replicas != 2 {
		t.Fatalf("rejected growth leaked: calls=%d quota=%+v reservation=%+v deployment=%+v", kube.scaleCalls, q, c.GPUReservations, current)
	}
}

func TestScaleClaimsOperationAndRetainsUncertainReservation(t *testing.T) {
	clusterService := mustClusterService(t)
	if _, err := clusterService.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "scale-cluster", Name: "scale-cluster", TenantID: "default", Namespace: "tenant-default", ClusterID: "gpu-west", Runtime: domain.RuntimeVLLM, Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.ReserveCapacity("gpu-west", clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, GPUType: "A100", GPUCount: 2}, d.Runtime); err != nil {
		t.Fatal(err)
	}
	kube := &clusterLifecycleKube{kube: newMockKube(), scaleEntered: make(chan struct{}), scaleResume: make(chan struct{}), scaleErr: errors.New("uncertain adapter response")}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube.kube)
	uc.SetClusterPlacement(clusterService, kube)
	quotaStore := data.NewMemoryQuotaStore()
	if err := quotaStore.AddUsed("default", "A100", 2); err != nil {
		t.Fatal(err)
	}
	uc.SetQuota(biz.NewQuotaUseCase(quotaStore, slog.Default()))
	done := make(chan error, 1)
	go func() { _, err := uc.ScaleDeployment(context.Background(), d.ID, 5); done <- err }()
	select {
	case <-kube.scaleEntered:
	case <-time.After(3 * time.Second):
		t.Fatal("adapter scale not called")
	}
	uc.SyncFromK8s(context.Background(), d.ID)
	current, _ := repo.Get(d.ID)
	if current.Status != domain.DeploymentStatusScaling {
		t.Fatalf("reconcile ended active operation: %s", current.Status)
	}
	if _, err := uc.ScaleDeployment(context.Background(), d.ID, 6); err == nil {
		t.Fatal("parallel scale accepted")
	}
	if err := uc.DeleteDeployment(context.Background(), d.ID); err == nil {
		t.Fatal("delete raced active scale")
	}
	close(kube.scaleResume)
	if err := <-done; err == nil {
		t.Fatal("expected adapter failure")
	}
	c, _ := clusterService.Get("gpu-west")
	q, _ := quotaStore.Get("default", "A100")
	current, _ = repo.Get(d.ID)
	if c.GPUReservations[0].GPUCount != 5 || c.GPUReservations[0].TemplateGeneration != 0 || q.Used != 5 || current.Status != domain.DeploymentStatusFailed || current.Replicas != 5 {
		t.Fatalf("uncertain failure lost capacity: reservations=%+v quota=%+v deployment=%+v", c.GPUReservations, q, current)
	}
}

func (c *clusterLifecycleKube) CreateDeploymentForCluster(ctx context.Context, clusterID string, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	return c.kube.CreateDeployment(ctx, spec)
}
func (c *clusterLifecycleKube) GetDeploymentForCluster(ctx context.Context, clusterID, name, namespace string) (*clients.K8sDeploymentResult, error) {
	return c.kube.GetDeployment(ctx, name, namespace)
}
func (c *clusterLifecycleKube) DeleteDeploymentForCluster(ctx context.Context, clusterID, name, namespace string) error {
	if c.deleteErr != nil {
		return c.deleteErr
	}
	return c.kube.DeleteDeployment(ctx, name, namespace)
}

func TestClusterCreateReservesBeforeSubmitAndDeleteReleases(t *testing.T) {
	clusterService := mustClusterService(t)
	if _, err := clusterService.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{domain.RuntimeVLLM}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 2, Allocatable: 2}}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	kube := &clusterLifecycleKube{kube: newMockKube()}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube.kube)
	uc.SetClusterPlacement(clusterService, kube)
	created, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{IdempotencyKey: "reserved", Name: "reserved", ModelVersionID: "v1", Replicas: 2})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := clusterService.Get("gpu-west")
	if len(c.GPUReservations) != 1 || c.GPUReservations[0].DeploymentID != created.ID || c.GPUReservations[0].GPUCount != 2 {
		t.Fatalf("reservation missing before submit: %+v", c.GPUReservations)
	}
	if _, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{Name: "no-capacity", ModelVersionID: "v1", Replicas: 1}); err == nil {
		t.Fatal("second create oversold pending capacity")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, _ := repo.Get(created.ID)
		if current.Status == domain.DeploymentStatusRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("deployment did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	quotaStore := data.NewMemoryQuotaStore()
	if err := quotaStore.AddUsed("default", "A100", 2); err != nil {
		t.Fatal(err)
	}
	uc.SetQuota(biz.NewQuotaUseCase(quotaStore, slog.Default()))
	kube.deleteErr = errors.New("resources still terminating")
	if err := uc.DeleteDeployment(context.Background(), created.ID); err == nil {
		t.Fatal("expected deletion failure")
	}
	c, _ = clusterService.Get("gpu-west")
	if len(c.GPUReservations) != 1 {
		t.Fatal("failed delete released reservation")
	}
	quota, _ := quotaStore.Get("default", "A100")
	if quota.Used != 2 {
		t.Fatal("unconfirmed delete released tenant quota")
	}
	kube.deleteErr = nil
	uc.RetryDelete(context.Background(), created.ID)
	current, _ := repo.Get(created.ID)
	if current.Status != domain.DeploymentStatusDeleted {
		t.Fatalf("background deletion did not finish: %s", current.Status)
	}
	c, _ = clusterService.Get("gpu-west")
	if len(c.GPUReservations) != 0 {
		t.Fatal("successful deletion leaked reservation")
	}
	quota, _ = quotaStore.Get("default", "A100")
	if quota.Used != 0 {
		t.Fatal("confirmed deletion leaked tenant quota")
	}
}

type rejectingDeploymentRepository struct{ data.DeploymentRepository }

type interruptedDeleteRepository struct {
	data.DeploymentRepository
	interrupt bool
}

func (r *interruptedDeleteRepository) CompareStatus(id, from, to string, generation int64, at time.Time) error {
	if to == domain.DeploymentStatusDeleted && r.interrupt {
		r.interrupt = false
		return errors.New("controlplane interrupted before terminal state")
	}
	return r.DeploymentRepository.CompareStatus(id, from, to, generation, at)
}

func TestDeleteRetryAfterQuotaReleaseDoesNotReleaseTwice(t *testing.T) {
	repo := &interruptedDeleteRepository{DeploymentRepository: data.NewMemoryDeploymentRepository(), interrupt: true}
	d := &domain.ModelDeployment{ID: "delete-retry", Name: "delete-retry", TenantID: "default", Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	store := data.NewMemoryQuotaStore()
	if err := store.AddUsed("default", "A100", 5); err != nil {
		t.Fatal(err)
	}
	kube := newMockKube()
	kube.deploys[d.Name] = 2
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	uc.SetQuota(biz.NewQuotaUseCase(store, slog.Default()))
	uc.RetryDelete(context.Background(), d.ID)
	if kube.deploys[d.Name] != 2 {
		t.Fatal("background retry deleted a running deployment")
	}
	if err := uc.DeleteDeployment(context.Background(), d.ID); err == nil {
		t.Fatal("expected interrupted finalization")
	}
	current, _ := repo.Get(d.ID)
	quota, _ := store.Get("default", "A100")
	if current.Status != domain.DeploymentStatusDeleting || quota.Used != 3 {
		t.Fatalf("unexpected interrupted state: %+v quota=%+v", current, quota)
	}
	// A restarted use case recovers from repository state and the quota receipt.
	restarted := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	restarted.SetQuota(biz.NewQuotaUseCase(store, slog.Default()))
	restarted.RetryDelete(context.Background(), d.ID)
	restarted.RetryDelete(context.Background(), d.ID)
	current, _ = repo.Get(d.ID)
	quota, _ = store.Get("default", "A100")
	if current.Status != domain.DeploymentStatusDeleted || quota.Used != 3 {
		t.Fatalf("retry lost quota/state: %+v quota=%+v", current, quota)
	}
}

func (r rejectingDeploymentRepository) Create(*domain.ModelDeployment) error {
	return errors.New("write failed")
}

type rejectingScaleClient struct{ *mockKubeClient }

func (c rejectingScaleClient) ScaleDeployment(context.Context, string, string, int32) (*clients.K8sDeploymentResult, error) {
	return nil, errors.New("adapter unavailable")
}

func TestCreateWriteFailureReleasesQuota(t *testing.T) {
	store := data.NewMemoryQuotaStore()
	uc := biz.NewDeploymentUseCase(rejectingDeploymentRepository{data.NewMemoryDeploymentRepository()}, &mockModelClient{}, newMockKube())
	uc.SetQuota(biz.NewQuotaUseCase(store, slog.Default()))
	_, err := uc.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{Name: "failed-create", ModelVersionID: "v1", Replicas: 2})
	if err == nil {
		t.Fatal("expected repository error")
	}
	q, _ := store.Get("default", "A100")
	if q.Used != 0 {
		t.Fatalf("failed create leaked quota: %d", q.Used)
	}
}

func TestFailedScaleDownRetainsQuotaAndReplicas(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "scale", Name: "scale", TenantID: "default", Replicas: 4, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	store := data.NewMemoryQuotaStore()
	if err := store.AddUsed("default", "A100", 4); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, rejectingScaleClient{newMockKube()})
	uc.SetQuota(biz.NewQuotaUseCase(store, slog.Default()))
	if _, err := uc.ScaleDeployment(context.Background(), d.ID, 2); err == nil {
		t.Fatal("expected scale error")
	}
	q, _ := store.Get("default", "A100")
	current, _ := repo.Get(d.ID)
	if q.Used != 4 || current.Replicas != 4 {
		t.Fatalf("failed shrink: used=%d replicas=%d", q.Used, current.Replicas)
	}
}

func TestRepeatedDeleteDoesNotReleaseOtherDeploymentQuota(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	d := &domain.ModelDeployment{ID: "delete", Name: "delete", TenantID: "default", Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusRunning}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	store := data.NewMemoryQuotaStore()
	if err := store.AddUsed("default", "A100", 5); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, newMockKube())
	uc.SetQuota(biz.NewQuotaUseCase(store, slog.Default()))
	for range 2 {
		if err := uc.DeleteDeployment(context.Background(), d.ID); err != nil {
			t.Fatal(err)
		}
	}
	q, _ := store.Get("default", "A100")
	if q.Used != 3 {
		t.Fatalf("repeat delete released unrelated quota: %d", q.Used)
	}
}
