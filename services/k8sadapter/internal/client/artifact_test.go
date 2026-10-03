package client

import (
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestRenderDeploymentArtifactInitContainer(t *testing.T) {
	spec := &DeploymentSpec{
		DeploymentID: "d1", Name: "qwen", Namespace: "tenant-t1", Replicas: 1,
		Resource: domain.Resource{GPUType: "A100", GPUCount: 1, MemoryMB: 1024},
		Labels:   map[string]string{"carrot.ai/deployment-id": "d1"}, ModelPath: "/models/qwen",
		ArtifactURI: "s3://models/qwen.tar", ArtifactDigest: "sha256:abcdef",
	}
	manifests, err := renderDeploymentManifests(spec, spec.Namespace, "vllm:test", true, artifactStorageConfig{
		Endpoint: "minio.storage.svc:9000", AccessKey: "access", SecretKey: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if manifests.Secret == nil || manifests.Secret.StringData["secret-key"] != "secret" {
		t.Fatalf("artifact Secret missing: %+v", manifests.Secret)
	}
	pod := manifests.Deployment.Spec.Template.Spec
	if len(pod.InitContainers) != 1 || len(pod.Volumes) != 1 {
		t.Fatalf("artifact init resources missing: %+v", pod)
	}
	init := pod.InitContainers[0]
	if len(init.Command) != 1 || init.Command[0] != "/bin/sh" || !strings.Contains(init.Args[1], "sha256sum -c") {
		t.Fatalf("checksum init command missing: %+v", init)
	}
	main := pod.Containers[0]
	if main.Args[1] != "/models/model" || len(main.VolumeMounts) != 1 || !main.VolumeMounts[0].ReadOnly {
		t.Fatalf("main container model mount invalid: %+v", main)
	}
	for _, env := range main.Env {
		if strings.Contains(env.Name, "S3") || strings.Contains(env.Value, "s3://") {
			t.Fatalf("artifact credentials leaked to main container: %+v", main.Env)
		}
	}
}

func TestRenderDeploymentRejectsUnverifiedArtifact(t *testing.T) {
	spec := &DeploymentSpec{Name: "qwen", Resource: domain.Resource{GPUCount: 1}, ArtifactURI: "s3://models/qwen.tar", ArtifactDigest: "etag:abc"}
	if _, err := renderDeploymentManifests(spec, "tenant-t1", "vllm:test", true, artifactStorageConfig{Endpoint: "minio:9000", AccessKey: "a", SecretKey: "s"}); err == nil {
		t.Fatal("non-SHA256 artifact must be rejected")
	}
}
