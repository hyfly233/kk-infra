package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/services/observability/internal/metrics"
	"kk-infra/services/observability/internal/usage"
)

type fakeUsageStore struct {
	records []usage.Record
	card    usage.RateCard
	rows    []usage.DailyUsage
}

func (f *fakeUsageStore) Record(_ context.Context, r usage.Record) error {
	f.records = append(f.records, r)
	return nil
}
func (f *fakeUsageStore) SetRateCard(_ context.Context, r usage.RateCard) error {
	f.card = r
	return nil
}
func (f *fakeUsageStore) ListDaily(context.Context, string, time.Time, time.Time) ([]usage.DailyUsage, error) {
	return f.rows, nil
}
func (f *fakeUsageStore) AccrueGPU(context.Context, time.Time, time.Duration) error { return nil }

func TestUsageIngestionAndBillingAPI(t *testing.T) {
	store := &fakeUsageStore{rows: []usage.DailyUsage{{TenantID: "tenant-a", DeploymentID: "dep-1", InputTokens: 10, OutputTokens: 20}}}
	srv := NewServer(metrics.NewStore(0), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetUsageStore(store)
	h := srv.Handler()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/metrics/requests", jsonBody(`{"tenantId":"tenant-a","deploymentId":"dep-1","model":"qwen","inputTokens":10,"outputTokens":20}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || len(store.records) != 1 || store.records[0].InputTokens != 10 {
		t.Fatalf("record status=%d records=%+v", rec.Code, store.records)
	}
	req = httptest.NewRequest(http.MethodGet, "/internal/billing?tenantId=tenant-a", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var response apitypes.Response
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Code != 0 {
		t.Fatalf("billing response=%s err=%v", rec.Body.String(), err)
	}
}
func jsonBody(value string) io.Reader { return &stringReader{s: value} }

type stringReader struct {
	s   string
	off int
}

func (r *stringReader) Read(p []byte) (int, error) {
	if r.off >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.off:])
	r.off += n
	return n, nil
}
