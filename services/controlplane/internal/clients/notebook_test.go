package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kk-infra/lib/domain"
)

func TestNotebookHubLifecycleUsesScopedOwnerAndAsyncStates(t *testing.T) {
	name := domain.NotebookUsername("tenant-a", "u1")
	exists, pending, ready := false, "", false
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token hub-secret" {
			t.Error("missing service identity")
		}
		if !strings.HasPrefix(r.URL.Path, "/hub/api/users/"+name) {
			t.Errorf("unscoped path %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			if !exists {
				w.WriteHeader(404)
				return
			}
			servers := map[string]any{}
			if pending != "" || ready {
				servers["workspace"] = map[string]any{"pending": pending, "ready": ready, "url": "https://malicious.example"}
			}
			json.NewEncoder(w).Encode(map[string]any{"name": name, "servers": servers})
		case http.MethodPost:
			if strings.HasSuffix(r.URL.Path, "/servers/workspace") {
				var body map[string]string
				json.NewDecoder(r.Body).Decode(&body)
				if body["platform_spawn_token"] != "short-lived-grant" {
					t.Error("missing grant")
				}
				pending = "spawn"
				w.WriteHeader(202)
			} else {
				exists = true
				w.WriteHeader(201)
			}
		case http.MethodDelete:
			var body map[string]bool
			json.NewDecoder(r.Body).Decode(&body)
			if !body["remove"] {
				t.Error("named server not removed")
			}
			pending = "stop"
			w.WriteHeader(202)
		}
	}))
	defer hub.Close()
	c, err := NewNotebookHubClient(hub.URL, "https://notebooks.example", "hub-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, err := c.Start(ctx, "tenant-a", "u1", "short-lived-grant")
	if err != nil || w.Status != "STARTING" {
		t.Fatalf("start=%+v err=%v", w, err)
	}
	pending, ready = "", true
	w, err = c.Get(ctx, "tenant-a", "u1")
	if err != nil || w.Status != "RUNNING" || w.URL != "https://notebooks.example/user/"+name+"/workspace/lab" {
		t.Fatalf("get=%+v err=%v", w, err)
	}
	w, err = c.Delete(ctx, "tenant-a", "u1")
	if err != nil || w.Status != "STOPPING" || w.URL != "" {
		t.Fatalf("delete=%+v err=%v", w, err)
	}
	if name == domain.NotebookUsername("tenant-b", "u1") || name == domain.NotebookUsername("tenant-a", "u2") {
		t.Fatal("owner collision")
	}
}

func TestNotebookHubRejectsOwnerMismatchAndSanitizesErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{200, `{"name":"another-user"}`}, {500, `hub-secret short-lived-grant`}, {302, ""}} {
		hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://external.example")
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		c, err := NewNotebookHubClient(hub.URL, "https://notebooks.example", "hub-secret")
		if err != nil {
			t.Fatal(err)
		}
		_, err = c.Get(context.Background(), "tenant-a", "u1")
		if err == nil || strings.Contains(err.Error(), "hub-secret") || strings.Contains(err.Error(), "short-lived-grant") {
			t.Fatalf("unsafe error: %v", err)
		}
		hub.Close()
	}
}
