// Package proxy OpenAI 兼容请求代理：转发、流式透传、限流、超时。
package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/gateway/internal/router"
)

// MetricsSink 指标上报接口（observability 注入，nil 可跳过）
type MetricsSink interface {
	Record(tenantID, deploymentID, model string, latencyMs int64, ttftMs int64, inputTokens, outputTokens int, err bool)
}

// AuthorizeFunc 模型授权回调（R2-4：API Key 模型白名单）。
// 返回 nil 表示允许，返回错误表示拒绝（错误信息透传给客户端）。
type AuthorizeFunc func(ctx context.Context, apiKey, model string) error

// Proxy 推理代理
type Proxy struct {
	routes    *router.Table
	client    *http.Client
	logger    *slog.Logger
	metrics   MetricsSink
	authorize AuthorizeFunc // 可为 nil（不校验模型授权）

	// 租户限流（简单令牌桶：每分钟 N 请求）
	mu     sync.Mutex
	limits map[string]*rateLimiter
	// 限流配置：每分钟请求数，0=不限
	RatePerMinute int
}

// SetAuthorize 设置模型授权回调（R2-4）
func (p *Proxy) SetAuthorize(fn AuthorizeFunc) {
	p.authorize = fn
}

type rateLimiter struct {
	windowStart time.Time
	count       int
}

// NewProxy 创建代理
func NewProxy(routes *router.Table, logger *slog.Logger, metrics MetricsSink) *Proxy {
	return &Proxy{
		routes:  routes,
		logger:  logger,
		metrics: metrics,
		client: &http.Client{
			Timeout: 120 * time.Second, // 推理请求可能较长
		},
		limits:        make(map[string]*rateLimiter),
		RatePerMinute: 0, // 默认不限流
	}
}

// ChatRequest 代理侧请求（解析 model 字段）
type ChatRequest struct {
	Model    string          `json:"model"`
	Stream   bool            `json:"stream"`
	Messages json.RawMessage `json:"messages"`
}

// Forward 转发 chat/completions 请求
func (p *Proxy) Forward(w http.ResponseWriter, r *http.Request, tenantID string) {
	// 解析请求体（保留原始 body 转发）
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读取请求体失败")
		return
	}
	var chat ChatRequest
	if err := json.Unmarshal(raw, &chat); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败")
		return
	}
	if chat.Model == "" {
		writeErr(w, http.StatusBadRequest, "model 字段必填")
		return
	}
	if chat.Stream {
		var payload map[string]interface{}
		if json.Unmarshal(raw, &payload) == nil {
			payload["stream_options"] = map[string]bool{"include_usage": true}
			if encoded, encodeErr := json.Marshal(payload); encodeErr == nil {
				raw = encoded
			}
		}
	}

	// R2-4：模型授权（API Key 白名单）
	if p.authorize != nil {
		apiKey := r.Header.Get("Authorization")
		apiKey = strings.TrimPrefix(apiKey, "Bearer ")
		if err := p.authorize(r.Context(), apiKey, chat.Model); err != nil {
			writeErr(w, http.StatusForbidden, err.Error())
			return
		}
	}

	// 限流
	if p.RatePerMinute > 0 {
		if !p.allow(tenantID) {
			writeErr(w, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
			return
		}
	}

	// 路由
	route, err := p.routes.Resolve(chat.Model)
	if err != nil {
		writeErr(w, http.StatusNotFound, "模型不可用: "+chat.Model)
		return
	}
	// 租户隔离：只有归属租户可访问（MVP 单租户，保留校验）
	if route.TenantID != "" && tenantID != "" && route.TenantID != tenantID {
		writeErr(w, http.StatusForbidden, "无权访问该模型")
		return
	}

	// 构造上游请求
	target := strings.TrimRight(route.Endpoint, "/") + "/v1/chat/completions"
	upReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, strings.NewReader(string(raw)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "构造上游请求失败")
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	sessionID := r.Header.Get("X-Carrot-Session-Id")
	if sessionID == "" {
		sessionID = middleware.GetRequestID(r.Context())
	}
	if sessionID != "" {
		upReq.Header.Set("X-Carrot-Session-Id", sessionID)
		upReq.Header.Set("X-Request-Id", sessionID)
	}
	// 透传 Authorization（若上游需要）

	start := time.Now()
	upResp, err := p.client.Do(upReq)
	if err != nil {
		p.logger.Error("上游请求失败", "model", chat.Model, "err", err)
		p.record(route, start, 0, 0, 0, 0, true)
		writeErr(w, http.StatusBadGateway, "模型服务不可达")
		return
	}
	defer upResp.Body.Close()

	// 非 2xx：透传错误
	if upResp.StatusCode < 200 || upResp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(upResp.Body, 64<<10))
		p.record(route, start, 0, 0, 0, 0, true)
		p.logger.Error("上游返回错误", "model", chat.Model, "status", upResp.StatusCode, "body", truncate(string(body), 200))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write(body)
		return
	}

	// 流式透传
	if chat.Stream {
		p.forwardStream(w, r, upResp, route, start)
		return
	}
	// 非流式透传（并统计 token）
	p.forwardNonStream(w, upResp, route, start)
}

