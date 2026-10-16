package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReconcileScaledObjectEnabled(t *testing.T) {
	var posted map[string]interface{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), kedaEnabled: true, prometheusURL: "http://prometheus:9090"}
	spec := &DeploymentSpec{Name: "qwen", DeploymentID: "dep-1", Labels: map[string]string{"carrot.ai/tenant-id": "tenant-a"}}
	if err := c.reconcileScaledObject(context.Background(), spec, "tenant-tenant-a"); err != nil {
		t.Fatal(err)
	}
	metadata := posted["metadata"].(map[string]interface{})
	if metadata["name"] != "qwen" {
		t.Fatalf("metadata=%v", metadata)
	}
	body := posted["spec"].(map[string]interface{})
	triggers := body["triggers"].([]interface{})
	if len(triggers) != 2 {
		t.Fatalf("triggers=%v", triggers)
	}
}

func TestReconcileScaledObjectDisabledDoesNotCallAPI(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	if err := c.reconcileScaledObject(context.Background(), &DeploymentSpec{Name: "qwen"}, "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("disabled KEDA made %d calls", calls)
	}
}

func TestReconcileScaledObjectTargetsRollout(t *testing.T) {
	var posted map[string]interface{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&posted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), kedaEnabled: true, progressiveEnabled: true, prometheusURL: "http://prometheus:9090"}
	if err := c.reconcileScaledObject(context.Background(), &DeploymentSpec{Name: "qwen", DeploymentID: "d1", Labels: map[string]string{"carrot.ai/tenant-id": "t1"}}, "tenant-t1"); err != nil {
		t.Fatal(err)
	}
	target := posted["spec"].(map[string]interface{})["scaleTargetRef"].(map[string]interface{})
	if target["kind"] != "Rollout" || target["apiVersion"] != "argoproj.io/v1alpha1" {
		t.Fatalf("unexpected target: %+v", target)
	}
}
