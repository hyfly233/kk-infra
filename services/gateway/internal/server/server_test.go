package server

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"kk-infra/services/gateway/internal/auth"
	"kk-infra/services/gateway/internal/proxy"
	"kk-infra/services/gateway/internal/router"
)

// mockMetrics 记录指标
type mockMetrics struct {
	records []proxyMetric
}

type proxyMetric struct {
	deploymentID string
	model        string
	latencyMs    int64
	ttftMs       int64
	tokens       int
	err          bool
}

func (m *mockMetrics) Record(_ string, deploymentID, model string, latencyMs, ttftMs int64, inputTokens, outputTokens int, err bool) {
	m.records = append(m.records, proxyMetric{deploymentID, model, latencyMs, ttftMs, inputTokens + outputTokens, err})
}

// fakeUpstream 模拟上游 vLLM（非流式 + 流式），带模拟 TTFT 延迟
func fakeUpstream() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream, _ := body["stream"].(bool)
		// 模拟 TTFT：确保指标可断言（>0ms）
		time.Sleep(20 * time.Millisecond)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"好\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"你好"}}],"usage":{"total_tokens":15}}`))
	})
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen-demo"}]}`))
	})
	return mux
}

func newTestGateway(t *testing.T) (http.Handler, *mockMetrics, *router.Table, *auth.Manager) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	keys := auth.NewManager()
	routes := router.NewTable()
	metrics := &mockMetrics{}
	p := proxy.NewProxy(routes, logger, metrics)
	srv := NewServer(keys, routes, p, logger)
	return srv.Handler(), metrics, routes, keys
}

func setupRouteAndKey(t *testing.T, routes *router.Table, keys *auth.Manager, upstreamURL string) (string, string) {
	t.Helper()
	if err := routes.Register(context.Background(), &router.Route{
		Model:        "qwen-demo",
		Endpoint:     upstreamURL,
		TenantID:     "default",
		DeploymentID: "deploy-001",
	}); err != nil {
		t.Fatal(err)
	}
	res, err := keys.Issue("default")
	if err != nil {
		t.Fatalf("创建 Key 失败: %v", err)
	}
	return res.Key, res.KeyID
}

func TestRegisterRouteKeepsRolloutMetadataWithoutWeighting(t *testing.T) {
	h, _, routes, _ := newTestGateway(t)
	body := `{"model":"qwen","modelId":"m1","endpoint":"http://qwen.tenant.svc","tenantId":"t1","deploymentId":"d1","clusterId":"gpu-west","stableEndpoint":"http://qwen-stable.tenant.svc","canaryEndpoint":"http://qwen-canary.tenant.svc","rolloutStatus":"Progressing"}`
	req := httptest.NewRequest(http.MethodPost, "/internal/routes", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register failed: %d %s", rec.Code, rec.Body.String())
	}
	route, err := routes.Resolve("qwen")
	if err != nil {
		t.Fatal(err)
	}
	if route.Endpoint != "http://qwen.tenant.svc" || route.ClusterID != "gpu-west" || route.StableEndpoint == "" || route.CanaryEndpoint == "" || route.RolloutStatus != "Progressing" {
		t.Fatalf("rollout metadata lost: %+v", route)
	}
	if route.Endpoint == route.StableEndpoint || route.Endpoint == route.CanaryEndpoint {
		t.Fatalf("gateway must keep Istio entry endpoint: %+v", route)
	}
}

func TestGatewayForwardsToClusterIngressPath(t *testing.T) {
	requested := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()
	h, _, routes, keys := newTestGateway(t)
	key, _ := setupRouteAndKey(t, routes, keys, upstream.URL+"/tenant-a/qwen")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen-demo","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("gateway response=%d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case path := <-requested:
		if path != "/tenant-a/qwen/v1/chat/completions" {
			t.Fatalf("wrong cluster ingress path: %s", path)
		}
	default:
		t.Fatal("gateway did not call cluster ingress")
	}
}

