package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/pipeline/internal/data"
)

func TestPipelineRejectsWrongTenantBeforeCreatingRelease(t *testing.T) {
	mutations := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := platformauth.AuthenticateService("secret", platformauth.BearerToken(r), "modelregistry", "pipeline"); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodGet {
			mutations++
		}
		json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]string{"id": "v1", "tenantId": "tenant-a"}})
	}))
	defer api.Close()
	store := data.NewMemoryStore()
	s := New(store, api.URL, "", "")
	s.SetServiceSecret("secret")
	for _, tenant := range []string{"tenant-b", ""} {
		if _, err := s.Start(context.Background(), "v1", "developer", tenant); err == nil {
			t.Fatal("wrong tenant release accepted")
		}
	}
	rows, err := store.List(100, "", "")
	if err != nil || len(rows) != 0 || mutations != 0 {
		t.Fatalf("unauthorized side effect: rows=%d calls=%d err=%v", len(rows), mutations, err)
	}
	if err := s.authorizeVersion(context.Background(), "v1", "tenant-a"); err != nil {
		t.Fatal(err)
	}
}

func TestPipelineDoesNotTrustHTTP200WithoutExpectedVersionState(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"code":0,"data":{"id":"wrong-version","tenantId":"tenant-a","status":"VALIDATED"}}`))
	}))
	defer api.Close()
	s := New(data.NewMemoryStore(), api.URL, "", "")
	s.SetServiceSecret("secret")
	if err := s.modelAction(context.Background(), "v1", "validate", false); err == nil {
		t.Fatal("wrong version state accepted")
	}
}
