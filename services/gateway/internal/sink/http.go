// Package sink 指标上报实现。
// HTTP Sink 将请求指标异步上报到 observability 服务，失败仅记日志不阻塞推理链路。
package sink

import (
	"bytes"
	"encoding/json"
	platformauth "kk-infra/lib/auth"
	"log/slog"
	"net/http"
	"time"
)

// HTTPSink 实现 proxy.MetricsSink，通过 HTTP 上报到 observability。
type HTTPSink struct {
	baseURL       string
	client        *http.Client
	logger        *slog.Logger
	serviceSecret string
}

func (h *HTTPSink) SetServiceSecret(secret string) {
	h.serviceSecret = secret
	h.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
}

// NewHTTPSink 创建 HTTP 指标上报器。baseURL 形如 http://localhost:8084。
func NewHTTPSink(baseURL string, logger *slog.Logger) *HTTPSink {
	return &HTTPSink{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 3 * time.Second, // 上报失败不能拖慢推理
		},
		logger: logger,
	}
}

// Record 上报一条请求指标（异步，不阻塞调用方）。
func (h *HTTPSink) Record(tenantID, deploymentID, model string, latencyMs int64, ttftMs int64, inputTokens, outputTokens int, err bool) {
	payload := map[string]any{
		"tenantId":     tenantID,
		"deploymentId": deploymentID,
		"model":        model,
		"latencyMs":    latencyMs,
		"ttftMs":       ttftMs,
		"inputTokens":  inputTokens,
		"outputTokens": outputTokens,
		"err":          err,
	}
	body, jerr := json.Marshal(payload)
	if jerr != nil {
		return
	}
	go func() {
		req, rerr := http.NewRequest(http.MethodPost, h.baseURL+"/api/v1/metrics/requests", bytes.NewReader(body))
		if rerr != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if h.serviceSecret != "" {
			token, err := platformauth.IssueServiceToken(h.serviceSecret, "gateway", "observability")
			if err != nil {
				h.logger.Warn("指标服务身份签发失败")
				return
			}
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := h.client.Do(req)
		if err != nil {
			h.logger.Warn("指标上报失败", "deploymentId", deploymentID, "err", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			h.logger.Warn("指标上报返回非 200", "deploymentId", deploymentID, "status", resp.StatusCode)
		}
	}()
}
