package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestTelemetryDistinguishesZeroMissingStaleAndFailedSamples(t *testing.T) {
	for _, scenario := range []string{"zero", "xid", "missing", "stale", "down", "nan", "sentinel", "error"} {
		t.Run(scenario, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/query" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if scenario == "error" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				q := r.URL.Query().Get("query")
				var result []map[string]any
				sample := func(name, value string) map[string]any {
					return map[string]any{"metric": map[string]string{"__name__": name, "instance": "node:9400", "UUID": "GPU-a", "gpu": "0"}, "value": []any{1000, value}}
				}
				switch {
				case q == `up{job="dcgm"}`:
					v := "1"
					if scenario == "down" {
						v = "0"
					}
					result = []map[string]any{sample("up", v)}
				case strings.HasPrefix(q, "max(time()"):
					v := "10"
					if scenario == "stale" {
						v = "120"
					}
					result = []map[string]any{sample("", v)}
				case q == dcgmSelector:
					for _, name := range []string{"DCGM_FI_DEV_GPU_UTIL", "DCGM_FI_DEV_FB_USED", "DCGM_FI_DEV_FB_FREE", "DCGM_FI_DEV_XID_ERRORS"} {
						if scenario == "missing" && name == "DCGM_FI_DEV_FB_FREE" {
							continue
						}
						v := "0"
						if scenario == "xid" && name == "DCGM_FI_DEV_XID_ERRORS" {
							v = "31"
						}
						if scenario == "nan" {
							v = "NaN"
						}
						if scenario == "sentinel" {
							v = "9223372036854775794"
						}
						result = append(result, sample(name, v))
					}
				default:
					t.Errorf("unexpected query %s", q)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "vector", "result": result}})
			}))
			defer api.Close()
			r := &Reporter{}
			if err := r.ConfigureTelemetry(api.URL); err != nil {
				t.Fatal(err)
			}
			got := r.telemetry.collect(context.Background())
			if scenario == "zero" || scenario == "xid" {
				if got.Status != "healthy" || len(got.Samples) != 5 || got.Samples[1].Value != 0 || got.OldestSampleAgeSeconds == nil || *got.OldestSampleAgeSeconds != 10 {
					t.Fatalf("valid zero lost: %+v", got)
				}
			} else if got.Status != "unhealthy" || len(got.Samples) != 0 || got.Reason == "" {
				t.Fatalf("invalid telemetry accepted: %+v", got)
			}
		})
	}
}

func TestTelemetryFailureDoesNotFailKubernetesCapacity(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer metrics.Close()
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
	r, err := New(&source{nodes: []domain.GPUResource{{GPUType: "A100", Total: 8, Allocatable: 8, Health: domain.GPUHealthHealthy}}}, api.URL, "gpu-west", tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.ConfigureTelemetry(metrics.URL); err != nil {
		t.Fatal(err)
	}
	if err := r.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.HealthStatus != "healthy" || len(got.GPUCapacity) != 1 || got.Telemetry == nil || got.Telemetry.Status != "unhealthy" {
		t.Fatalf("monitoring outage failed serving capacity: %+v", got)
	}
}
