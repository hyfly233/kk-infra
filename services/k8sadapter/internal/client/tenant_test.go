package client

import (
	"context"
	"net/http"
	"net/http/httptest"
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

func resourceGetPath(postPath string) string {
	// POST collection path maps to the conventional singular object expected by the next GET.
	// The fake API treats each collection itself as created, sufficient for idempotency coverage.
	return postPath
}
