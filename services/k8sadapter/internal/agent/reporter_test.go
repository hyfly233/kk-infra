package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kk-infra/lib/domain"
)

type source struct {
	nodes []domain.GPUResource
	err   error
}

func (s *source) ListGPUNodes(context.Context) ([]domain.GPUResource, error) { return s.nodes, s.err }

type queueSource struct {
	source
	queues   []domain.VolcanoQueueCapacity
	queueErr error
}

type attributedSource struct{ source }

func (s *attributedSource) ListGPUCapacitySnapshot(context.Context) ([]domain.GPUResource, []domain.DeploymentGPUObservation, error) {
	generation := int64(0)
	return s.nodes, []domain.DeploymentGPUObservation{{DeploymentID: "deploy-a", TenantID: "tenant-a", Namespace: "tenant-a", NodeName: "node-a", GPUType: "A100", GPUCount: 2, TemplateGeneration: &generation}}, s.err
}

func TestReporterForwardsAttributionAndClearsOnFailure(t *testing.T) {
	var got snapshot
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = snapshot{}
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"code":0,"data":{"accepted":true}}`)
	}))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &attributedSource{source{nodes: []domain.GPUResource{{GPUType: "A100", Total: 8, Allocatable: 8, Used: 2, Health: domain.GPUHealthHealthy}}}}
	r, err := New(s, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background()); err != nil || len(got.DeploymentGPU) != 1 || got.DeploymentGPU[0].GPUCount != 2 {
		t.Fatalf("attribution: %+v %v", got, err)
	}
	if got.DeploymentGPU[0].TemplateGeneration == nil || *got.DeploymentGPU[0].TemplateGeneration != 0 {
		t.Fatal("zero template generation omitted from heartbeat")
	}
	s.err = errors.New("Pod watch failed")
	if err := r.Report(context.Background()); err == nil || got.HealthStatus != "unhealthy" || len(got.DeploymentGPU) != 0 {
		t.Fatalf("failed report: %+v %v", got, err)
	}
}

func (s *queueSource) ListVolcanoQueues(context.Context) ([]domain.VolcanoQueueCapacity, error) {
	return s.queues, s.queueErr
}

func TestQueueCollectionFailureReplacesHealthyCapacity(t *testing.T) {
	var got snapshot
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"code":0,"data":{"accepted":true}}`)
	}))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("test-token"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &queueSource{source: source{nodes: []domain.GPUResource{{GPUType: "A100", Total: 8, Allocatable: 8, Health: domain.GPUHealthHealthy}}}, queues: []domain.VolcanoQueueCapacity{{Name: "tenant-a", State: "Open", Allocated: map[string]string{"nvidia.com/gpu": "2"}}}}
	r, err := New(s, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background()); err != nil || len(got.VolcanoQueues) != 1 {
		t.Fatalf("queue not reported: %+v %v", got, err)
	}
	s.queueErr = errors.New("Volcano API unavailable")
	got = snapshot{}
	if err := r.Report(context.Background()); err == nil || got.HealthStatus != "unhealthy" || len(got.VolcanoQueues) != 0 || len(got.GPUCapacity) != 0 {
		t.Fatalf("queue failure retained capacity: %+v %v", got, err)
	}
}

func TestReporterAggregatesRotatesTokenAndReplacesFailedSnapshot(t *testing.T) {
	var got snapshot
	var token string
	status := http.StatusOK
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/clusters/gpu-west/heartbeat" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		token = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, `{"code":0,"data":{"accepted":true}}`)
	}))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("first\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &source{nodes: []domain.GPUResource{
		{GPUType: "H100", Total: 8, Allocatable: 8, Used: 2, Health: domain.GPUHealthHealthy},
		{GPUType: "H100", Total: 8, Allocatable: 7, Used: 3, Health: domain.GPUHealthHealthy},
	}}
	r, err := New(s, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token != "Bearer first" || got.HealthStatus != "healthy" || len(got.GPUCapacity) != 1 || got.GPUCapacity[0].Used != 5 || got.GPUCapacity[0].Allocatable != 15 {
		t.Fatalf("unexpected heartbeat: %+v", got)
	}
	if err := os.WriteFile(tokenFile, []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	s.err = errors.New("API unavailable")
	if err := r.Report(context.Background()); err == nil {
		t.Fatal("expected collection error")
	}
	if token != "Bearer second" || got.HealthStatus != "unhealthy" || len(got.GPUCapacity) != 0 {
		t.Fatalf("failed snapshot retained capacity: %+v", got)
	}
	s.err = nil
	status = http.StatusUnauthorized
	if err := r.Report(context.Background()); err == nil {
		t.Fatal("ignored rejected credentials")
	}
}

func TestReporterDoesNotFollowRedirects(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			t.Error("heartbeat followed redirect")
		}
		http.Redirect(w, r, "/redirect", http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(&source{}, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background()); err == nil {
		t.Fatal("accepted redirect")
	}
}

func TestRunReportsImmediatelyAndStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	received := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"accepted":true}}`)
		received <- struct{}{}
	}))
	defer api.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := New(&source{}, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { r.Run(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); close(done) }()
	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatal("initial report not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reporter did not stop")
	}
}
