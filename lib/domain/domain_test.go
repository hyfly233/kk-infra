package domain

import "testing"

// 状态机合法/非法转换测试
func TestStateMachine_ValidTransitions(t *testing.T) {
	sm := NewDeploymentStateMachine()

	cases := []struct {
		from string
		to   string
		ok   bool
	}{
		{DeploymentStatusNew, DeploymentStatusValidating, true},
		{DeploymentStatusValidating, DeploymentStatusSubmitting, true},
		{DeploymentStatusSubmitting, DeploymentStatusStarting, true},
		{DeploymentStatusStarting, DeploymentStatusRunning, true},
		{DeploymentStatusRunning, DeploymentStatusScaling, true},
		{DeploymentStatusScaling, DeploymentStatusRunning, true},
		{DeploymentStatusRunning, DeploymentStatusRestarting, true},
		{DeploymentStatusRestarting, DeploymentStatusRunning, true},
		{DeploymentStatusRunning, DeploymentStatusDeleting, true},
		{DeploymentStatusDeleting, DeploymentStatusDeleted, true},
		// 非法转换
		{DeploymentStatusNew, DeploymentStatusRunning, false},
		{DeploymentStatusNew, DeploymentStatusDeleted, false},
		{DeploymentStatusRunning, DeploymentStatusValidating, false},
		{DeploymentStatusStarting, DeploymentStatusSubmitting, false},
		{DeploymentStatusDeleted, DeploymentStatusRunning, false},
		{DeploymentStatusDeleted, DeploymentStatusDeleting, false},
		// FAILED 重试路径
		{DeploymentStatusStarting, DeploymentStatusFailed, true},
		{DeploymentStatusFailed, DeploymentStatusSubmitting, true},
		{DeploymentStatusFailed, DeploymentStatusDeleting, true},
		// 任意状态可终止删除
		{DeploymentStatusValidating, DeploymentStatusDeleting, true},
		{DeploymentStatusFailed, DeploymentStatusDeleting, true},
	}

	for _, c := range cases {
		got := sm.CanTransition(c.from, c.to)
		if got != c.ok {
			t.Errorf("CanTransition(%s→%s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

// 状态机错误信息
func TestStateMachine_TransitionError(t *testing.T) {
	sm := NewDeploymentStateMachine()
	if err := sm.Transition(DeploymentStatusNew, DeploymentStatusRunning); err == nil {
		t.Fatal("期望非法转换报错")
	}
	if err := sm.Transition(DeploymentStatusRunning, DeploymentStatusScaling); err != nil {
		t.Fatalf("合法转换不应报错: %v", err)
	}
}

// 模型版本可部署性
func TestModelVersion_Deployable(t *testing.T) {
	ok := &ModelVersion{
		Status:      ModelStatusReleased,
		Runtime:     RuntimeVLLM,
		ArtifactURI: "s3://bucket/qwen",
		GPUType:     "A100",
		GPUCount:    1,
	}
	if !ok.Deployable() {
		t.Fatal("RELEASED 版本应可部署")
	}
	cases := []*ModelVersion{
		{Status: ModelStatusRegistered, Runtime: RuntimeVLLM, ArtifactURI: "s3://x", GPUType: "A100", GPUCount: 1},
		{Status: ModelStatusValidated, Runtime: RuntimeVLLM, ArtifactURI: "s3://x", GPUType: "A100", GPUCount: 1},
		{Status: ModelStatusReleased, Runtime: "unsupported", ArtifactURI: "s3://x", GPUType: "A100", GPUCount: 1},
		{Status: ModelStatusReleased, Runtime: RuntimeVLLM, ArtifactURI: "", GPUType: "A100", GPUCount: 1},
		{Status: ModelStatusReleased, Runtime: RuntimeVLLM, ArtifactURI: "s3://x", GPUType: "", GPUCount: 1},
		{Status: ModelStatusReleased, Runtime: RuntimeVLLM, ArtifactURI: "s3://x", GPUType: "A100", GPUCount: 0},
	}
	for i, c := range cases {
		if c.Deployable() {
			t.Errorf("case %d 不应可部署: %+v", i, c)
		}
	}
	for _, runtime := range []string{RuntimeVLLM, RuntimeTriton, RuntimeTensorRTLLM} {
		candidate := *ok
		candidate.Runtime = runtime
		if !candidate.Deployable() {
			t.Errorf("runtime %s 应可部署", runtime)
		}
	}
}

// 版本状态机：REGISTERED → VALIDATED → RELEASED
func TestVersionStateMachine(t *testing.T) {
	cases := []struct {
		from, to string
		ok       bool
	}{
		{ModelStatusRegistered, ModelStatusValidating, true},
		{ModelStatusRegistered, ModelStatusValidated, true},
		{ModelStatusValidated, ModelStatusReleased, true},
		{ModelStatusReleased, ModelStatusUnavailable, true},
		// 非法
		{ModelStatusRegistered, ModelStatusReleased, false},
		{ModelStatusValidating, ModelStatusReleased, false},
		{ModelStatusReleased, ModelStatusValidated, false},
		{ModelStatusUnavailable, ModelStatusValidated, false},
	}
	for _, c := range cases {
		if got := CanTransitionVersion(c.from, c.to); got != c.ok {
			t.Errorf("CanTransitionVersion(%s→%s) = %v, want %v", c.from, c.to, got, c.ok)
		}
	}
}

// GPU 资源可用数
func TestGPUResource_Available(t *testing.T) {
	g := &GPUResource{Allocatable: 8, Used: 3}
	if g.Available() != 5 {
		t.Fatalf("Available = %d, want 5", g.Available())
	}
	g = &GPUResource{Allocatable: 2, Used: 5}
	if g.Available() != 0 {
		t.Fatalf("超卖时应返回 0")
	}
}

// 租户配额剩余
func TestTenantQuota_Available(t *testing.T) {
	q := &TenantQuota{Quota: 8, Used: 3}
	if q.Available() != 5 {
		t.Fatalf("Available = %d, want 5", q.Available())
	}
}