// 非流式全链路：API Key 鉴权 → 路由 → 上游 → 指标
func TestChatCompletionsNonStream(t *testing.T) {
	upstream := httptest.NewServer(fakeUpstream())
	defer upstream.Close()

	h, metrics, routes, keys := newTestGateway(t)
	key, _ := setupRouteAndKey(t, routes, keys, upstream.URL)

	body := `{"model":"qwen-demo","messages":[{"role":"user","content":"你好"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "你好") {
		t.Fatalf("响应缺少上游内容: %s", rec.Body.String())
	}
	// 指标已记录
	if len(metrics.records) == 0 {
		t.Fatal("应记录调用指标")
	}
	last := metrics.records[len(metrics.records)-1]
	if last.model != "qwen-demo" || last.tokens != 15 || last.err {
		t.Fatalf("指标异常: %+v", last)
	}
}

func TestGatewayForwardsSessionAffinityKey(t *testing.T) {
	seen := ""
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("X-Carrot-Session-Id")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"total_tokens":0}}`))
	}))
	defer upstream.Close()
	h, _, routes, keys := newTestGateway(t)
	key, _ := setupRouteAndKey(t, routes, keys, upstream.URL)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen-demo","messages":[]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Carrot-Session-Id", "conversation-42")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || seen != "conversation-42" {
		t.Fatalf("session affinity key not forwarded: status=%d seen=%q", res.Code, seen)
	}
}

// 流式全链路：首 Token 统计
// 注意：httptest.ResponseRecorder 不实现 http.Flusher，必须用真实 HTTP server 验证流式。
func TestChatCompletionsStream(t *testing.T) {
	upstream := httptest.NewServer(fakeUpstream())
	defer upstream.Close()

	h, metrics, routes, keys := newTestGateway(t)
	key, _ := setupRouteAndKey(t, routes, keys, upstream.URL)

	// 用真实 HTTP server 承载网关
	gwSrv := httptest.NewServer(h)
	defer gwSrv.Close()

	body := `{"model":"qwen-demo","messages":[{"role":"user","content":"你好"}],"stream":true}`
	req, _ := http.NewRequest(http.MethodPost, gwSrv.URL+"/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")

	// 流式读取（客户端也需要连接保持）
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("状态码 = %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("应为流式响应: %s", ct)
	}
	// 校验 SSE 透传完整
	scanner := bufio.NewScanner(resp.Body)
	foundDone := false
	chunkCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			chunkCount++
			if strings.Contains(line, "[DONE]") {
				foundDone = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("读取流失败: %v", err)
	}
	if !foundDone || chunkCount < 2 {
		t.Fatalf("流式透传不完整: chunks=%d done=%v", chunkCount, foundDone)
	}
	// 首 Token 指标
	foundTTFT := false
	for _, m := range metrics.records {
		if m.ttftMs > 0 && !m.err {
			foundTTFT = true
		}
	}
	if !foundTTFT {
		t.Fatal("应记录 TTFT 指标")
	}
}

// 无效 API Key
func TestInvalidAPIKey(t *testing.T) {
	h, _, _, _ := newTestGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	req.Header.Set("Authorization", "Bearer sk-carrot-invalid")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("无效 Key 应返回 401: %d", rec.Code)
	}
}

// 缺少 Authorization
func TestMissingAuth(t *testing.T) {
	h, _, _, _ := newTestGateway(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"x"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少 Key 应返回 401: %d", rec.Code)
	}
}

// 模型无路由
func TestModelNotFound(t *testing.T) {
	h, _, _, keys := newTestGateway(t)
	res, _ := keys.Issue("default")
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"unknown-model"}`))
	req.Header.Set("Authorization", "Bearer "+res.Key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知模型应返回 404: %d", rec.Code)
	}
}

// 租户隔离：非归属租户不可访问
func TestTenantIsolation(t *testing.T) {
	upstream := httptest.NewServer(fakeUpstream())
	defer upstream.Close()

	h, _, routes, keys := newTestGateway(t)
	if err := routes.Register(context.Background(), &router.Route{
		Model:    "qwen-demo",
		Endpoint: upstream.URL,
		TenantID: "tenant-a",
	}); err != nil {
		t.Fatal(err)
	}
	res, _ := keys.Issue("tenant-b") // 不同租户
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen-demo"}`))
	req.Header.Set("Authorization", "Bearer "+res.Key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("跨租户访问应返回 403: %d", rec.Code)
	}
}

// /v1/models 列表
func TestListModels(t *testing.T) {
	h, _, routes, keys := newTestGateway(t)
	if err := routes.Register(context.Background(), &router.Route{Model: "qwen-demo", Endpoint: "http://127.0.0.1:1", TenantID: "default"}); err != nil {
		t.Fatal(err)
	}
	res, _ := keys.Issue("default")
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+res.Key)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "qwen-demo") {
		t.Fatalf("模型列表缺少 qwen-demo: %s", rec.Body.String())
	}
}
