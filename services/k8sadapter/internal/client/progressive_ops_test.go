package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestProgressiveCRUDUsesRolloutOnly(t *testing.T) {
	rolloutExists := false
	paths := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.Contains(r.URL.Path, "/rollouts/qwen") && r.Method == http.MethodGet {
			if !rolloutExists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"metadata":{"labels":{"carrot.ai/deployment-id":"d1"}},"spec":{"replicas":1},"status":{"phase":"Healthy","readyReplicas":1,"availableReplicas":1}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/rollouts") && r.Method == http.MethodPost {
			rolloutExists = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		if strings.Contains(r.URL.Path, "/rollouts/qwen") && (r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			if r.Method == http.MethodDelete {
				rolloutExists = false
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.RawQuery, "labelSelector") {
				_, _ = w.Write([]byte(`{"items":[]}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost || r.Method == http.MethodDelete || r.Method == http.MethodPatch {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), progressiveEnabled: true, prometheusURL: "http://prometheus:9090", virtualGPUs: DefaultFakeNodes()}
	spec := &DeploymentSpec{DeploymentID: "d1", Name: "qwen", Namespace: "tenant-t1", Replicas: 1, Resource: domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 1024}, ModelPath: "/models/qwen", Labels: map[string]string{"carrot.ai/deployment-id": "d1", "carrot.ai/tenant-id": "t1"}}
	result, err := c.CreateDeployment(context.Background(), spec)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status.Condition != "Available" {
		t.Fatalf("unexpected rollout status: %+v", result.Status)
	}
	if result.RolloutStatus != "Healthy" || !strings.Contains(result.StableEndpoint, "qwen-stable") || !strings.Contains(result.CanaryEndpoint, "qwen-canary") {
		t.Fatalf("rollout routing metadata missing: %+v", result)
	}
	if _, err = c.UpdateDeployment(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ScaleDeployment(context.Background(), spec.Name, spec.Namespace, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RestartDeployment(context.Background(), spec.Name, spec.Namespace); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteDeployment(context.Background(), spec.Name, spec.Namespace); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(paths, "\n")
	for _, required := range []string{"POST /apis/argoproj.io/v1alpha1/namespaces/tenant-t1/rollouts", "PATCH /apis/argoproj.io/v1alpha1/namespaces/tenant-t1/rollouts/qwen", "POST /apis/networking.istio.io/v1beta1/namespaces/tenant-t1/virtualservices", "DELETE /apis/argoproj.io/v1alpha1/namespaces/tenant-t1/rollouts/qwen"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing %s\n%s", required, joined)
		}
	}
	if strings.Contains(joined, "POST /apis/apps/v1/namespaces/tenant-t1/deployments") || strings.Contains(joined, "PATCH /apis/apps/v1/namespaces/tenant-t1/deployments") {
		t.Fatalf("progressive mode touched Deployment API:\n%s", joined)
	}
}
