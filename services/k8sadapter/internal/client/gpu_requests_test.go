package client

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"kk-infra/lib/domain"
)

func TestRealGPUCapacityCountsAssignedPodsAndInitSidecars(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			fmt.Fprint(w, `{"items":[{"metadata":{"name":"gpu-1","labels":{"carrot.ai/gpu-type":"H100","nvidia.com/gpu.product":"NVIDIA-H100","nvidia.com/gpu.memory":"81920"}},"status":{"capacity":{"nvidia.com/gpu":"8"},"allocatable":{"nvidia.com/gpu":"7"},"conditions":[{"type":"Ready","status":"True"}]}}]}`)
		case "/api/v1/pods":
			fmt.Fprint(w, `{"items":[
			{"spec":{"nodeName":"gpu-1","containers":[{"resources":{"requests":{"nvidia.com/gpu":"2"}}}],"initContainers":[{"restartPolicy":"Always","resources":{"limits":{"nvidia.com/gpu":"1"}}},{"resources":{"requests":{"nvidia.com/gpu":"3"}}}]},"status":{"phase":"Running"}},
			{"metadata":{"deletionTimestamp":"2026-09-26T00:00:00Z"},"spec":{"nodeName":"gpu-1","containers":[{"resources":{"limits":{"nvidia.com/gpu":"1"}}}]},"status":{"phase":"Running"}},
			{"spec":{"nodeName":"gpu-1","containers":[{"resources":{"requests":{"nvidia.com/gpu":"4"}}}]},"status":{"phase":"Succeeded"}},
			{"spec":{"nodeName":"gpu-1","containers":[{"resources":{"requests":{"nvidia.com/gpu":"4"}}}]},"status":{"phase":"Failed"}},
			{"spec":{"containers":[{"resources":{"requests":{"nvidia.com/gpu":"4"}}}]},"status":{"phase":"Pending"}}
			]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	nodes, err := c.ListGPUNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 || nodes[0].GPUType != "H100" || nodes[0].Used != 5 || nodes[0].Total != 8 || nodes[0].Allocatable != 7 || nodes[0].Health != domain.GPUHealthHealthy {
		t.Fatalf("unexpected capacity: %+v", nodes)
	}
}

func TestRealGPUCapacityFailsWhenPodListIsForbidden(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/nodes" {
			fmt.Fprint(w, `{"items":[{"status":{"allocatable":{"nvidia.com/gpu":"8"}}}]}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	if _, err := c.ListGPUNodes(context.Background()); err == nil {
		t.Fatal("must not report zero occupancy on collection failure")
	}
}
