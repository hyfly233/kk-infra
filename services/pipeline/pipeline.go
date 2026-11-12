// Package pipeline 模型发布流水线接口定义（R4 扩展点预留）。
//
// 本包只定义接口与领域模型，不实现具体执行逻辑。
// 流水线阶段（对齐 PRD-V2 §4 模型与发布）：
//
//	Artifact 校验 → 启动探针 → 基准测试 → 人工确认 → 发布（RELEASED）
//
// 当前平台已实现：版本状态机（REGISTERED → VALIDATED → RELEASED）、
// vLLM 启动探针、RELEASED 门禁。基准测试/灰度权重为后续实现。
package pipeline

import "time"

// Stage 流水线阶段
type Stage string

const (
	StageArtifactValidate Stage = "artifact_validate" // artifact 校验（权重完整性/格式）
	StageProbe            Stage = "probe"             // 启动探针（readiness 通过）
	StageBenchmark        Stage = "benchmark"         // 基准测试（TTFT/Token/s）
	StageApproval         Stage = "approval"          // 人工确认
	StageRelease          Stage = "release"           // 发布（RELEASED）
)

// RunResult 流水线单阶段执行结果
type RunResult struct {
	Stage      Stage  `json:"stage"`
	Status     string `json:"status"` // pending / running / passed / failed / skipped
	Message    string `json:"message"`
	DurationMs int64  `json:"durationMs"`
}

// BenchmarkResult 基准测试结果（版本发布记录用）
type BenchmarkResult struct {
	ModelVersionID string  `json:"modelVersionId"`
	TTFTMs         float64 `json:"ttftMs"`       // 平均首 Token 延迟
	TokensPerSec   float64 `json:"tokensPerSec"` // 吞吐
	Requests       int64   `json:"requests"`     // 测试请求数
	ErrorRate      float64 `json:"errorRate"`    // 错误率 %
}

// ReleaseRecord 发布记录（持久化到 model_versions 状态 + audit）
type ReleaseRecord struct {
	ID              string           `json:"id"`
	ModelVersionID  string           `json:"modelVersionId"`
	TenantID        string           `json:"tenantId,omitempty"`
	Operator        string           `json:"operator"` // 操作者
	Status          string           `json:"status"`
	StageResults    []RunResult      `json:"stageResults"`
	Benchmark       *BenchmarkResult `json:"benchmark,omitempty"`
	ApprovedBy      string           `json:"approvedBy,omitempty"`
	ApprovalMessage string           `json:"approvalMessage,omitempty"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
	ReleasedAt      *time.Time       `json:"releasedAt,omitempty"`
}

// Pipeline 发布流水线接口。
// 实现方可以是内部执行器（当前）或外部编排服务（后续）。
type Pipeline interface {
	// Run 对模型版本执行完整发布流水线，返回各阶段结果。
	Run(modelVersionID, operator string) (*ReleaseRecord, error)
	// ValidateArtifact 校验权重 artifact 完整性（当前返回占位通过）。
	ValidateArtifact(modelVersionID string) (*RunResult, error)
	// RunBenchmark 执行基准测试（当前返回占位结果）。
	RunBenchmark(modelVersionID string) (*BenchmarkResult, error)
}
