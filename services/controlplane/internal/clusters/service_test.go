package clusters

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"kk-infra/lib/domain"
)

func TestClusterCredentialsAreEncryptedAndOmittedFromAPIObject(t *testing.T) {
	repo := NewMemoryRepository()
	service, err := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := service.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter-west:8082", "apiVersion: v1\nsecret: private", map[string]string{"region": "west"}, []string{"vLLM", "vLLM"}, []string{"tenant-a"}, "https://{service}.{namespace}.west.example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := service.DecryptCredentials(cluster.ID); err != nil || !strings.Contains(got, "private") {
		t.Fatalf("decrypt credentials: %q, %v", got, err)
	}
	_, encrypted, err := repo.Get(cluster.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encrypted), "private") {
		t.Fatal("repository stored plaintext credentials")
	}
	encoded, err := json.Marshal(cluster)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "kubeconfig") || strings.Contains(string(encoded), "private") {
		t.Fatalf("API object leaked credentials: %s", encoded)
	}
	if len(cluster.SupportedRuntimes) != 1 {
		t.Fatalf("runtimes were not normalized: %+v", cluster.SupportedRuntimes)
	}
}

func TestClusterPolicyCredentialRotationAndDeletion(t *testing.T) {
	repo := NewMemoryRepository()
	service, err := NewService(repo, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter-west:8082", "old-secret", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdatePolicy("gpu-west", map[string]string{"region": "west"}, []string{"Triton", "Triton"}, []string{"tenant-a"}); err != nil {
		t.Fatal(err)
	}
	c, err := service.Get("gpu-west")
	if err != nil || c.Labels["region"] != "west" || len(c.SupportedRuntimes) != 1 || c.SupportedRuntimes[0] != "Triton" || len(c.AllowedTenants) != 1 {
		t.Fatalf("policy not persisted: %+v %v", c, err)
	}
	if err := service.RotateCredentials("gpu-west", "new-secret"); err != nil {
		t.Fatal(err)
	}
	if got, err := service.DecryptCredentials("gpu-west"); err != nil || got != "new-secret" {
		t.Fatalf("credentials not rotated: %q %v", got, err)
	}
	_, encrypted, err := repo.Get("gpu-west")
	if err != nil || strings.Contains(string(encrypted), "new-secret") {
		t.Fatal("plaintext credentials stored")
	}
	if err := service.Delete("gpu-west"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get("gpu-west"); err != ErrNotFound {
		t.Fatalf("deleted cluster remains: %v", err)
	}
}

func TestClusterReportUpdatesHealthAndRejectsInvalidCapacity(t *testing.T) {
	service, _ := NewService(NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	if _, err := service.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter-west:8082", "secret", nil, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.Report("gpu-west", "healthy", []Capacity{{GPUType: "H100", Total: 8, Allocatable: 7, Used: 2}}); err != nil {
		t.Fatal(err)
	}
	clusters, err := service.List()
	if err != nil || len(clusters) != 1 {
		t.Fatalf("list cluster: %+v, %v", clusters, err)
	}
	if clusters[0].HealthStatus != "healthy" || clusters[0].GPUCapacity[0].Allocatable != 7 || clusters[0].LastHeartbeat == nil || time.Since(*clusters[0].LastHeartbeat) > time.Second {
		t.Fatalf("report not persisted: %+v", clusters[0])
	}
	if err := service.Report("gpu-west", "healthy", []Capacity{{GPUType: "H100", Total: 1, Allocatable: 2}}); err == nil {
		t.Fatal("expected invalid capacity error")
	}
}

func TestClusterChecksRejectStaleCapacityAndUnsupportedPlacement(t *testing.T) {
	service, _ := NewService(NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	if _, err := service.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter-west:8082", "secret", nil, []string{"vLLM"}, []string{"tenant-a"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := service.Report("gpu-west", "healthy", []Capacity{{GPUType: "H100", Total: 4, Allocatable: 4, Used: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckCapacity("gpu-west", domain.Resource{GPUType: "H100", GPUCount: 3}); err != nil {
		t.Fatalf("expected exact available capacity: %v", err)
	}
	if err := service.CheckCapacity("gpu-west", domain.Resource{GPUType: "H100", GPUCount: 4}); err == nil {
		t.Fatal("accepted request above available capacity")
	}
	if err := service.CheckRuntime("gpu-west", "Triton", "tenant-a"); err == nil {
		t.Fatal("accepted unsupported runtime")
	}
	if err := service.CheckRuntime("gpu-west", "vLLM", "tenant-b"); err == nil {
		t.Fatal("accepted disallowed tenant")
	}
	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	if err := service.CheckCapacity("gpu-west", domain.Resource{GPUType: "H100", GPUCount: 1}); err == nil {
		t.Fatal("accepted stale capacity")
	}
	cluster, err := service.Get("gpu-west")
	if err != nil || cluster.HealthStatus != "stale" {
		t.Fatalf("stale cluster appears healthy: %+v %v", cluster, err)
	}
	list, err := service.List()
	if err != nil || len(list) != 1 || list[0].HealthStatus != "stale" {
		t.Fatalf("stale cluster list: %+v %v", list, err)
	}
}

func TestClusterRegistrationRequiresTLSAndEncryptionKey(t *testing.T) {
	if _, err := NewService(NewMemoryRepository(), []byte("short")); err == nil {
		t.Fatal("expected invalid AES key error")
	}
	service, _ := NewService(NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	if _, err := service.Register("bad-id", "test", "http://k8s.example.test", "http://adapter:8082", "secret", nil, nil, nil, ""); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
}

func TestServingRouteTemplateAndPlacement(t *testing.T) {
	s, _ := NewService(NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	for _, template := range []string{"https://edge.example.test/{service}", "https://user:password@{service}.{namespace}.example.test", "https://{service}.{namespace}.example.test?token=secret", "ftp://{service}.{namespace}.example.test", "http://{service}.{namespace}.example.test", "https://{service}.{unknown}.{namespace}.example.test"} {
		if err := s.SetServingURLTemplate("missing", template); err == nil {
			t.Fatalf("accepted unsafe template %q", template)
		}
	}
	if _, err := s.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter:8082", "secret", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Report("gpu-west", "healthy", []Capacity{{GPUType: "H100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectWithServingRoute(domain.Resource{GPUType: "H100", GPUCount: 1}, "vLLM", "tenant-a"); err == nil {
		t.Fatal("placed gateway workload without an external serving route")
	}
	if err := s.SetServingURLTemplate("gpu-west", "https://{service}.{namespace}.west.example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectWithServingRoute(domain.Resource{GPUType: "H100", GPUCount: 1}, "vLLM", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	endpoint, err := s.ResolveServingEndpoint("gpu-west", "qwen-stable", "tenant-a")
	if err != nil || endpoint != "https://qwen-stable.tenant-a.west.example.test" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	if _, err := s.ResolveServingEndpoint("gpu-west", "../../bad", "tenant-a"); err == nil {
		t.Fatal("accepted invalid service name")
	}
}

func TestPlacementUsesHealthRuntimeTenantAndGPUCapacity(t *testing.T) {
	service, _ := NewService(NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	now := time.Now().UTC()
	service.now = func() time.Time { return now }
	for _, item := range []struct {
		id          string
		allocatable int32
		tenants     []string
		runtime     string
	}{
		{"gpu-west", 8, []string{"tenant-a"}, "vLLM"},
		{"gpu-east", 4, nil, "vLLM"},
		{"gpu-old", 8, []string{"tenant-b"}, "Triton"},
	} {
		if _, err := service.Register(item.id, item.id, "https://"+item.id+".example.test", "http://"+item.id+":8082", "kubeconfig", nil, []string{item.runtime}, item.tenants, ""); err != nil {
			t.Fatal(err)
		}
		if err := service.Report(item.id, "healthy", []Capacity{{GPUType: "H100", Total: item.allocatable, Allocatable: item.allocatable, Used: 0}}); err != nil {
			t.Fatal(err)
		}
	}
	selected, err := service.Select(domain.Resource{GPUType: "H100", GPUCount: 3}, "vLLM", "tenant-a")
	if err != nil || selected.ID != "gpu-east" {
		t.Fatalf("best-fit placement = %q, %v", selected.ID, err)
	}
	if _, err := service.Select(domain.Resource{GPUType: "H100", GPUCount: 3}, "Triton", "tenant-a"); err == nil {
		t.Fatal("placement ignored tenant policy")
	}
	service.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, err := service.Select(domain.Resource{GPUType: "H100", GPUCount: 1}, "vLLM", "tenant-a"); err == nil {
		t.Fatal("placement accepted stale heartbeat")
	}
}
