package clients

import (
	"context"
	"kk-infra/services/controlplane/internal/clusters"
	"net/http"
	"net/http/httptest"
	"testing"
)

type cleanupResolver struct {
	ClusterAdapterResolver
	url string
}

func (r cleanupResolver) Get(string) (clusters.Cluster, error) {
	return clusters.Cluster{AdapterURL: r.url}, nil
}

func TestManagedCleanupNeverFallsBackToLegacyDelete(t *testing.T) {
	for _, supported := range []bool{true, false} {
		calls := 0
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method != http.MethodDelete || r.URL.Path != "/v1/deployments/model/managed" || r.URL.Query().Get("deploymentId") != "d1" || r.URL.Query().Get("namespace") != "tenant-a" {
				t.Errorf("wrong managed request: %s %s", r.Method, r.URL.String())
			}
			if !supported {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"deleted":true}}`))
		}))
		pool := NewClusterAdapterPool(cleanupResolver{url: api.URL}, nil)
		err := pool.DeleteManagedDeploymentForCluster(context.Background(), "old", "model", "tenant-a", "d1")
		api.Close()
		if (err == nil) != supported || calls != 1 {
			t.Fatalf("supported=%v err=%v calls=%d", supported, err, calls)
		}
	}
}
