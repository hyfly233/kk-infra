package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kk-infra/services/controlplane/internal/clusters"
)

func TestClusterAdapterPoolRoutesToAssignedAdapter(t *testing.T) {
	var gotName string
	var tenantProvisioned bool
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-a/provision" {
			tenantProvisioned = true
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
			return
		}
		if r.URL.Path != "/v1/deployments" {
			t.Errorf("unexpected adapter path: %s", r.URL.Path)
		}
		var body CreateDeploymentSpec
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		gotName = body.Name
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": K8sDeploymentResult{DeploymentID: body.DeploymentID, Name: body.Name, Endpoint: "inference.tenant.svc"}})
	}))
	defer adapter.Close()
	registry, _ := clusters.NewService(clusters.NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	if _, err := registry.Register("gpu-west", "west", "https://k8s-west.example.test", adapter.URL, "encrypted-at-rest", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	pool := NewClusterAdapterPool(registry, NewK8sAdapterClient("http://unused.invalid"))
	result, err := pool.CreateDeploymentForCluster(context.Background(), "gpu-west", &CreateDeploymentSpec{DeploymentID: "d1", Name: "model-a", Labels: map[string]string{"carrot.ai/tenant-id": "tenant-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "model-a" || gotName != "model-a" || !tenantProvisioned {
		t.Fatalf("wrong adapter result=%+v requestName=%q tenantProvisioned=%v", result, gotName, tenantProvisioned)
	}
	if _, err := pool.CreateDeploymentForCluster(context.Background(), "missing", &CreateDeploymentSpec{}); err == nil {
		t.Fatal("unknown cluster was routed")
	}
}
