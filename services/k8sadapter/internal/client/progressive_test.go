package client

import (
	"encoding/json"
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestRenderProgressiveManifests(t *testing.T) {
	spec := &DeploymentSpec{DeploymentID: "d1", Name: "qwen", Namespace: "tenant-t1", Replicas: 2, Resource: domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 1024}, ModelPath: "/models/qwen", Labels: map[string]string{"carrot.ai/tenant-id": "t1"}}
	base, err := renderDeploymentManifests(spec, spec.Namespace, "vllm:test", true, artifactStorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	resources, err := renderProgressiveManifests(spec, base, "http://prometheus:9090")
	if err != nil {
		t.Fatal(err)
	}
	if resources.StableService.Metadata.Name != "qwen-stable" || resources.CanaryService.Metadata.Name != "qwen-canary" {
		t.Fatalf("service names invalid: %+v", resources)
	}
	body, _ := json.Marshal(resources)
	text := string(body)
	for _, required := range []string{"argoproj.io/v1alpha1", "networking.istio.io/v1beta1", "qwen-traffic", "setWeight", "error-rate", "ttft", "throughput", "failureLimit", "failureCondition", "inconclusiveLimit", "max-error-rate", "max-ttft-ms", "min-throughput"} {
		if !strings.Contains(text, required) {
			t.Errorf("missing %s in %s", required, text)
		}
	}
	if strings.Contains(text, "carrot_inference_queue_length") || strings.Contains(text, "kv_cache") {
		t.Fatal("queue/KV metrics must not drive rollout promotion")
	}
	rolloutSpec := resources.Rollout["spec"].(map[string]any)
	canary := rolloutSpec["strategy"].(map[string]any)["canary"].(map[string]any)
	steps := canary["steps"].([]map[string]any)
	if steps[len(steps)-2]["setWeight"] != 100 {
		t.Fatalf("final canary weight missing: %+v", steps)
	}
	if _, ok := steps[len(steps)-1]["analysis"]; !ok {
		t.Fatalf("100%% traffic must pass final analysis before promotion: %+v", steps)
	}
}

func TestRenderProgressiveRequiresPrometheus(t *testing.T) {
	spec := &DeploymentSpec{Name: "qwen", Resource: domain.Resource{GPUCount: 1}, Labels: map[string]string{}}
	base, _ := renderDeploymentManifests(spec, "tenant-t1", "vllm:test", true, artifactStorageConfig{})
	if _, err := renderProgressiveManifests(spec, base, ""); err == nil {
		t.Fatal("missing prometheus URL should fail")
	}
}
