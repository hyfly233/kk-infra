package clusters

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"kk-infra/lib/domain"
)

func reservationService(t *testing.T) (*Service, *MemoryRepository) {
	t.Helper()
	repo := NewMemoryRepository()
	s, err := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Report("gpu-west", "healthy", []Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	return s, repo
}

func TestConcurrentClusterReservationsAndIdempotency(t *testing.T) {
	s, repo := reservationService(t)
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			request := GPUReservation{DeploymentID: fmt.Sprint(i), TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 1}
			if s.ReserveCapacity("gpu-west", request, "vLLM") == nil {
				accepted.Add(1)
				if err := s.ReserveCapacity("gpu-west", request, "vLLM"); err != nil {
					t.Errorf("idempotent reserve: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	c, _ := s.Get("gpu-west")
	if accepted.Load() != 8 || len(c.GPUReservations) != 8 || availableGPU(c, "A100") != 0 {
		t.Fatalf("accepted=%d reservations=%d available=%d", accepted.Load(), len(c.GPUReservations), availableGPU(c, "A100"))
	}
	// Recreating the service preserves its repository-backed ledger.
	restarted, err := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.CheckCapacity("gpu-west", domain.Resource{GPUType: "A100", GPUCount: 1}); err == nil {
		t.Fatal("restart lost reservations")
	}
	if err := restarted.Delete("gpu-west"); err == nil {
		t.Fatal("deleted reserved cluster")
	}
	for _, item := range c.GPUReservations {
		if err := restarted.ReleaseCapacity("gpu-west", item.DeploymentID); err != nil {
			t.Fatal(err)
		}
	}
	if err := restarted.CheckCapacity("gpu-west", domain.Resource{GPUType: "A100", GPUCount: 8}); err != nil {
		t.Fatal(err)
	}
}

func TestReservationOffsetsOnlyMatchingTemplateAndKeepsExternalUsage(t *testing.T) {
	s, _ := reservationService(t)
	request := GPUReservation{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 4}
	if err := s.ReserveCapacity("gpu-west", request, "vLLM"); err != nil {
		t.Fatal(err)
	}
	generation := int64(0)
	assigned := []domain.DeploymentGPUObservation{{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", NodeName: "node", GPUType: "A100", GPUCount: 2, TemplateGeneration: &generation}}
	capacity := []Capacity{{GPUType: "A100", Total: 8, Allocatable: 8, Used: 3}}
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get("gpu-west")
	if got := availableGPU(c, "A100"); got != 3 {
		t.Fatalf("double counted assigned Pods or ignored external usage: %d", got)
	}
	generation = 1
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get("gpu-west")
	if got := availableGPU(c, "A100"); got != 1 {
		t.Fatalf("wrong template offset reservation: %d", got)
	}
	if err := s.ReleaseCapacity("gpu-west", "deploy"); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get("gpu-west")
	if got := availableGPU(c, "A100"); got != 5 {
		t.Fatalf("release erased still-observed Pod usage: %d", got)
	}
}

func TestConcurrentGrowthKeepsTemplateAndCannotOversell(t *testing.T) {
	s, _ := reservationService(t)
	for _, id := range []string{"a", "b"} {
		if err := s.ReserveCapacity("gpu-west", GPUReservation{DeploymentID: id, TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 2}, "vLLM"); err != nil {
			t.Fatal(err)
		}
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.GrowCapacity("gpu-west", id, "A100", "", 6) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	c, _ := s.Get("gpu-west")
	if accepted.Load() != 1 || availableGPU(c, "A100") != 0 {
		t.Fatalf("growth accepted=%d available=%d", accepted.Load(), availableGPU(c, "A100"))
	}
	for _, item := range c.GPUReservations {
		if item.TemplateGeneration != 0 {
			t.Fatal("growth changed template generation")
		}
		if err := s.GrowCapacity("gpu-west", item.DeploymentID, "A100", "", 1); err != nil {
			t.Fatal(err)
		}
	}
	c, _ = s.Get("gpu-west")
	if availableGPU(c, "A100") != 0 {
		t.Fatal("growth API released reservation on shrink")
	}
	if err := s.GrowCapacity("gpu-west", "a", "H100", "", 3); err == nil {
		t.Fatal("growth reserved wrong GPU type")
	}
}

func TestGrowthCreditsAlreadyObservedSameTemplatePods(t *testing.T) {
	s, _ := reservationService(t)
	request := GPUReservation{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 2}
	if err := s.ReserveCapacity("gpu-west", request, "vLLM"); err != nil {
		t.Fatal(err)
	}
	generation := int64(0)
	assigned := []domain.DeploymentGPUObservation{{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", NodeName: "node", GPUType: "A100", GPUCount: 5, TemplateGeneration: &generation}}
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", []Capacity{{GPUType: "A100", Total: 8, Allocatable: 8, Used: 5}}, nil, nil, assigned); err != nil {
		t.Fatal(err)
	}
	if err := s.GrowCapacity("gpu-west", "deploy", "A100", "", 6); err != nil {
		t.Fatalf("same-template observed capacity not credited: %v", err)
	}
	c, _ := s.Get("gpu-west")
	if got := availableGPU(c, "A100"); got != 2 {
		t.Fatalf("available=%d, want 2", got)
	}
}

func TestRollbackGrowthCannotBorrowFailedVersionReservation(t *testing.T) {
	s, _ := reservationService(t)
	for _, request := range []GPUReservation{{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 2, ModelVersionID: "v1"}, {DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", GPUType: "A100", GPUCount: 5, ModelVersionID: "v2", TemplateGeneration: 1}} {
		if err := s.ReserveCapacity("gpu-west", request, "vLLM"); err != nil {
			t.Fatal(err)
		}
	}
	oldGeneration, newGeneration := int64(0), int64(1)
	assigned := []domain.DeploymentGPUObservation{{DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", NodeName: "node", GPUType: "A100", GPUCount: 2, TemplateGeneration: &oldGeneration}, {DeploymentID: "deploy", TenantID: "tenant", Namespace: "tenant", NodeName: "node", GPUType: "A100", GPUCount: 5, TemplateGeneration: &newGeneration}}
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", []Capacity{{GPUType: "A100", Total: 8, Allocatable: 8, Used: 7}}, nil, nil, assigned); err != nil {
		t.Fatal(err)
	}
	if err := s.GrowCapacity("gpu-west", "deploy", "A100", "v1", 5); err == nil {
		t.Fatal("failed v2 reservation borrowed for stable v1 growth")
	}
	if err := s.GrowCapacity("gpu-west", "deploy", "A100", "v1", 3); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Get("gpu-west")
	if c.GPUReservations[0].GPUCount != 3 || c.GPUReservations[1].GPUCount != 5 {
		t.Fatalf("wrong version resized: %+v", c.GPUReservations)
	}
}
