package client

import (
	"fmt"

	"kk-infra/lib/domain"
)

type RuntimeContainer struct {
	Name        string
	Image       string
	Command     []string
	Args        []string
	Port        int32
	HealthPath  string
	MetricsPath string
}

// RuntimeDriver owns runtime-specific process arguments, probes and metric names.
type RuntimeDriver interface {
	Runtime() string
	Build(modelPath string, startupArgs []string) RuntimeContainer
	MetricMap() map[string]string
}

type vllmDriver struct{}

func (vllmDriver) Runtime() string { return domain.RuntimeVLLM }
func (vllmDriver) Build(modelPath string, startupArgs []string) RuntimeContainer {
	args := []string{"--model", modelPath, "--host", "0.0.0.0", "--port", "8000"}
	return RuntimeContainer{Name: "vllm", Image: "vllm/vllm-openai:v0.11.1", Args: append(args, startupArgs...), Port: 8000, HealthPath: "/health", MetricsPath: "/metrics"}
}
func (vllmDriver) MetricMap() map[string]string {
	return map[string]string{"ttft": "vllm:time_to_first_token_seconds", "tpot": "vllm:time_per_output_token_seconds", "queue": "vllm:num_requests_waiting", "kv_cache": "vllm:gpu_cache_usage_perc"}
}

type tritonDriver struct{}

func (tritonDriver) Runtime() string { return domain.RuntimeTriton }
func (tritonDriver) Build(modelPath string, startupArgs []string) RuntimeContainer {
	args := []string{"--model-repository=" + modelPath, "--http-port=8000"}
	return RuntimeContainer{Name: "triton", Image: "nvcr.io/nvidia/tritonserver:25.01-py3", Command: []string{"tritonserver"}, Args: append(args, startupArgs...), Port: 8000, HealthPath: "/v2/health/ready", MetricsPath: "/metrics"}
}
func (tritonDriver) MetricMap() map[string]string {
	return map[string]string{"requests": "nv_inference_request_success", "errors": "nv_inference_request_failure", "queue": "nv_inference_pending_request_count", "latency": "nv_inference_request_duration_us"}
}

type tensorRTLLMDriver struct{}

func (tensorRTLLMDriver) Runtime() string { return domain.RuntimeTensorRTLLM }
func (tensorRTLLMDriver) Build(modelPath string, startupArgs []string) RuntimeContainer {
	args := []string{"--model-repository=" + modelPath, "--http-port=8000", "--backend-config=tensorrtllm,default-max-batch-size=8"}
	return RuntimeContainer{Name: "tensorrt-llm", Image: "nvcr.io/nvidia/tritonserver:25.01-trtllm-python-py3", Command: []string{"tritonserver"}, Args: append(args, startupArgs...), Port: 8000, HealthPath: "/v2/health/ready", MetricsPath: "/metrics"}
}
func (tensorRTLLMDriver) MetricMap() map[string]string {
	return map[string]string{"requests": "nv_inference_request_success", "errors": "nv_inference_request_failure", "queue": "nv_trt_llm_request_metrics", "kv_cache": "nv_trt_llm_kv_cache_block_metrics"}
}

func runtimeDriver(runtime string) (RuntimeDriver, error) {
	switch runtime {
	case "", domain.RuntimeVLLM:
		return vllmDriver{}, nil
	case domain.RuntimeTriton:
		return tritonDriver{}, nil
	case domain.RuntimeTensorRTLLM:
		return tensorRTLLMDriver{}, nil
	default:
		return nil, fmt.Errorf("不支持的运行时: %s", runtime)
	}
}
