// Package server observability HTTP API。
// 提供指标上报与查询接口：
//
//	POST /api/v1/metrics/requests   上报请求指标（gateway 调用）
//	POST /api/v1/metrics/gpu        上报 GPU 利用率（采集器调用）
//	GET  /api/v1/deployments/{id}/metrics?range=1h  查询部署请求指标
//	GET  /api/v1/gpus/metrics?range=1h              查询 GPU 利用率
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/observability/internal/metrics"
	"kk-infra/services/observability/internal/prometheus"
	"kk-infra/services/observability/internal/usage"
)

// Server observability HTTP 服务
type Server struct {
	store  *metrics.Store
	logger *slog.Logger
	prom   *prometheus.Client // 可为 nil（无 Prometheus 时降级内存存储）
	usage  usage.Store
}

func (s *Server) SetUsageStore(store usage.Store) { s.usage = store }

// NewServer 创建 HTTP 服务。
func NewServer(store *metrics.Store, logger *slog.Logger) *Server {
	return &Server{store: store, logger: logger}
}

// SetPrometheus 配置 Prometheus 查询客户端（R2-3：配置后查询走 Prometheus）
func (s *Server) SetPrometheus(c *prometheus.Client) {
	s.prom = c
}

// Handler 返回带中间件的路由。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 上报
	mux.HandleFunc("POST /api/v1/metrics/requests", s.handleRecordRequest)
	mux.HandleFunc("POST /api/v1/metrics/gpu", s.handleRecordGPU)
	mux.HandleFunc("GET /metrics", s.handlePrometheusMetrics)

	// 查询
	mux.HandleFunc("GET /api/v1/deployments/{id}/metrics", s.handleDeploymentMetrics)
	mux.HandleFunc("GET /api/v1/gpus/metrics", s.handleGPUMetrics)
	mux.HandleFunc("PUT /internal/rate-cards/{tenantId}", s.handleSetRateCard)
	mux.HandleFunc("GET /internal/billing", s.handleBilling)

	return middleware.WithRequestID(
		middleware.Recover(s.logger,
			middleware.AccessLog(s.logger, mux),
		),
	)
}

func (s *Server) handleSetRateCard(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "账本存储未配置"))
		return
	}
	var card usage.RateCard
	if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	card.TenantID = r.PathValue("tenantId")
	if err := s.usage.SetRateCard(r.Context(), card); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrBadRequest, "费率无效", err))
		return
	}
	apitypes.WriteResult(w, r, card, nil)
}

func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "账本存储未配置"))
		return
	}
	tenant := r.URL.Query().Get("tenantId")
	if tenant == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "tenantId 必填"))
		return
	}
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -30)
	to := now
	if v := r.URL.Query().Get("from"); v != "" {
		if parsed, err := time.Parse("2006-01-02", v); err == nil {
			from = parsed
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if parsed, err := time.Parse("2006-01-02", v); err == nil {
			to = parsed
		}
	}
	rows, err := s.usage.ListDaily(r.Context(), tenant, from, to)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询账单失败", err))
		return
	}
	apitypes.WriteResult(w, r, rows, nil)
}

// ---- 上报 ----

type recordRequestReq struct {
	TenantID       string  `json:"tenantId"`
	DeploymentID   string  `json:"deploymentId"`
	Model          string  `json:"model"`
	Pod            string  `json:"pod"`
	LatencyMs      int64   `json:"latencyMs"`
	TTFTMs         int64   `json:"ttftMs"`
	Tokens         int     `json:"tokens"`
	InputTokens    int     `json:"inputTokens"`
	OutputTokens   int     `json:"outputTokens"`
	TPOTMs         float64 `json:"tpotMs"`
	QueueLength    float64 `json:"queueLength"`
	KVCacheUsage   float64 `json:"kvCacheUsage"`
	GPUMemoryBytes float64 `json:"gpuMemoryBytes"`
	Err            bool    `json:"err"`
}

