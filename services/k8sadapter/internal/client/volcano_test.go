package client

import (
	"testing"

	"kk-infra/lib/domain"
)

func TestRenderVolcanoPodGroupAndScheduler(t *testing.T) {
	spec := &DeploymentSpec{
		DeploymentID: "d1", Name: "qwen", Namespace: "tenant-t1", Runtime: domain.RuntimeVLLM, Replicas: 2,
		Resource: domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 1024}, ModelPath: "/models/qwen",
		Labels: map[string]string{"carrot.ai/tenant-id": "t1"},
	}
	manifest, err := renderDeploymentManifests(spec, spec.Namespace, "", true, artifactStorageConfig{}, volcanoConfig{Enabled: true, QueuePrefix: "tenant-"})
	if err != nil {
		t.Fatal(err)
	}
	pod := manifest.Deployment.Spec.Template
	if pod.Spec.SchedulerName != "volcano" || pod.Metadata.Annotations["scheduling.volcano.sh/group-name"] != spec.Name {
		t.Fatalf("Volcano pod scheduling fields missing: %+v", pod)
	}
	if manifest.PodGroup == nil {
		t.Fatal("Volcano PodGroup missing")
	}
	pgSpec := manifest.PodGroup["spec"].(map[string]any)
	if pgSpec["queue"] != "tenant-t1" || pgSpec["minMember"] != int32(2) {
		t.Fatalf("Volcano PodGroup mismatch: %+v", manifest.PodGroup)
	}
}

func TestRenderWithoutVolcanoKeepsDefaultScheduler(t *testing.T) {
	spec := &DeploymentSpec{Name: "qwen", Namespace: "tenant-t1", Replicas: 1, Resource: domain.Resource{GPUCount: 1}, ModelPath: "/models/qwen", Labels: map[string]string{}}
	manifest, err := renderDeploymentManifests(spec, spec.Namespace, "", true, artifactStorageConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.PodGroup != nil || manifest.Deployment.Spec.Template.Spec.SchedulerName != "" {
		t.Fatalf("Volcano must be feature-gated: %+v", manifest)
	}
}
