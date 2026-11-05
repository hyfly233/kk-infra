package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kk-infra/services/pipeline/internal/data"
)

func TestReleaseRequiresApprovalAndPersistsStages(t *testing.T) {
	released := false
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/release") {
			if r.Header.Get("X-Pipeline-Token") != "secret" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			released = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer registry.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeBenchmarkSSE(w, 0)
	}))
	defer probe.Close()

	store := data.NewMemoryStore()
	audit := &data.MemoryAuditStore{}
	svc := New(store, registry.URL, probe.URL, "secret", audit)
	record, err := svc.Start(context.Background(), "v1", "developer")
	if err != nil {
		t.Fatal(err)
	}
	if released || record.Status != "PENDING_APPROVAL" || len(record.StageResults) != 4 {
		t.Fatalf("release bypassed approval: %+v released=%v", record, released)
	}
	record, err = svc.Approve(context.Background(), record.ID, "tenant-admin", "looks good", true)
	if err != nil {
		t.Fatal(err)
	}
	if !released || record.Status != "RELEASED" || record.ReleasedAt == nil || len(record.StageResults) != 5 {
		t.Fatalf("approval did not release: %+v released=%v", record, released)
	}
	stored, err := store.Get(record.ID)
	if err != nil || stored.Status != "RELEASED" {
		t.Fatalf("record not persisted: %+v %v", stored, err)
	}
	if len(audit.Entries) != 2 || audit.Entries[0].Action != "release.start" || audit.Entries[1].Action != "release.approve" || audit.Entries[1].Resource != record.ID {
		t.Fatalf("release audit trail mismatch: %+v", audit.Entries)
	}
	rejected, err := svc.Start(context.Background(), "v2", "developer")
	if err != nil {
		t.Fatal(err)
	}
	rejected, err = svc.Approve(context.Background(), rejected.ID, "tenant-admin", "benchmark regression", false)
	if err != nil || rejected.Status != "REJECTED" {
		t.Fatalf("release rejection failed: %+v err=%v", rejected, err)
	}
	if len(audit.Entries) != 4 || audit.Entries[3].Action != "release.reject" || audit.Entries[3].Resource != rejected.ID {
		t.Fatalf("release rejection audit mismatch: %+v", audit.Entries)
	}
}

func TestProbeFailureBlocksApproval(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer registry.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer probe.Close()
	svc := New(data.NewMemoryStore(), registry.URL, probe.URL, "secret")
	record, err := svc.Start(context.Background(), "v1", "developer")
	if err == nil || record.Status != "FAILED" || record.StageResults[len(record.StageResults)-1].Stage != "probe" {
		t.Fatalf("probe failure did not block pipeline: %+v err=%v", record, err)
	}
	if _, err := svc.Approve(context.Background(), record.ID, "admin", "", true); err == nil {
		t.Fatal("failed pipeline must not be approvable")
	}
}

func TestFailedReleaseIsAudited(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer registry.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }))
	defer probe.Close()
	audit := &data.MemoryAuditStore{}
	svc := New(data.NewMemoryStore(), registry.URL, probe.URL, "secret", audit)
	record, err := svc.Start(context.Background(), "v1", "developer")
	if err == nil || record.Status != "FAILED" {
		t.Fatalf("expected failed release: %+v err=%v", record, err)
	}
	if len(audit.Entries) != 2 || audit.Entries[1].Action != "release.failed" || audit.Entries[1].Resource != record.ID {
		t.Fatalf("failed release audit mismatch: %+v", audit.Entries)
	}
}

func TestBenchmarkThresholdFailureBlocksApprovalAndPersistsMetrics(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer registry.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		writeBenchmarkSSE(w, 20*time.Millisecond)
	}))
	defer probe.Close()
	store := data.NewMemoryStore()
	svc := New(store, registry.URL, probe.URL, "secret")
	svc.SetBenchmarkPolicy(BenchmarkPolicy{MaxTTFTMs: 1, MinTokensPerSec: 0.1, MaxErrorRate: 0})
	record, err := svc.Start(context.Background(), "v1", "developer")
	if err == nil || record.Status != "FAILED" || record.Benchmark == nil || record.Benchmark.TTFTMs <= 1 {
		t.Fatalf("benchmark threshold did not block release: %+v err=%v", record, err)
	}
	stored, getErr := store.Get(record.ID)
	if getErr != nil || stored.Benchmark == nil || stored.Status != "FAILED" {
		t.Fatalf("failed benchmark metrics not persisted: %+v err=%v", stored, getErr)
	}
	if _, approveErr := svc.Approve(context.Background(), record.ID, "admin", "", true); approveErr == nil {
		t.Fatal("benchmark failure must not be approvable")
	}
}

func TestBenchmarkEmptyStreamBlocksRelease(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer registry.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer probe.Close()
	record, err := New(data.NewMemoryStore(), registry.URL, probe.URL, "secret").Start(context.Background(), "v1", "developer")
	if err == nil || record.Status != "FAILED" || !strings.Contains(err.Error(), "未返回有效 Token") {
		t.Fatalf("empty stream must block release: %+v err=%v", record, err)
	}
}

func writeBenchmarkSSE(w http.ResponseWriter, firstTokenDelay time.Duration) {
	w.Header().Set("Content-Type", "text/event-stream")
	time.Sleep(firstTokenDelay)
	_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"pong\"}}]}\n\n"))
	_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"completion_tokens\":1}}\n\n"))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}
