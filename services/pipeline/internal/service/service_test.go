package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer probe.Close()

	store := data.NewMemoryStore()
	svc := New(store, registry.URL, probe.URL, "secret")
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
