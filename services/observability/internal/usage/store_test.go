package usage

import (
	"context"
	"testing"
	"time"
)

func TestBillingCostCalculation(t *testing.T) {
	card := RateCard{InputTokenPerMillion: 2, OutputTokenPerMillion: 6, GPUHour: 3}
	if got := TokenCost(500_000, 250_000, card); got != 2.5 {
		t.Fatalf("token cost=%v", got)
	}
	if got := GPUCost(7200, card); got != 6 {
		t.Fatalf("gpu cost=%v", got)
	}
}
func TestBillingZeroUsage(t *testing.T) {
	if TokenCost(0, 0, RateCard{}) != 0 || GPUCost(0, RateCard{}) != 0 {
		t.Fatal("zero usage must cost zero")
	}
}

func TestMemoryStoreAggregatesTenantUsage(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	_ = s.SetRateCard(ctx, RateCard{TenantID: "tenant-a", GPUType: "default", InputTokenPerMillion: 2, OutputTokenPerMillion: 6})
	now := time.Now()
	_ = s.Record(ctx, Record{TenantID: "tenant-a", DeploymentID: "dep-1", InputTokens: 500_000, OutputTokens: 250_000, CreatedAt: now})
	_ = s.Record(ctx, Record{TenantID: "tenant-b", DeploymentID: "dep-2", InputTokens: 9, CreatedAt: now})
	rows, err := s.ListDaily(ctx, "tenant-a", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil || len(rows) != 1 || rows[0].EstimatedCost != 2.5 || rows[0].RequestCount != 1 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}
