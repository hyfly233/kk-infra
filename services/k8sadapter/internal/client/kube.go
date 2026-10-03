// Package client 定义 Kubernetes 客户端接口与 Fake 实现。
// 领域逻辑只依赖该接口，不直接依赖 client-go，方便本地测试与多集群适配。
package client

import (
	"context"
	"errors"

	"kk-infra/lib/domain"
	"kk-infra/services/k8sadapter/internal/k8s"
)

// ErrNotFound 部署不存在
var ErrNotFound = errors.New("deployment not found")

// DeploymentSpec 创建部署的入参（由 controlplane 通过 HTTP 传入）
type DeploymentSpec struct {
	DeploymentID   string            `json:"deploymentId"`
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace"`
	Replicas       int32             `json:"replicas"`
	Resource       domain.Resource   `json:"resource"`
	Image          string            `json:"image"`
	Args           []string          `json:"args"`
	Labels         map[string]string `json:"labels"`    // carrot.ai/* 标签
	Env            map[string]string `json:"env"`       // 注入环境变量
	ModelPath      string            `json:"modelPath"` // 模型权重挂载路径
	ArtifactURI    string            `json:"artifactUri,omitempty"`
	ArtifactDigest string            `json:"artifactDigest,omitempty"`
}

// DeploymentResult 创建/查询结果
type DeploymentResult struct {
	DeploymentID string                `json:"deploymentId"`
	Name         string                `json:"name"` // 部署名（R2-2 孤儿检测用）
	Status       *k8s.DeploymentStatus `json:"status"`
	Pods         []k8s.Pod             `json:"pods"`
	Events       []k8s.Event           `json:"events"`
	Endpoint     string                `json:"endpoint"` // 服务内部 DNS 名（gateway 通过控制面获得）
	Message      string                `json:"message"`
}

// KubeClient Kubernetes 客户端接口
type KubeClient interface {
	// ListGPUNodes 返回 GPU 节点资源快照
	ListGPUNodes(ctx context.Context) ([]domain.GPUResource, error)
	// CreateDeployment 幂等创建 Deployment + Service
	CreateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error)
	// UpdateDeployment 将现有 Deployment 的模板与资源规格更新为期望值。
	UpdateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error)
	// GetDeployment 查询部署状态（含 Pod/事件）
	GetDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error)
	// ListDeployments 按 owner label 扫描全部受管部署（R2-2：重启对账/孤儿回收）
	ListDeployments(ctx context.Context, namespace string) ([]*DeploymentResult, error)
	// ScaleDeployment 调整副本数
	ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*DeploymentResult, error)
	// RestartDeployment 通过更新 Pod template annotation 触发 Kubernetes 滚动重启。
	RestartDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error)
	// DeleteDeployment 幂等删除（资源不存在返回成功）
	DeleteDeployment(ctx context.Context, name, namespace string) error
	// ProvisionTenant 幂等创建租户命名空间及最小隔离资源。
	ProvisionTenant(ctx context.Context, tenantID string) error
	// NodeGPUCapacity 查询指定 GPU 类型的全局可用数（供配额校验）
	NodeGPUCapacity(ctx context.Context, gpuType string) (total, allocatable, used int32, err error)
}
