package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxBodyBytes = 16 << 20

// Handler coordinates the vLLM NIXL prefill/decode protocol. It never falls
// back to unified inference when KV transfer metadata is unavailable.
type Handler struct {
	prefill []*url.URL
	decode  []*url.URL
	client  *http.Client
}

func New(prefill, decode []string, client *http.Client) (*Handler, error) {
	prefillURLs, err := parseEndpoints(prefill)
	if err != nil {
		return nil, fmt.Errorf("prefill endpoints: %w", err)
	}
	decodeURLs, err := parseEndpoints(decode)
	if err != nil {
		return nil, fmt.Errorf("decode endpoints: %w", err)
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Handler{prefill: prefillURLs, decode: decodeURLs, client: client}, nil
}

func parseEndpoints(values []string) ([]*url.URL, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("至少需要一个端点")
	}
	result := make([]*url.URL, 0, len(values))
	for _, value := range values {
		u, err := url.Parse(strings.TrimSpace(value))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("无效端点 %q", value)
		}
		result = append(result, u)
	}
	return result, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
		return
	}
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/v1/completions") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	var original map[string]interface{}
	if err := json.Unmarshal(body, &original); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	sessionID := r.Header.Get("X-Carrot-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-Request-Id")
	}
	index := stableIndex(sessionID)
	prefillRequest := clone(original)
	prefillRequest["stream"] = false
	prefillRequest["max_tokens"] = float64(1)
	delete(prefillRequest, "stream_options")
	prefillRequest["kv_transfer_params"] = map[string]interface{}{"do_remote_decode": true, "do_remote_prefill": false}

	prefillResponse, err := h.callJSON(r, h.prefill[index%len(h.prefill)], prefillRequest)
	if err != nil {
		http.Error(w, "prefill failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	kv, ok := prefillResponse["kv_transfer_params"]
	if !ok || kv == nil {
		http.Error(w, "prefill response missing kv_transfer_params", http.StatusBadGateway)
		return
	}
	decodeRequest := clone(original)
	decodeRequest["kv_transfer_params"] = kv
	if err := h.forward(r, w, h.decode[index%len(h.decode)], decodeRequest); err != nil {
		http.Error(w, "decode failed: "+err.Error(), http.StatusBadGateway)
	}
}

func (h *Handler) callJSON(r *http.Request, endpoint *url.URL, payload interface{}) (map[string]interface{}, error) {
	resp, err := h.do(r, endpoint, payload)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream status %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return result, nil
}

func (h *Handler) forward(r *http.Request, w http.ResponseWriter, endpoint *url.URL, payload interface{}) error {
	resp, err := h.do(r, endpoint, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
}

func (h *Handler) do(r *http.Request, endpoint *url.URL, payload interface{}) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	target := *endpoint
	target.Path = strings.TrimRight(target.Path, "/") + r.URL.Path
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for _, key := range []string{"Authorization", "X-Request-Id", "X-Carrot-Session-Id"} {
		if value := r.Header.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	return h.client.Do(req)
}

func stableIndex(value string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return int(h.Sum32())
}

func clone(source map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{}, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}
