package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManagedDeleteValidatesAllOwnershipAndPinsUIDs(t *testing.T) {
	for _, scenario := range []string{"success", "wrong-owner", "changed-uid"} {
		t.Run(scenario, func(t *testing.T) {
			deleted := map[string]bool{}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/pods") || strings.HasSuffix(r.URL.Path, "/replicasets") {
					_, _ = w.Write([]byte(`{"items":[]}`))
					return
				}
				if r.Method == http.MethodGet {
					if deleted[r.URL.Path] {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					owner := "d1"
					if scenario == "wrong-owner" && strings.Contains(r.URL.Path, "/secrets/") {
						owner = "d2"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": r.URL.Path, "labels": map[string]string{"carrot.ai/deployment-id": owner, "carrot.ai/managed-by": "carrot"}}})
					return
				}
				calls++
				var options struct {
					Preconditions struct {
						UID string `json:"uid"`
					} `json:"preconditions"`
				}
				if err := json.NewDecoder(r.Body).Decode(&options); err != nil || options.Preconditions.UID != r.URL.Path {
					t.Errorf("missing UID precondition: %+v %v", options, err)
				}
				if scenario == "changed-uid" {
					w.WriteHeader(http.StatusConflict)
					return
				}
				deleted[r.URL.Path] = true
			}))
			defer api.Close()
			c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
			err := c.DeleteManagedDeployment(context.Background(), "model", "tenant-a", "d1")
			if (err == nil) != (scenario == "success") {
				t.Fatalf("scenario=%s err=%v", scenario, err)
			}
			if scenario == "wrong-owner" && calls != 0 {
				t.Fatal("deleted resources before complete identity validation")
			}
			if scenario == "success" && calls != 3 {
				t.Fatalf("deleted %d resources", calls)
			}
		})
	}
}
