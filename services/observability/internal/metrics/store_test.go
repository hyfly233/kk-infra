package metrics

import (
	"strings"
	"testing"
	"time"
)

func TestRecordAndQueryDeployment(t *testing.T) {
	s := NewStore(0)
	now := time.Now()

	// 记录 3 条请求：2 成功 1 失败
	s.RecordRequest(Sample{Ts: now, DeploymentID: "dep-1", Model: "qwen", LatencyMs: 100, TTFTMs: 30, Tokens: 50})
	s.RecordRequest(Sample{Ts: now, DeploymentID: "dep-1", Model: "qwen", LatencyMs: 200, TTFTMs: 60, Tokens: 100})
	s.RecordRequest(Sample{Ts: now, DeploymentID: "dep-1", Model: "qwen", LatencyMs: 300, TTFTMs: 90, Tokens: 150, Err: true})
	// 其他部署不干扰
	s.RecordRequest(Sample{Ts: now, DeploymentID: "dep-2", Model: "llama", LatencyMs: 999, TTFTMs: 999, Tokens: 999})

	rg := Range{From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	ser := s.DeploymentMetrics("dep-1", rg)

	if len(ser.Buckets) != 1 {
		t.Fatalf("期望 1 个桶，得到 %d", len(ser.Buckets))
	}
	b := ser.Buckets[0]
	if b.Requests != 3 {
		t.Errorf("requests = %d, 期望 3", b.Requests)
	}
	if b.Errors != 1 {
		t.Errorf("errors = %d, 期望 1", b.Errors)
	}
	if got := b.ErrorRate(); got != 100.0/3.0 {
		t.Errorf("errorRate = %v, 期望 %v", got, 100.0/3.0)
	}
	if got := b.AvgLatency(); got != 200 {
		t.Errorf("avgLatency = %v, 期望 200", got)
	}
	if got := b.AvgTTFT(); got != 60 {
		t.Errorf("avgTTFT = %v, 期望 60", got)
	}
	if got := b.TokensPerSec(60); got != 5 {
		t.Errorf("tokensPerSec = %v, 期望 5", got)
	}
}

func TestPrometheusTextIncludesTenantDimensionsAndGuards(t *testing.T) {
	s := NewStore(0)
	s.RecordRequest(Sample{Ts: time.Now(), TenantID: "tenant-a", DeploymentID: "dep-1", Model: "qwen", InputTokens: 10, OutputTokens: 20, Tokens: 30, TTFTMs: 45, TPOTMs: 8.5, QueueLength: 3, KVCacheUsage: .7, GPUMemoryBytes: 1024})
	text := s.PrometheusText()
	for _, want := range []string{`tenant_id="tenant-a",model_id="qwen",deployment_id="dep-1"`, `carrot_inference_requests_total`, `carrot_inference_queue_length`, `carrot_inference_ttft_ms`, `carrot_inference_tpot_ms`, `carrot_inference_kv_cache_usage_ratio`, `carrot_inference_gpu_memory_bytes`} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestRangeFilter(t *testing.T) {
	s := NewStore(0)
	now := time.Now()

	s.RecordRequest(Sample{Ts: now.Add(-2 * time.Hour), DeploymentID: "dep-1", LatencyMs: 1, TTFTMs: 1, Tokens: 1})
	s.RecordRequest(Sample{Ts: now.Add(-time.Minute), DeploymentID: "dep-1", LatencyMs: 1, TTFTMs: 1, Tokens: 1})

	rg := Range{From: now.Add(-30 * time.Minute), To: now}
	ser := s.DeploymentMetrics("dep-1", rg)
	if len(ser.Buckets) != 1 {
		t.Fatalf("期望 1 个桶（范围外被过滤），得到 %d", len(ser.Buckets))
	}
}

func TestGPUMetrics(t *testing.T) {
	s := NewStore(0)
	now := time.Now()

	s.RecordGPU(GPUSample{Ts: now, NodeName: "gpu-001", GPUType: "A100", Utilization: 50})
	s.RecordGPU(GPUSample{Ts: now, NodeName: "gpu-001", GPUType: "A100", Utilization: 70})

	rg := Range{From: now.Add(-time.Minute), To: now.Add(time.Minute)}
	gs := s.GPUMetrics(rg)
	if len(gs.Buckets) != 1 {
		t.Fatalf("期望 1 个桶，得到 %d", len(gs.Buckets))
	}
	// LatencySum 存放 Utilization*100 的累加（50+70=120 → /2/100 = 0.6）
	b := gs.AvgGPUUtil()[0]
	if got := float64(b.LatencySum); got != 60 {
		t.Errorf("平均利用率 = %v, 期望 60", got)
	}
}

func TestParseRange(t *testing.T) {
	now := time.Now()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"5m", 5 * time.Minute},
		{"15m", 15 * time.Minute},
		{"30m", 30 * time.Minute},
		{"1h", time.Hour},
		{"6h", 6 * time.Hour},
		{"24h", 24 * time.Hour},
		{"", time.Hour},
		{"bogus", time.Hour},
	}
	for _, c := range cases {
		rg := ParseRange(c.in, now)
		if got := rg.To.Sub(rg.From); got != c.want {
			t.Errorf("ParseRange(%q) = %v, 期望 %v", c.in, got, c.want)
		}
	}
}
