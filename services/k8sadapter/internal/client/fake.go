package client

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"kk-infra/lib/domain"
	"kk-infra/services/k8sadapter/internal/k8s"
)

// FakeNodeConfig 模拟 GPU 节点配置
type FakeNodeConfig struct {
	NodeName    string  // 节点名
	GPUType     string  // GPU 型号
	GPUCount    int32   // GPU 总数
	MemoryMB    int64   // 单卡显存
	Utilization float64 // 平均利用率 0-100
	Health      string  // 健康状态
}

// DefaultFakeNodes 默认 Fake 集群：2 节点 × 8 卡 A100
func DefaultFakeNodes() []FakeNodeConfig {
	return []FakeNodeConfig{
		{NodeName: "gpu-001", GPUType: "A100", GPUCount: 8, MemoryMB: 81920, Utilization: 42, Health: domain.GPUHealthHealthy},
		{NodeName: "gpu-002", GPUType: "A100", GPUCount: 8, MemoryMB: 81920, Utilization: 65, Health: domain.GPUHealthHealthy},
	}
}

// FakeDeployment 内存中的模拟部署
type FakeDeployment struct {
	Spec      DeploymentSpec
	CreatedAt time.Time
	// 当前推进阶段：0=pending 1=starting 2=running
	Stage     int
	Ready     int32
	FailAfter int // >0 表示推进 N 阶段后失败（测试注入）
	Message   string
}

// FakeKubeClient 模拟 Kubernetes 客户端。
// 部署生命周期：创建 → Pending(1s) → Starting(2s) → Running。
// 扩缩容：更新副本数后 Ready 逐步收敛。删除：幂等。
type FakeKubeClient struct {
	mu           sync.RWMutex
	nodes        []FakeNodeConfig
	deploys      map[string]*FakeDeployment // key: namespace/name
	startLatency time.Duration              // 每阶段推进延迟
}

// NewFakeKubeClient 创建 Fake 客户端
func NewFakeKubeClient(nodes []FakeNodeConfig) *FakeKubeClient {
	return &FakeKubeClient{
		nodes:        nodes,
		deploys:      make(map[string]*FakeDeployment),
		startLatency: 1200 * time.Millisecond,
	}
}

// key 组装部署键
func key(name, namespace string) string { return namespace + "/" + name }

// ListGPUNodes 返回 GPU 节点快照（统计已用）
func (f *FakeKubeClient) ListGPUNodes(ctx context.Context) ([]domain.GPUResource, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]domain.GPUResource, 0, len(f.nodes))
	for _, n := range f.nodes {
		used := f.usedGPUOnNode(n.NodeName)
		out = append(out, domain.GPUResource{
			NodeName:    n.NodeName,
			GPUType:     n.GPUType,
			Total:       n.GPUCount,
			Allocatable: n.GPUCount,
			Used:        used,
			MemoryMB:    n.MemoryMB,
			Utilization: n.Utilization,
			Health:      n.Health,
		})
	}
	return out, nil
}

// usedGPUOnNode 统计某节点已用 GPU（Running/Starting 阶段的部署副本数 × GPU/副本）
func (f *FakeKubeClient) usedGPUOnNode(nodeName string) int32 {
	var used int32
	// 简单模拟：按部署顺序平均分散到节点
	for _, d := range f.deploys {
		if d.Stage >= 1 {
			used += d.Spec.Replicas * d.Spec.Resource.GPUCount
		}
	}
	return used
}

// NodeGPUCapacity 返回指定 GPU 类型容量
func (f *FakeKubeClient) NodeGPUCapacity(ctx context.Context, gpuType string) (total, allocatable, used int32, err error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	for _, n := range f.nodes {
		if n.GPUType == gpuType {
			total += n.GPUCount
			allocatable += n.GPUCount
		}
	}
	// used 按所有部署占用统计
	var allUsed int32
	for _, d := range f.deploys {
		if d.Spec.Resource.GPUType == gpuType && d.Stage >= 1 {
			allUsed += d.Spec.Replicas * d.Spec.Resource.GPUCount
		}
	}
	return total, allocatable, allUsed, nil
}

// CreateDeployment 幂等创建部署，异步推进生命周期
func (f *FakeKubeClient) CreateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error) {
	f.mu.Lock()
	k := key(spec.Name, spec.Namespace)
	if _, exists := f.deploys[k]; exists {
		f.mu.Unlock()
		return f.GetDeployment(ctx, spec.Name, spec.Namespace)
	}
	d := &FakeDeployment{Spec: *spec, CreatedAt: time.Now()}
	f.deploys[k] = d
	f.mu.Unlock()

	// 异步推进：Pending → Starting → Running
	go f.advance(k)
	return f.GetDeployment(ctx, spec.Name, spec.Namespace)
}

// UpdateDeployment 替换 Fake 部署规格并重新推进就绪状态，模拟滚动更新。
func (f *FakeKubeClient) UpdateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error) {
	f.mu.Lock()
	d, ok := f.deploys[key(spec.Name, spec.Namespace)]
	if !ok {
		f.mu.Unlock()
		return nil, ErrNotFound
	}
	d.Spec = *spec
	d.Stage = 0
	d.Ready = 0
	d.Message = "正在滚动更新"
	f.mu.Unlock()

	go f.advance(key(spec.Name, spec.Namespace))
	return f.GetDeployment(ctx, spec.Name, spec.Namespace)
}