func (s *Server) handleRecordRequest(w http.ResponseWriter, r *http.Request) {
	var req recordRequestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	if req.DeploymentID == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "deploymentId 必填"))
		return
	}
	s.store.RecordRequest(metrics.Sample{
		Ts:           time.Now(),
		TenantID:     req.TenantID,
		DeploymentID: req.DeploymentID,
		Model:        req.Model,
		Pod:          req.Pod,
		LatencyMs:    req.LatencyMs,
		TTFTMs:       req.TTFTMs,
		Tokens:       req.InputTokens + req.OutputTokens,
		InputTokens:  req.InputTokens, OutputTokens: req.OutputTokens, TPOTMs: req.TPOTMs, QueueLength: req.QueueLength, KVCacheUsage: req.KVCacheUsage, GPUMemoryBytes: req.GPUMemoryBytes,
		Err: req.Err,
	})
	if s.usage != nil {
		if err := s.usage.Record(r.Context(), usage.Record{TenantID: req.TenantID, DeploymentID: req.DeploymentID, ModelID: req.Model, InputTokens: req.InputTokens, OutputTokens: req.OutputTokens, LatencyMs: req.LatencyMs, Failed: req.Err, CreatedAt: time.Now()}); err != nil {
			s.logger.Warn("写入用量账本失败", "err", err)
		}
	}
	apitypes.WriteResult(w, r, map[string]string{"status": "ok"}, nil)
}

func (s *Server) handlePrometheusMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = w.Write([]byte(s.store.PrometheusText()))
}

type recordGPUReq struct {
	NodeName    string  `json:"nodeName"`
	GPUType     string  `json:"gpuType"`
	Utilization float64 `json:"utilization"` // 0-100
	Used        int32   `json:"used"`
	Total       int32   `json:"total"`
}

func (s *Server) handleRecordGPU(w http.ResponseWriter, r *http.Request) {
	var req recordGPUReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	s.store.RecordGPU(metrics.GPUSample{
		Ts:          time.Now(),
		NodeName:    req.NodeName,
		GPUType:     req.GPUType,
		Utilization: req.Utilization,
		Used:        req.Used,
		Total:       req.Total,
	})
	apitypes.WriteResult(w, r, map[string]string{"status": "ok"}, nil)
}

// ---- 查询 ----

// handleDeploymentMetrics 查询某部署的请求指标序列。
func (s *Server) handleDeploymentMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rg := metrics.ParseRange(r.URL.Query().Get("range"), time.Now())
	if s.prom != nil {
		if series, err := s.prometheusDeploymentSeries(r.Context(), id, rg); err == nil {
			rangeName := r.URL.Query().Get("range")
			if rangeName == "" {
				rangeName = "1h"
			}
			apitypes.WriteResult(w, r, apitypes.MetricsView{DeploymentID: id, Range: rangeName, Series: series}, nil)
			return
		} else {
			s.logger.Warn("Prometheus 部署指标查询失败，降级内存", "deploymentId", id, "err", err)
		}
	}
	series := s.store.DeploymentMetrics(id, rg)

	view := apitypes.MetricsView{
		DeploymentID: id,
		Range:        r.URL.Query().Get("range"),
	}
	if r.URL.Query().Get("range") == "" {
		view.Range = "1h"
	}
	if len(series.Buckets) == 0 {
		view.Series = []apitypes.MetricSeries{}
		apitypes.WriteResult(w, r, view, nil)
		return
	}

	view.Series = []apitypes.MetricSeries{
		{Name: "requests", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return float64(b.Requests) })},
		{Name: "errorRate", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.ErrorRate() })},
		{Name: "ttftMs", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.AvgTTFT() })},
		{Name: "tokensPerSec", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.TokensPerSec(series.BucketSecs) })},
		{Name: "tpotMs", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 {
			if b.Requests == 0 {
				return 0
			}
			return b.TPOTSum / float64(b.Requests)
		})},
		{Name: "queueLength", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.QueueMax })},
		{Name: "kvCacheUsage", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.KVCacheMax })},
		{Name: "gpuMemoryBytes", Points: seriesPoints(series.Buckets, func(b metrics.Bucket) float64 { return b.GPUMemoryMax })},
	}
	apitypes.WriteResult(w, r, view, nil)
}

