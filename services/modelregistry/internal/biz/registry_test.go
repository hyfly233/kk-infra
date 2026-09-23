package biz

import (
	"context"
	"errors"
	"testing"

	"kk-infra/lib/apitypes"
	"kk-infra/services/modelregistry/internal/artifact"
	"kk-infra/services/modelregistry/internal/data"
)

type failingVerifier struct{}

func (failingVerifier) Verify(context.Context, string, string) (*artifact.Metadata, error) {
	return nil, errors.New("checksum mismatch")
}

func TestValidateVersionDoesNotAdvanceOnArtifactFailure(t *testing.T) {
	repo := data.NewMemoryRepository()
	registry := NewRegistry(repo, failingVerifier{})
	model, err := registry.CreateModel(&apitypes.CreateModelRequest{Name: "qwen"})
	if err != nil {
		t.Fatal(err)
	}
	version, err := registry.CreateVersion(model.ID, &apitypes.CreateModelVersionRequest{
		Version: "1", ArtifactURI: "s3://models/qwen.bin", GPUType: "A100", GPUCount: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ValidateVersion(context.Background(), version.ID); err == nil {
		t.Fatal("artifact failure must reject validation")
	}
	stored, err := repo.GetVersion(version.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != "REGISTERED" || stored.ArtifactVerifiedAt != nil {
		t.Fatalf("failed validation mutated version: %+v", stored)
	}
}