// forwardNonStream 非流式转发
func (p *Proxy) forwardNonStream(w http.ResponseWriter, upResp *http.Response, route *router.Route, start time.Time) {
	body, err := io.ReadAll(io.LimitReader(upResp.Body, 4<<20))
	if err != nil {
		p.record(route, start, 0, 0, 0, 0, true)
		writeErr(w, http.StatusBadGateway, "读取上游响应失败")
		return
	}
	// 统计 token（尽力解析）
	inputTokens, outputTokens := int64(0), int64(0)
	var usage struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &usage) == nil {
		inputTokens = int64(usage.Usage.PromptTokens)
		outputTokens = int64(usage.Usage.CompletionTokens)
		if inputTokens == 0 && outputTokens == 0 {
			outputTokens = int64(usage.Usage.TotalTokens)
		}
	}
	latency := time.Since(start).Milliseconds()
	p.record(route, start, latency, 0, inputTokens, outputTokens, false)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// forwardStream 流式转发（SSE 透传 + 首 Token 时间统计）
func (p *Proxy) forwardStream(w http.ResponseWriter, r *http.Request, upResp *http.Response, route *router.Route, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		p.logger.Error("流式转发：ResponseWriter 不支持 Flusher")
		writeErr(w, http.StatusInternalServerError, "不支持流式响应")
		return
	}
	p.logger.Debug("流式转发开始", "model", route.Model, "upstreamStatus", upResp.StatusCode)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	scanner := bufio.NewScanner(upResp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	firstChunk := true
	inputTokens, outputTokens := int64(0), int64(0)
	fallbackOutputTokens := int64(0)
	ttftMs := int64(0)
	for scanner.Scan() {
		line := scanner.Text()
		// 统计首 Token 时间与 token 数
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" {
				fallbackOutputTokens++
				var chunk struct {
					Usage *struct {
						PromptTokens     int `json:"prompt_tokens"`
						CompletionTokens int `json:"completion_tokens"`
					} `json:"usage"`
				}
				if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil {
					inputTokens = int64(chunk.Usage.PromptTokens)
					outputTokens = int64(chunk.Usage.CompletionTokens)
				}
				if firstChunk {
					ttftMs = time.Since(start).Milliseconds()
					firstChunk = false
				}
			}
		}
		_, _ = fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()
	}
	streamFailed := false
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		p.logger.Error("流式读取上游失败", "model", route.Model, "err", err)
		streamFailed = true
	}
	if outputTokens == 0 {
		outputTokens = fallbackOutputTokens
	}
	latency := time.Since(start).Milliseconds()
	p.record(route, start, latency, ttftMs, inputTokens, outputTokens, streamFailed)
}

// record 指标上报
func (p *Proxy) record(route *router.Route, start time.Time, latencyMs, ttftMs, inputTokens, outputTokens int64, err bool) {
	if p.metrics != nil {
		modelID := route.ModelID
		if modelID == "" {
			modelID = route.Model
		}
		p.metrics.Record(route.TenantID, route.DeploymentID, modelID, latencyMs, ttftMs, int(inputTokens), int(outputTokens), err)
	}
}

// allow 租户限流判断
func (p *Proxy) allow(tenantID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	l, ok := p.limits[tenantID]
	if !ok || now.Sub(l.windowStart) >= time.Minute {
		p.limits[tenantID] = &rateLimiter{windowStart: now, count: 0}
		l = p.limits[tenantID]
	}
	l.count++
	return l.count <= p.RatePerMinute
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    "gateway_error",
			"code":    fmt.Sprintf("%d", status),
		},
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// 确保 errcode 引用（响应错误码统一）
var _ = errcode.ErrUpstream
