// Package collector 指标采集器。
// MVP 实现 GPU 利用率采集：定期从 controlplane 拉取 GPU 资源状态并写入存储。
// 预留 Prometheus adapter 接口（后续轮替换为 Prometheus/DCGM 直接采集）。
package collector

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
)

// GPUCollector 定期从 controlplane 拉取 GPU 状态并上报。
type GPUCollector struct {
	sourceURL     string // controlplane /api/v1/resources/gpus
	client        *http.Client
	logger        *slog.Logger
	interval      time.Duration
	serviceSecret string

	// OnGPU 每轮采集回调（由 observability server 注入存储写入）
	OnGPU func(view apitypes.GPUResourcesView)
}

func (c *GPUCollector) SetServiceSecret(secret string) {
	c.serviceSecret = secret
	c.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
}

// NewGPUCollector 创建 GPU 采集器。
func NewGPUCollector(sourceURL string, logger *slog.Logger, interval time.Duration) *GPUCollector {
	return &GPUCollector{
		sourceURL: sourceURL,
		client:    &http.Client{Timeout: 5 * time.Second},
		logger:    logger,
		interval:  interval,
	}
}

// Run 循环采集直到 ctx 取消。
func (c *GPUCollector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.collect()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.collect()
		}
	}
}

func (c *GPUCollector) collect() {
	if c.sourceURL == "" || c.OnGPU == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sourceURL, nil)
	if err != nil {
		return
	}
	if c.serviceSecret != "" {
		token, err := platformauth.IssueServiceToken(c.serviceSecret, "observability", "controlplane")
		if err != nil {
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Warn("GPU 采集失败", "err", err)
		return
	}
	defer resp.Body.Close()

	var wrap struct {
		Code int                       `json:"code"`
		Data apitypes.GPUResourcesView `json:"data"`
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err := json.Unmarshal(body, &wrap); err != nil || wrap.Code != 0 {
		c.logger.Warn("GPU 采集响应解析失败", "code", wrap.Code, "err", err)
		return
	}
	c.OnGPU(wrap.Data)
}
