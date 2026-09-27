// Package metrics 内存指标存储与聚合（MVP）。
// 提供请求指标（延迟/TTFT/Token/错误）与 GPU 利用率指标的采样、聚合和查询，
// 预留 Prometheus adapter 接口（后续轮替换存储后端）。
package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sample 一条请求指标采样
type Sample struct {
	Ts             time.Time
	TenantID       string
	DeploymentID   string
	Model          string
	Pod            string
	LatencyMs      int64
	TTFTMs         int64
	Tokens         int
	InputTokens    int
	OutputTokens   int
	TPOTMs         float64
	QueueLength    float64
	KVCacheUsage   float64
	GPUMemoryBytes float64
	Err            bool
}

// GPUSample 一条 GPU 利用率采样（节点 × 型号粒度）
type GPUSample struct {
	Ts          time.Time
	NodeName    string
	GPUType     string
	Utilization float64 // 0-100
	Used        int32
	Total       int32
}

// Store 内存指标存储。线程安全。
// 保留窗口：请求指标 2h、GPU 指标 2h，超窗采样惰性清理。
type Store struct {
	mu        sync.RWMutex
	reqs      []Sample
	gpus      []GPUSample
	keepSince time.Time
	totals    map[string]*requestTotal
}

type requestTotal struct {
	TenantID, DeploymentID, Model, Pod          string
	Requests, Errors, InputTokens, OutputTokens int64
	Latest                                      Sample
}

// NewStore 创建指标存储，retention 为保留窗口（默认 2h）。
func NewStore(retention time.Duration) *Store {
	if retention <= 0 {
		retention = 2 * time.Hour
	}
	return &Store{keepSince: time.Now().Add(-retention), totals: make(map[string]*requestTotal)}
}

// RecordRequest 记录一条请求指标。
func (s *Store) RecordRequest(sm Sample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, sm)
	key := sm.TenantID + "\x00" + sm.DeploymentID + "\x00" + sm.Model + "\x00" + sm.Pod
	total := s.totals[key]
	if total == nil {
		total = &requestTotal{TenantID: sm.TenantID, DeploymentID: sm.DeploymentID, Model: sm.Model, Pod: sm.Pod}
		s.totals[key] = total
	}
	total.Requests++
	if sm.Err {
		total.Errors++
	}
	total.InputTokens += int64(sm.InputTokens)
	total.OutputTokens += int64(sm.OutputTokens)
	total.Latest = sm
	s.gcLocked(time.Now())
}

// PrometheusText exposes request counters and autoscaling/guard gauges.
func (s *Store) PrometheusText() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.totals))
	for key := range s.totals {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# TYPE carrot_inference_requests_total counter\n# TYPE carrot_inference_errors_total counter\n# TYPE carrot_inference_input_tokens_total counter\n# TYPE carrot_inference_output_tokens_total counter\n")
	b.WriteString("# TYPE carrot_inference_queue_length gauge\n# TYPE carrot_inference_ttft_ms gauge\n# TYPE carrot_inference_tpot_ms gauge\n# TYPE carrot_inference_kv_cache_usage_ratio gauge\n# TYPE carrot_inference_gpu_memory_bytes gauge\n")
	for _, key := range keys {
		t := s.totals[key]
		labels := fmt.Sprintf(`tenant_id="%s",model_id="%s",deployment_id="%s",pod="%s"`, escapeLabel(t.TenantID), escapeLabel(t.Model), escapeLabel(t.DeploymentID), escapeLabel(t.Pod))
		fmt.Fprintf(&b, "carrot_inference_requests_total{%s} %d\ncarrot_inference_errors_total{%s} %d\ncarrot_inference_input_tokens_total{%s} %d\ncarrot_inference_output_tokens_total{%s} %d\n", labels, t.Requests, labels, t.Errors, labels, t.InputTokens, labels, t.OutputTokens)
		fmt.Fprintf(&b, "carrot_inference_queue_length{%s} %g\ncarrot_inference_ttft_ms{%s} %d\ncarrot_inference_tpot_ms{%s} %g\ncarrot_inference_kv_cache_usage_ratio{%s} %g\ncarrot_inference_gpu_memory_bytes{%s} %g\n", labels, t.Latest.QueueLength, labels, t.Latest.TTFTMs, labels, t.Latest.TPOTMs, labels, t.Latest.KVCacheUsage, labels, t.Latest.GPUMemoryBytes)
	}
	return b.String()
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	return strings.ReplaceAll(v, "\"", "\\\"")
}

// RecordGPU 记录一条 GPU 利用率采样。
func (s *Store) RecordGPU(g GPUSample) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gpus = append(s.gpus, g)
	s.gcLocked(time.Now())
}

// gcLocked 惰性清理窗口外采样。
func (s *Store) gcLocked(now time.Time) {
	if now.Sub(s.keepSince) < time.Minute {
		return
	}
	s.keepSince = now
	cut := now.Add(-2 * time.Hour)
	reqs := s.reqs[:0]
	for _, r := range s.reqs {
		if r.Ts.After(cut) {
			reqs = append(reqs, r)
		}
	}
	s.reqs = reqs
	gpus := s.gpus[:0]
	for _, g := range s.gpus {
		if g.Ts.After(cut) {
			gpus = append(gpus, g)
		}
	}
	s.gpus = gpus
}

