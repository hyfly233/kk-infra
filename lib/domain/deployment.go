package domain

import "time"

// ModelDeployment 模型服务部署，是平台的核心资源对象。
// 生命周期由状态机管理，所有字段均可序列化用于持久化。
type ModelDeployment struct {
	ID             string    `json:"id"`             // 部署 ID（幂等键）
	Name           string    `json:"name"`           // 服务名称（namespace 内唯一）
	ModelID        string    `json:"modelId"`        // 所属模型
	ModelVersionID string    `json:"modelVersionId"` // 引用的模型版本
	ModelName      string    `json:"modelName"`      // 冗余模型名，便于展示与路由
	ModelVersion   string    `json:"modelVersion"`   // 冗余版本号
	TenantID       string    `json:"tenantId"`       // 租户
	Namespace      string    `json:"namespace"`      // K8s Namespace
	Replicas       int32     `json:"replicas"`       // 期望副本数
	Resource       Resource  `json:"resource"`       // 单副本资源规格
	Runtime        string    `json:"runtime"`        // 运行时，如 vLLM
	StartupArgs    []string  `json:"startupArgs"`    // 启动参数（vLLM extra args）
	Endpoint       string    `json:"endpoint"`       // 服务成功后返回的 API Endpoint
	Status         string    `json:"status"`         // 当前状态（见 statemachine.go）
	Generation     int64     `json:"generation"`     // 变更代数，每次期望变更 +1
	Diagnostics    string    `json:"diagnostics"`    // 失败诊断信息
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// DeploymentStatus 部署状态常量（与状态机一致）
const (
	DeploymentStatusNew        = "NEW"        // 新建
	DeploymentStatusValidating = "VALIDATING" // 校验中
	DeploymentStatusSubmitting = "SUBMITTING" // 提交 K8s 中
	DeploymentStatusStarting   = "STARTING"   // 启动中
	DeploymentStatusRunning    = "RUNNING"    // 运行中
	DeploymentStatusScaling    = "SCALING"    // 扩缩容中
	DeploymentStatusRestarting = "RESTARTING" // 重启中
	DeploymentStatusDeleting   = "DELETING"   // 删除中
	DeploymentStatusDeleted    = "DELETED"    // 已删除
	DeploymentStatusFailed     = "FAILED"     // 失败
)

// StatusEvent 状态变更事件，用于审计与诊断
type StatusEvent struct {
	DeploymentID string    `json:"deploymentId"`
	From         string    `json:"from"`   // 变更前状态
	To           string    `json:"to"`     // 变更后状态
	Reason       string    `json:"reason"` // 触发原因
	RequestID    string    `json:"requestId"`
	Diagnostics  string    `json:"diagnostics,omitempty"` // 错误诊断
	ResourceVer  string    `json:"resourceVer,omitempty"` // K8s 资源版本
	At           time.Time `json:"at"`
}

type DeploymentRevision struct {
	ID             string    `json:"id"`
	DeploymentID   string    `json:"deploymentId"`
	Generation     int64     `json:"generation"`
	ModelVersionID string    `json:"modelVersionId"`
	ModelVersion   string    `json:"modelVersion"`
	CreatedAt      time.Time `json:"createdAt"`
}
