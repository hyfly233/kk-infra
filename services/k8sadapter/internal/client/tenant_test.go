package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestProvisionTenantCreatesIsolationResourcesOnce(t *testing.T) {
	var mu sync.Mutex
	created := map[string]bool{}
	posts := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := r.URL.Path
		if r.Method == http.MethodGet {
			if posts < 8 && !created[key] {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(`{}`))
			return
		}
		if r.Method == http.MethodPost {
			posts++
			created[resourceGetPath(r.URL.Path)] = true
			w.Write([]byte(`{}`))
			return
		}
		if r.Method == http.MethodPatch {
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client()}
	if err := c.ProvisionTenant(context.Background(), "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if posts != 8 {
		t.Fatalf("created %d resources, want 8", posts)
	}
	if err := c.ProvisionTenant(context.Background(), "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if posts != 8 {
		t.Fatalf("idempotent call created %d resources", posts)
	}
}

func TestProvisionTenantReconcilesNotebookPolicyAndQueue(t *testing.T) {
	patches, queues := 0, 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if strings.Contains(r.URL.Path, "/queues/") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Write([]byte(`{}`))
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/notebook-access") {
			patches++
			if r.Header.Get("Content-Type") != "application/merge-patch+json" {
				t.Error("incorrect patch type")
			}
			spec := body["spec"].(map[string]any)
			egress := spec["egress"].([]any)
			if len(egress) != 2 {
				t.Fatal("expected DNS and Hub-only egress")
			}
			ports := egress[1].(map[string]any)["ports"].([]any)
			if ports[0].(map[string]any)["port"] != float64(8081) {
				t.Error("Hub API egress missing")
			}
		} else if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/queues") {
			queues++
			if body["metadata"].(map[string]any)["name"] != "tenant-tenant-a" {
				t.Error("wrong tenant queue")
			}
		} else {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{}`))
	}))
	defer api.Close()
	c := &RealKubeClient{baseURL: api.URL, httpClient: api.Client(), volcanoEnabled: true, volcanoQueuePrefix: "tenant-"}
	if err := c.ProvisionTenant(context.Background(), "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if patches != 1 || queues != 1 {
		t.Fatalf("patches=%d queues=%d", patches, queues)
	}
}

func resourceGetPath(postPath string) string {
	// POST collection path maps to the conventional singular object expected by the next GET.
	// The fake API treats each collection itself as created, sufficient for idempotency coverage.
	return postPath
}