// Range 时间范围
type Range struct {
	From time.Time
	To   time.Time
}

// ParseRange 解析查询范围：1h/30m/5m 等，默认 1h。
func ParseRange(r string, now time.Time) Range {
	d := time.Hour
	switch {
	case r == "5m":
		d = 5 * time.Minute
	case r == "15m":
		d = 15 * time.Minute
	case r == "30m":
		d = 30 * time.Minute
	case r == "6h":
		d = 6 * time.Hour
	case r == "24h":
		d = 24 * time.Hour
	}
	return Range{From: now.Add(-d), To: now}
}

// Bucket 时间桶（聚合点）
type Bucket struct {
	Ts           int64 // 桶起始 unix 秒
	Requests     int64
	Errors       int64
	LatencySum   int64
	TTFTSum      int64
	Tokens       int64
	TPOTSum      float64
	QueueMax     float64
	KVCacheMax   float64
	GPUMemoryMax float64
}

// ErrorRate 错误率（%）
func (b Bucket) ErrorRate() float64 {
	if b.Requests == 0 {
		return 0
	}
	return float64(b.Errors) * 100 / float64(b.Requests)
}

// AvgLatency 平均延迟 ms
func (b Bucket) AvgLatency() float64 {
	if b.Requests == 0 {
		return 0
	}
	return float64(b.LatencySum) / float64(b.Requests)
}

// AvgTTFT 平均首 Token 延迟 ms
func (b Bucket) AvgTTFT() float64 {
	if b.Requests == 0 {
		return 0
	}
	return float64(b.TTFTSum) / float64(b.Requests)
}

// TokensPerSec 每秒 Token 数（桶内累计/桶秒数）
func (b Bucket) TokensPerSec(bucketSecs int64) float64 {
	if bucketSecs <= 0 || b.Tokens == 0 {
		return 0
	}
	return float64(b.Tokens) / float64(bucketSecs)
}

// DeploymentSeries 按部署聚合的请求指标序列
type DeploymentSeries struct {
	DeploymentID string
	Buckets      []Bucket
	BucketSecs   int64
}

// DeploymentMetrics 按部署查询请求指标。
func (s *Store) DeploymentMetrics(deploymentID string, r Range) DeploymentSeries {
	s.mu.RLock()
	defer s.mu.RUnlock()
	step, secs := stepFor(r)
	idx := make(map[int64]int)
	var buckets []Bucket
	for _, sm := range s.reqs {
		if sm.DeploymentID != deploymentID || !inRange(sm.Ts, r) {
			continue
		}
		k := sm.Ts.Unix() / step * step
		i, ok := idx[k]
		if !ok {
			i = len(buckets)
			idx[k] = i
			buckets = append(buckets, Bucket{Ts: k})
		}
		b := &buckets[i]
		b.Requests++
		if sm.Err {
			b.Errors++
		}
		b.LatencySum += sm.LatencyMs
		b.TTFTSum += sm.TTFTMs
		b.Tokens += int64(sm.Tokens)
		b.TPOTSum += sm.TPOTMs
		if sm.QueueLength > b.QueueMax {
			b.QueueMax = sm.QueueLength
		}
		if sm.KVCacheUsage > b.KVCacheMax {
			b.KVCacheMax = sm.KVCacheUsage
		}
		if sm.GPUMemoryBytes > b.GPUMemoryMax {
			b.GPUMemoryMax = sm.GPUMemoryBytes
		}
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Ts < buckets[j].Ts })
	return DeploymentSeries{DeploymentID: deploymentID, Buckets: buckets, BucketSecs: secs}
}

// GPUSeries GPU 利用率序列（按节点聚合）
type GPUSeries struct {
	NodeName string
	Buckets  []Bucket
}

// GPUMetrics 查询 GPU 利用率（%）。
func (s *Store) GPUMetrics(r Range) GPUSeries {
	s.mu.RLock()
	defer s.mu.RUnlock()
	step, _ := stepFor(r)
	idx := make(map[int64]int)
	var buckets []Bucket
	for _, g := range s.gpus {
		if !inRange(g.Ts, r) {
			continue
		}
		k := g.Ts.Unix() / step * step
		i, ok := idx[k]
		if !ok {
			i = len(buckets)
			idx[k] = i
			buckets = append(buckets, Bucket{Ts: k})
		}
		b := &buckets[i]
		b.Requests++                               // 复用为采样数
		b.LatencySum += int64(g.Utilization * 100) // 存放大 100 倍，避免浮点误差
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Ts < buckets[j].Ts })
	return GPUSeries{Buckets: buckets}
}

// AvgGPUUtil 返回 GPUSeries 每桶平均利用率（%）。
func (g GPUSeries) AvgGPUUtil() []Bucket {
	out := make([]Bucket, 0, len(g.Buckets))
	for _, b := range g.Buckets {
		b2 := b
		if b2.Requests > 0 {
			b2.LatencySum = b2.LatencySum / b2.Requests / 100
		}
		out = append(out, b2)
	}
	return out
}

// stepFor 根据范围选择聚合步长：5m→10s，其余 60s。
func stepFor(r Range) (int64, int64) {
	d := r.To.Sub(r.From)
	if d <= 5*time.Minute {
		return 10, 10
	}
	return 60, 60
}

func inRange(t time.Time, r Range) bool {
	return !t.Before(r.From) && !t.After(r.To)
}