// advance 推进部署生命周期（幂等，并发安全）
func (f *FakeKubeClient) advance(k string) {
	time.Sleep(f.startLatency)
	f.mu.Lock()
	d, ok := f.deploys[k]
	if !ok {
		f.mu.Unlock()
		return
	}
	if d.Stage == 0 {
		d.Stage = 1
		d.Message = "Pod 已创建，正在拉取镜像"
	}
	f.mu.Unlock()

	time.Sleep(f.startLatency)
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok = f.deploys[k]
	if !ok {
		return
	}
	if d.Stage == 1 {
		if d.FailAfter > 0 && d.FailAfter <= 1 {
			d.Stage = 99 // 失败态
			d.Message = "镜像拉取失败: ImagePullBackOff"
			return
		}
		d.Stage = 2
		d.Ready = d.Spec.Replicas
		d.Message = ""
	}
}

// GetDeployment 查询部署状态
func (f *FakeKubeClient) GetDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error) {
	f.mu.RLock()
	d, ok := f.deploys[key(name, namespace)]
	if !ok {
		f.mu.RUnlock()
		return nil, ErrNotFound
	}
	spec := d.Spec
	stage := d.Stage
	ready := d.Ready
	message := d.Message
	f.mu.RUnlock()

	// 组装状态
	cond := "Progressing"
	status := k8s.DeploymentStatus{
		Replicas:          spec.Replicas,
		ReadyReplicas:     ready,
		AvailableReplicas: ready,
	}
	switch {
	case stage == 99:
		cond = "ReplicaFailure"
		status.Condition = cond
		status.Message = message
	case stage == 2:
		cond = "Available"
		status.Condition = cond
	case stage == 1:
		status.Message = message
	default:
		status.Message = "等待调度"
	}

	pods := f.simulatePods(name, namespace, spec, stage, ready, message)
	events := f.simulateEvents(name, namespace, stage, message)
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local", name, namespace)
	return &DeploymentResult{
		DeploymentID: spec.DeploymentID,
		Name:         name,
		Status:       &status,
		Pods:         pods,
		Events:       events,
		Endpoint:     endpoint,
		Message:      message,
	}, nil
}

// simulatePods 模拟 Pod 列表
func (f *FakeKubeClient) simulatePods(name, namespace string, spec DeploymentSpec, stage int, ready int32, message string) []k8s.Pod {
	var pods []k8s.Pod
	for i := int32(0); i < spec.Replicas; i++ {
		p := k8s.Pod{
			Name:      fmt.Sprintf("%s-%d", name, i),
			Namespace: namespace,
			Labels:    map[string]string{"app": name},
		}
		switch {
		case stage == 99:
			p.Phase = "Failed"
			p.Reason = "ImagePullBackOff"
			p.Message = message
		case stage == 2 && i < ready:
			p.Phase = "Running"
			p.Ready = true
		case stage == 2:
			p.Phase = "Running"
		case stage == 1:
			p.Phase = "Pending"
			p.Message = "拉取镜像中"
		default:
			p.Phase = "Pending"
			p.Message = "等待调度"
		}
		pods = append(pods, p)
	}
	return pods
}

// simulateEvents 模拟 K8s 事件
func (f *FakeKubeClient) simulateEvents(name, namespace string, stage int, message string) []k8s.Event {
	events := []k8s.Event{
		{Type: "Normal", Reason: "Scheduled", Message: "成功调度到节点 gpu-001", LastTime: time.Now().Format(time.RFC3339)},
	}
	if stage >= 1 {
		events = append(events, k8s.Event{Type: "Normal", Reason: "Pulled", Message: "镜像拉取完成", LastTime: time.Now().Format(time.RFC3339)})
	}
	if stage == 99 {
		events = append(events, k8s.Event{Type: "Warning", Reason: "BackOff", Message: message, LastTime: time.Now().Format(time.RFC3339)})
	}
	if stage == 2 {
		events = append(events, k8s.Event{Type: "Normal", Reason: "Created", Message: "容器创建成功", LastTime: time.Now().Format(time.RFC3339)})
	}
	return events
}

// ListDeployments 列出 Fake 集群中全部受管部署（R2-2）
func (f *FakeKubeClient) ListDeployments(ctx context.Context, namespace string) ([]*DeploymentResult, error) {
	f.mu.RLock()
	var names []string
	for k := range f.deploys {
		parts := strings.SplitN(k, "/", 2)
		if len(parts) == 2 && (namespace == "" || parts[0] == namespace) {
			names = append(names, parts[1])
		}
	}
	f.mu.RUnlock()
	out := make([]*DeploymentResult, 0, len(names))
	for _, n := range names {
		res, err := f.GetDeployment(ctx, n, namespace)
		if err == nil {
			out = append(out, res)
		}
	}
	return out, nil
}

// ScaleDeployment 扩缩容
func (f *FakeKubeClient) ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*DeploymentResult, error) {
	f.mu.Lock()
	d, ok := f.deploys[key(name, namespace)]
	if !ok {
		f.mu.Unlock()
		return nil, ErrNotFound
	}
	d.Spec.Replicas = replicas
	if d.Stage == 2 {
		d.Ready = replicas
	}
	f.mu.Unlock()
	return f.GetDeployment(ctx, name, namespace)
}

// RestartDeployment 重新推进 Pod 生命周期，模拟 Kubernetes 滚动重启。
func (f *FakeKubeClient) RestartDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error) {
	f.mu.Lock()
	d, ok := f.deploys[key(name, namespace)]
	if !ok {
		f.mu.Unlock()
		return nil, ErrNotFound
	}
	d.Stage = 0
	d.Ready = 0
	d.Message = "正在重启"
	f.mu.Unlock()

	go f.advance(key(name, namespace))
	return f.GetDeployment(ctx, name, namespace)
}

// DeleteDeployment 幂等删除
func (f *FakeKubeClient) DeleteDeployment(ctx context.Context, name, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.deploys, key(name, namespace))
	return nil
}
