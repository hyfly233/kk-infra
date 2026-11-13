// Package domain 定义 Carrot AI Infra 核心领域模型。
// 领域逻辑不依赖任何外部框架与 Kubernetes SDK，保证可独立测试。
package domain

import "time"

// 模型运行时类型。
const (
	RuntimeVLLM        = "vLLM"
	RuntimeTriton      = "Triton"
	RuntimeTensorRTLLM = "TensorRT-LLM"
)

func IsSupportedRuntime(runtime string) bool {
	return runtime == RuntimeVLLM || runtime == RuntimeTriton || runtime == RuntimeTensorRTLLM
}

// 模型版本状态
const (
	ModelStatusRegistered  = "REGISTERED"  // 已注册
	ModelStatusValidating  = "VALIDATING"  // 校验中
	ModelStatusValidated   = "VALIDATED"   // 校验通过（可进入发布）
	ModelStatusReleased    = "RELEASED"    // 已发布（可部署）
	ModelStatusUnavailable = "UNAVAILABLE" // 不可部署
)

// 版本状态流转：REGISTERED → VALIDATED → RELEASED
var versionTransitions = map[string]map[string]bool{
	ModelStatusRegistered:  {ModelStatusValidating: true, ModelStatusUnavailable: true, ModelStatusValidated: true},
	ModelStatusValidating:  {ModelStatusValidated: true, ModelStatusUnavailable: true},
	ModelStatusValidated:   {ModelStatusReleased: true, ModelStatusUnavailable: true},
	ModelStatusReleased:    {ModelStatusUnavailable: true},
	ModelStatusUnavailable: {},
}

// CanTransitionVersion 判断版本状态是否可流转
func CanTransitionVersion(from, to string) bool {
	if tos, ok := versionTransitions[from]; ok {
		return tos[to]
	}
	return false
}

// Model 模型主信息
type Model struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`        // 模型名称，全局唯一
	Description string    `json:"description"` // 模型说明
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ModelVersion 模型版本：一个模型可有多个版本，版本是部署的最小引用单元
type ModelVersion struct {
	ID                 string     `json:"id"`
	ModelID            string     `json:"modelId"`        // 所属模型
	ModelName          string     `json:"modelName"`      // 冗余模型名（modelregistry 填充）
	Version            string     `json:"version"`        // 版本号，模型内唯一
	ArtifactURI        string     `json:"artifactUri"`    // 权重地址（S3/MinIO/NFS），不写入日志
	ArtifactDigest     string     `json:"artifactDigest"` // 校验后的摘要（优先 SHA-256）
	ArtifactSize       int64      `json:"artifactSize"`   // 对象大小（字节）
	StorageType        string     `json:"storageType"`    // 当前固定为 s3
	ArtifactVerifiedAt *time.Time `json:"artifactVerifiedAt,omitempty"`
	Runtime            string     `json:"runtime"`       // 运行时，如 vLLM
	GPUType            string     `json:"gpuType"`       // 所需 GPU 型号，如 A100
	GPUCount           int32      `json:"gpuCount"`      // 单副本所需 GPU 数
	MemoryMB           int64      `json:"memoryMB"`      // 单副本所需内存
	ContextLength      int32      `json:"contextLength"` // 最大上下文长度
	Status             string     `json:"status"`        // 可部署性状态
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
}

// Deployable 校验模型版本是否可部署（仅 RELEASED 可部署，对齐 PRD-V2 §4）
func (v *ModelVersion) Deployable() bool {
	return v != nil &&
		v.Status == ModelStatusReleased &&
		IsSupportedRuntime(v.Runtime) &&
		v.ArtifactURI != "" &&
		v.GPUType != "" &&
		v.GPUCount > 0
}
