package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kk-infra/services/pipeline"
	"kk-infra/services/pipeline/internal/data"
	"kk-infra/services/pipeline/internal/service"
)

func TestListReleasesFiltersAndLimits(t *testing.T) {
	store := data.NewMemoryStore()
	base := time.Now().UTC()
	for _, record := range []*pipeline.ReleaseRecord{
		{ID: "old-v1", ModelVersionID: "v1", Status: "FAILED", CreatedAt: base},
		{ID: "new-v2", ModelVersionID: "v2", Status: "RELEASED", CreatedAt: base.Add(2 * time.Second)},
		{ID: "new-v1", ModelVersionID: "v1", Status: "PENDING_APPROVAL", CreatedAt: base.Add(time.Second)},
	} {
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(service.New(store, "", "", ""), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases?modelVersionId=v1&limit=1", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Code int                      `json:"code"`
		Data []pipeline.ReleaseRecord `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || len(body.Data) != 1 || body.Data[0].ID != "new-v1" {
		t.Fatalf("filtered release list mismatch: %+v", body)
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/v1/releases?limit=201", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit returned %d: %s", bad.Code, bad.Body.String())
	}
}