func (s *Server) prometheusDeploymentSeries(ctx context.Context, deploymentID string, rg metrics.Range) ([]apitypes.MetricSeries, error) {
	id := strings.ReplaceAll(strings.ReplaceAll(deploymentID, `\`, `\\`), `"`, `\"`)
	selector := fmt.Sprintf(`deployment_id="%s"`, id)
	queries := []struct{ name, query string }{
		{"requests", fmt.Sprintf(`sum(increase(carrot_inference_requests_total{%s}[1m]))`, selector)},
		{"errorRate", fmt.Sprintf(`100 * sum(rate(carrot_inference_errors_total{%s}[5m])) / clamp_min(sum(rate(carrot_inference_requests_total{%s}[5m])), 0.001)`, selector, selector)},
		{"ttftMs", fmt.Sprintf(`avg(carrot_inference_ttft_ms{%s})`, selector)},
		{"tokensPerSec", fmt.Sprintf(`sum(rate(carrot_inference_input_tokens_total{%s}[5m])) + sum(rate(carrot_inference_output_tokens_total{%s}[5m]))`, selector, selector)},
		{"tpotMs", fmt.Sprintf(`avg(carrot_inference_tpot_ms{%s})`, selector)},
		{"queueLength", fmt.Sprintf(`max(carrot_inference_queue_length{%s})`, selector)},
		{"kvCacheUsage", fmt.Sprintf(`max(carrot_inference_kv_cache_usage_ratio{%s})`, selector)},
		{"gpuMemoryBytes", fmt.Sprintf(`max(carrot_inference_gpu_memory_bytes{%s})`, selector)},
	}
	out := make([]apitypes.MetricSeries, 0, len(queries))
	for _, item := range queries {
		res, err := s.prom.QueryRange(ctx, item.query, rg.From, rg.To, time.Minute)
		if err != nil {
			return nil, err
		}
		points := make([]apitypes.MetricPoint, 0)
		if len(res.Data.Result) > 0 {
			for _, value := range res.Data.Result[0].Values {
				if len(value) != 2 {
					continue
				}
				ts, ok := value[0].(float64)
				if !ok {
					continue
				}
				val, err := parseFloat(value[1])
				if err == nil {
					points = append(points, apitypes.MetricPoint{Ts: int64(ts), Val: val})
				}
			}
		}
		out = append(out, apitypes.MetricSeries{Name: item.name, Points: points})
	}
	return out, nil
}

// handleGPUMetrics 查询 GPU 利用率序列（按节点）。
// R2-3：配置 Prometheus 时用 DCGM 指标（gpu_utilization），否则降级内存。
func (s *Server) handleGPUMetrics(w http.ResponseWriter, r *http.Request) {
	rangeStr := r.URL.Query().Get("range")
	rg := metrics.ParseRange(rangeStr, time.Now())
	if rangeStr == "" {
		rangeStr = "1h"
	}
	// Prometheus 模式：DCGM 指标
	if s.prom != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		res, err := s.prom.QueryRange(ctx,
			`avg by (instance) (dcgm_gpu_utilization)`,
			rg.From, rg.To, time.Minute)
		if err != nil {
			s.logger.Warn("Prometheus GPU 查询失败，降级内存", "err", err)
		} else {
			var pts []apitypes.MetricPoint
			if len(res.Data.Result) > 0 {
				for _, v := range res.Data.Result[0].Values {
					if len(v) == 2 {
						if ts, ok := v[0].(float64); ok {
							if val, err := parseFloat(v[1]); err == nil {
								pts = append(pts, apitypes.MetricPoint{Ts: int64(ts), Val: val})
							}
						}
					}
				}
			}
			apitypes.WriteResult(w, r, apitypes.MetricsView{
				DeploymentID: "gpu",
				Range:        rangeStr,
				Series:       []apitypes.MetricSeries{{Name: "gpuUtil", Points: pts}},
			}, nil)
			return
		}
	}
	// 内存降级
	gs := s.store.GPUMetrics(rg)
	buckets := gs.AvgGPUUtil()
	view := apitypes.MetricsView{
		DeploymentID: "gpu",
		Range:        rangeStr,
	}
	if len(buckets) == 0 {
		view.Series = []apitypes.MetricSeries{}
		apitypes.WriteResult(w, r, view, nil)
		return
	}
	view.Series = []apitypes.MetricSeries{
		{Name: "gpuUtil", Points: seriesPoints(buckets, func(b metrics.Bucket) float64 { return float64(b.LatencySum) })},
	}
	apitypes.WriteResult(w, r, view, nil)
}

// parseFloat 解析 Prometheus 字符串数值
func parseFloat(v interface{}) (float64, error) {
	switch val := v.(type) {
	case string:
		var f float64
		_, err := fmt.Sscanf(val, "%f", &f)
		return f, err
	case float64:
		return val, nil
	default:
		return 0, fmt.Errorf("unexpected value type: %T", v)
	}
}

func seriesPoints(buckets []metrics.Bucket, f func(metrics.Bucket) float64) []apitypes.MetricPoint {
	pts := make([]apitypes.MetricPoint, 0, len(buckets))
	for _, b := range buckets {
		pts = append(pts, apitypes.MetricPoint{Ts: b.Ts, Val: f(b)})
	}
	return pts
}
