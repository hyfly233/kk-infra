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
