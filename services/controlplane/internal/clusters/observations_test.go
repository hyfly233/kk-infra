package clusters

import (
	"testing"

	"kk-infra/lib/domain"
)

func TestAttributedSnapshotValidationReplacementAndIsolation(t *testing.T) {
	repo := NewMemoryRepository()
	s, err := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register("gpu-west", "west", "https://k8s.test", "http://adapter:8082", "private", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	capacity := []Capacity{{GPUType: "A100", Total: 8, Allocatable: 8, Used: 3}}
	generation := int64(0)
	assigned := []domain.DeploymentGPUObservation{{DeploymentID: "deploy-a", TenantID: "tenant-a", Namespace: "tenant-a", NodeName: "node-a", GPUType: "A100", GPUCount: 2}}
	assigned[0].TemplateGeneration = &generation
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err != nil {
		t.Fatal(err)
	}
	assigned[0].GPUCount = 9
	generation = 10
	c, _ := s.Get("gpu-west")
	if len(c.DeploymentGPU) != 1 || c.DeploymentGPU[0].GPUCount != 2 {
		t.Fatalf("report alias: %+v", c)
	}
	if c.DeploymentGPU[0].TemplateGeneration == nil || *c.DeploymentGPU[0].TemplateGeneration != 0 {
		t.Fatal("generation input alias")
	}
	*c.DeploymentGPU[0].TemplateGeneration = 11
	c.DeploymentGPU[0].GPUCount = 8
	c, _ = s.Get("gpu-west")
	if c.DeploymentGPU[0].GPUCount != 2 {
		t.Fatal("query alias")
	}
	if *c.DeploymentGPU[0].TemplateGeneration != 0 {
		t.Fatal("generation query alias")
	}
	generation = -1
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err == nil {
		t.Fatal("negative generation accepted")
	}
	generation = 0
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err == nil {
		t.Fatal("overcount accepted")
	}
	assigned[0].GPUCount = 2
	assigned[0].TenantID = ""
	if err := s.ReportAttributedSnapshot("gpu-west", "healthy", capacity, nil, nil, assigned); err == nil {
		t.Fatal("missing tenant accepted")
	}
	if err := s.ReportSnapshot("gpu-west", "healthy", capacity, nil, nil); err != nil {
		t.Fatal(err)
	}
	c, _ = s.Get("gpu-west")
	if len(c.DeploymentGPU) != 0 {
		t.Fatal("legacy heartbeat retained old attribution")
	}
}
