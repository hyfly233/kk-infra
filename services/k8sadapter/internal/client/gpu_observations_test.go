package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGPUCapacityAttributionUsesSamePodSnapshot(t *testing.T) {
	invalidGeneration := ""
	pod := func(name, phase, node string, managed bool, count string) map[string]any {
		labels := map[string]string{"carrot.ai/deployment-id": "deploy-a", "carrot.ai/tenant-id": "tenant-a"}
		if managed {
			labels["carrot.ai/managed-by"] = "carrot"
			if name == "stable" {
				labels["carrot.ai/template-generation"] = "0"
			}
			if name == "canary" {
				labels["carrot.ai/template-generation"] = "1"
			}
			if invalidGeneration != "" {
				labels["carrot.ai/template-generation"] = invalidGeneration
			}
		}
		return map[string]any{"metadata": map[string]any{"name": name, "namespace": "tenant-a", "labels": labels, "deletionTimestamp": "2026-09-26T00:00:00Z"}, "spec": map[string]any{"nodeName": node, "containers": []any{map[string]any{"resources": map[string]any{"requests": map[string]string{"nvidia.com/gpu": count}}}}}, "status": map[string]string{"phase": phase}}
	}
	podCalls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes":
			fmt.Fprint(w, `{"items":[{"metadata":{"name":"node-a","labels":{"carrot.ai/gpu-type":"A100"}},"status":{"allocatable":{"nvidia.com/gpu":"8"},"conditions":[{"type":"Ready","status":"True"}]}}]}`)
		case "/api/v1/pods":
			podCalls++
			json.NewEncoder(w).Encode(map[string]any{"items": []any{pod("stable", "Running", "node-a", true, "2"), pod("canary", "Pending", "node-a", true, "1"), pod("legacy", "Running", "node-a", true, "1"), pod("external", "Running", "node-a", false, "1"), pod("done", "Succeeded", "node-a", true, "4"), pod("unassigned", "Pending", "", true, "4")}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	nodes, assigned, err := c.ListGPUCapacitySnapshot(context.Background())
	if err != nil || len(nodes) != 1 || nodes[0].Used != 5 || len(assigned) != 3 || podCalls != 1 {
		t.Fatalf("nodes=%+v assigned=%+v calls=%d err=%v", nodes, assigned, podCalls, err)
	}
	var total int32
	for _, item := range assigned {
		if item.DeploymentID != "deploy-a" || item.TenantID != "tenant-a" || item.GPUType != "A100" || item.NodeName != "node-a" {
			t.Fatalf("wrong attribution: %+v", item)
		}
		total += item.GPUCount
	}
	if total != 4 {
		t.Fatalf("assigned GPUs=%d", total)
	}
	if assigned[0].TemplateGeneration == nil || *assigned[0].TemplateGeneration != 0 || assigned[1].TemplateGeneration == nil || *assigned[1].TemplateGeneration != 1 || assigned[2].TemplateGeneration != nil {
		t.Fatalf("template generations lost: %+v", assigned)
	}
	for _, invalid := range []string{"-1", "bad", "9223372036854775808"} {
		invalidGeneration = invalid
		if _, _, err := c.ListGPUCapacitySnapshot(context.Background()); err == nil {
			t.Fatalf("invalid generation %q accepted", invalid)
		}
	}
}

func TestScaleDoesNotChangePodTemplateGeneration(t *testing.T) {
	for _, progressive := range []bool{false, true} {
		t.Run(fmt.Sprint(progressive), func(t *testing.T) {
			patched := false
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch {
					var patch map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
						t.Error(err)
					}
					var spec map[string]json.RawMessage
					if err := json.Unmarshal(patch["spec"], &spec); err != nil {
						t.Error(err)
					}
					if len(patch) != 1 || len(spec) != 1 || string(spec["replicas"]) != "3" {
						t.Errorf("scale modified template: %+v", patch)
					}
					patched = true
					fmt.Fprint(w, `{}`)
					return
				}
				if r.URL.RawQuery != "" {
					fmt.Fprint(w, `{"items":[]}`)
					return
				}
				fmt.Fprint(w, `{"metadata":{"labels":{"carrot.ai/deployment-id":"deploy-a"}},"spec":{"replicas":3},"status":{"phase":"Healthy","readyReplicas":3,"availableReplicas":3}}`)
			}))
			defer api.Close()
			c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), progressiveEnabled: progressive}
			if _, err := c.ScaleDeployment(context.Background(), "demo", "tenant-a", 3); err != nil {
				t.Fatal(err)
			}
			if !patched {
				t.Fatal("scale patch missing")
			}
		})
	}
}
