package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeleteRequiresConfirmedResourceRemoval(t *testing.T) {
	for _, scenario := range []string{"gone", "controller", "replicaset", "terminating-pod", "service", "query-error", "delete-error"} {
		for _, progressive := range []bool{false, true} {
			t.Run(scenario+map[bool]string{false: "/deployment", true: "/rollout"}[progressive], func(t *testing.T) {
				pending := scenario != "gone"
				foreground := false
				api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					controller := strings.HasSuffix(r.URL.Path, "/deployments/model") || strings.HasSuffix(r.URL.Path, "/rollouts/model")
					if r.Method == http.MethodDelete {
						if controller {
							var options map[string]string
							if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
								t.Error(err)
							}
							foreground = options["propagationPolicy"] == "Foreground"
							if !foreground {
								t.Error("missing foreground deletion")
							}
						}
						if pending && scenario == "delete-error" && strings.HasSuffix(r.URL.Path, "/secrets/model-artifact") {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						w.WriteHeader(http.StatusOK)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/pods") || strings.HasSuffix(r.URL.Path, "/replicasets") {
						if r.URL.Query().Get("labelSelector") != "app=model" {
							t.Errorf("unsafe selector: %s", r.URL.RawQuery)
						}
						if pending && scenario == "query-error" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if pending && ((scenario == "replicaset" && strings.HasSuffix(r.URL.Path, "/replicasets")) || (scenario == "terminating-pod" && strings.HasSuffix(r.URL.Path, "/pods"))) {
							_, _ = w.Write([]byte(`{"items":[{"metadata":{"deletionTimestamp":"2026-09-26T00:00:00Z"}}]}`))
							return
						}
						_, _ = w.Write([]byte(`{"items":[]}`))
						return
					}
					if pending && ((scenario == "controller" && controller) || (scenario == "service" && strings.HasSuffix(r.URL.Path, "/services/model"))) {
						_, _ = w.Write([]byte(`{"metadata":{"deletionTimestamp":"2026-09-26T00:00:00Z"}}`))
						return
					}
					w.WriteHeader(http.StatusNotFound)
				}))
				defer api.Close()
				c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), progressiveEnabled: progressive, kedaEnabled: true, volcanoEnabled: true}
				err := c.DeleteDeployment(context.Background(), "model", "tenant-test")
				if (err != nil) != pending || !foreground {
					t.Fatalf("pending=%v foreground=%v err=%v", pending, foreground, err)
				}
				// Retry must succeed once the API server confirms all resources gone.
				pending = false
				if err := c.DeleteDeployment(context.Background(), "model", "tenant-test"); err != nil {
					t.Fatalf("retry failed: %v", err)
				}
			})
		}
	}
}
