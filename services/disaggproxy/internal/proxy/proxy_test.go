package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoordinatesPrefillAndDecode(t *testing.T) {
	var decodeBody map[string]interface{}
	prefill := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != false || body["max_tokens"] != float64(1) {
			t.Fatalf("unexpected prefill request: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"kv_transfer_params": map[string]interface{}{"remote_block_ids": []int{1, 2}}})
	}))
	defer prefill.Close()
	decode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&decodeBody)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: done\n\n"))
	}))
	defer decode.Close()
	handler, err := New([]string{prefill.URL}, []string{decode.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"max_tokens":9}`))
	req.Header.Set("X-Carrot-Session-Id", "conversation-1")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "data: done\n\n" {
		t.Fatalf("unexpected response: %d %q", recorder.Code, recorder.Body.String())
	}
	if decodeBody["stream"] != true || decodeBody["max_tokens"] != float64(9) || decodeBody["kv_transfer_params"] == nil {
		t.Fatalf("unexpected decode request: %#v", decodeBody)
	}
}

func TestRejectsMissingKVTransferMetadata(t *testing.T) {
	prefill := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer prefill.Close()
	decodeCalled := false
	decode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { decodeCalled = true }))
	defer decode.Close()
	handler, _ := New([]string{prefill.URL}, []string{decode.URL}, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(`{"model":"m"}`)))
	if recorder.Code != http.StatusBadGateway || decodeCalled {
		t.Fatalf("expected fail-closed response, got %d decodeCalled=%v", recorder.Code, decodeCalled)
	}
}

func TestRejectsInvalidEndpoint(t *testing.T) {
	if _, err := New([]string{"prefill"}, []string{"http://decode"}, nil); err == nil {
		t.Fatal("expected invalid endpoint error")
	}
}
