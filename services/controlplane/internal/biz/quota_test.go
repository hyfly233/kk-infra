package biz

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"kk-infra/services/controlplane/internal/data"
)

func TestQuotaReserveConcurrentRequests(t *testing.T) {
	store := data.NewMemoryQuotaStore()
	uc := NewQuotaUseCase(store, slog.Default())
	if _, err := uc.Set("tenant", "A100", 4); err != nil {
		t.Fatal(err)
	}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if uc.Reserve(context.Background(), "tenant", "A100", 1) == nil {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 4 {
		t.Fatalf("accepted %d, want 4", accepted.Load())
	}
	uc.Release(context.Background(), "tenant", "A100", 2)
	if err := uc.Reserve(context.Background(), "tenant", "A100", 2); err != nil {
		t.Fatal(err)
	}
}
