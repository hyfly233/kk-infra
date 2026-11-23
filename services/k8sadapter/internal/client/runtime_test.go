package client

import (
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestRuntimeDriversRenderRuntimeSpecificContainers(t *testing.T) {
	tests := []struct {
		runtime, name, image, health, arg string
	}{
		{domain.RuntimeVLLM, "vllm", "vllm/vllm-openai", "/health", "--model"},
		{domain.RuntimeTriton, "triton", "tritonserver", "/v2/health/ready", "--model-repository=/models/model"},
		{domain.RuntimeTensorRTLLM, "tensorrt-llm", "trtllm", "/v2/health/ready", "--backend-config=tensorrtllm"},
	}
	for _, tt := range tests {
		t.Run(tt.runtime, func(t *testing.T) {
			driver, err := runtimeDriver(tt.runtime)
			if err != nil {
				t.Fatal(err)
			}
			container := driver.Build("/models/model", []string{"--strict-model-config=false"})
			joined := strings.Join(container.Args, " ")
			if container.Name != tt.name || !strings.Contains(container.Image, tt.image) || container.HealthPath != tt.health || !strings.Contains(joined, tt.arg) || !strings.Contains(joined, "--strict-model-config=false") {
				t.Fatalf("runtime container mismatch: %+v", container)
			}
			if container.MetricsPath == "" || len(driver.MetricMap()) == 0 {
				t.Fatalf("runtime metrics missing: %+v", driver.MetricMap())
			}
		})
	}
}

func TestRenderTritonDeploymentUsesDriverProbeAndMetrics(t *testing.T) {
	spec := &DeploymentSpec{Name: "triton-demo", Namespace: "tenant-t1", Runtime: domain.RuntimeTriton, Replicas: 1, ModelPath: "/models/repository", Resource: domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 1024}, Labels: map[string]string{}}
	manifest, err := renderDeploymentManifests(spec, spec.Namespace, "", true, artifactStorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	container := manifest.Deployment.Spec.Template.Spec.Containers[0]
	if container.Name != "triton" || container.ReadinessProbe.HTTPGet.Path != "/v2/health/ready" || manifest.Deployment.Spec.Template.Metadata.Annotations["prometheus.io/path"] != "/metrics" {
		t.Fatalf("Triton manifest mismatch: %+v", manifest.Deployment)
	}
}

func TestRuntimeDriverRejectsUnknownRuntime(t *testing.T) {
	if _, err := runtimeDriver("unknown"); err == nil {
		t.Fatal("unknown runtime must be rejected")
	}
}
