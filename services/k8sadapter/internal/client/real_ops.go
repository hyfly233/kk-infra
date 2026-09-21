package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"kk-infra/lib/domain"
	"kk-infra/services/k8sadapter/internal/k8s"
)

// ProvisionTenant 创建租户 Namespace 及默认拒绝入口/出口的网络策略。
// 其余租户资源由后续 RBAC/配额控制器补齐；每项先 GET 再 POST，重复调用安全。
func (c *RealKubeClient) ProvisionTenant(ctx context.Context, tenantID string) error {
	if tenantID == "" || strings.Contains(tenantID, "/") {
		return fmt.Errorf("invalid tenant id")
	}
	ns := "tenant-" + tenantID
	if err := c.ensure(ctx, "/api/v1/namespaces/"+ns, "/api/v1/namespaces", map[string]interface{}{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]interface{}{"name": ns, "labels": map[string]string{"carrot.ai/tenant-id": tenantID}}}); err != nil {
		return err
	}
	if err := c.ensure(ctx, "/api/v1/namespaces/"+ns+"/serviceaccounts/tenant-runtime", "/api/v1/namespaces/"+ns+"/serviceaccounts", map[string]interface{}{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]interface{}{"name": "tenant-runtime", "namespace": ns}, "automountServiceAccountToken": false}); err != nil {
		return err
	}
	if err := c.ensure(ctx, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/roles/tenant-runtime", "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/roles", map[string]interface{}{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": map[string]interface{}{"name": "tenant-runtime", "namespace": ns}, "rules": []map[string]interface{}{{"apiGroups": []string{"apps"}, "resources": []string{"deployments"}, "verbs": []string{"get", "list", "watch"}}, {"apiGroups": []string{""}, "resources": []string{"pods", "services"}, "verbs": []string{"get", "list", "watch"}}}}); err != nil {
		return err
	}
	if err := c.ensure(ctx, "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/rolebindings/tenant-runtime", "/apis/rbac.authorization.k8s.io/v1/namespaces/"+ns+"/rolebindings", map[string]interface{}{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": map[string]interface{}{"name": "tenant-runtime", "namespace": ns}, "roleRef": map[string]string{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "tenant-runtime"}, "subjects": []map[string]string{{"kind": "ServiceAccount", "name": "tenant-runtime", "namespace": ns}}}); err != nil {
		return err
	}
	if err := c.ensure(ctx, "/api/v1/namespaces/"+ns+"/resourcequotas/tenant-default", "/api/v1/namespaces/"+ns+"/resourcequotas", map[string]interface{}{"apiVersion": "v1", "kind": "ResourceQuota", "metadata": map[string]interface{}{"name": "tenant-default", "namespace": ns}, "spec": map[string]interface{}{"hard": map[string]string{"pods": "100", "requests.cpu": "100", "requests.memory": "256Gi"}}}); err != nil {
		return err
	}
	return c.ensure(ctx, "/apis/networking.k8s.io/v1/namespaces/"+ns+"/networkpolicies/default-deny", "/apis/networking.k8s.io/v1/namespaces/"+ns+"/networkpolicies", map[string]interface{}{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]interface{}{"name": "default-deny", "namespace": ns}, "spec": map[string]interface{}{"podSelector": map[string]interface{}{}, "policyTypes": []string{"Ingress", "Egress"}}})
}

func (c *RealKubeClient) ensure(ctx context.Context, getPath, postPath string, body interface{}) error {
	var ignored interface{}
	err := c.do(ctx, http.MethodGet, getPath, nil, &ignored)
	if err == nil {
		return nil
	}
	if err != ErrNotFound {
		return err
	}
	return c.do(ctx, http.MethodPost, postPath, body, &ignored)
}

// ---- KubeClient 接口实现（真实集群） ----

// nodeList K8s Node 列表响应
type nodeList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Allocatable map[string]string `json:"allocatable"`
			Conditions  []struct {
				Type   string `json:"type"`
				Status string `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// ListGPUNodes 列出 GPU 节点。
// 真实节点上有 nvidia.com/gpu 资源时读取；无 GPU 集群（Docker Desktop）
// 使用虚拟 GPU 池，保证平台闭环可验证。
func (c *RealKubeClient) ListGPUNodes(ctx context.Context) ([]domain.GPUResource, error) {
	var out []domain.GPUResource
	var list nodeList
	if err := c.do(ctx, "GET", "/api/v1/nodes", nil, &list); err != nil {
		return nil, fmt.Errorf("查询节点失败: %w", err)
	}
	// 统计真实 GPU
	realFound := false
	for _, n := range list.Items {
		gpuTotal := parseInt(n.Status.Allocatable["nvidia.com/gpu"])
		gpuType := "A100" // 真实集群可扩展从标签读取，MVP 简化
		if gpuTotal > 0 {
			realFound = true
			healthy := true
			for _, cond := range n.Status.Conditions {
				if cond.Type == "Ready" && cond.Status != "True" {
					healthy = false
				}
			}
			health := domain.GPUHealthHealthy
			if !healthy {
				health = domain.GPUHealthError
			}
			out = append(out, domain.GPUResource{
				NodeName:    n.Metadata.Name,
				GPUType:     gpuType,
				Total:       gpuTotal,
				Allocatable: gpuTotal,
				Used:        0,
				MemoryMB:    81920,
				Utilization: 0,
				Health:      health,
			})
		}
	}
	// 无真实 GPU 时使用虚拟池
	if !realFound && len(c.virtualGPUs) > 0 {
		for _, v := range c.virtualGPUs {
			out = append(out, domain.GPUResource{
				NodeName:    v.NodeName,
				GPUType:     v.GPUType,
				Total:       v.GPUCount,
				Allocatable: v.GPUCount,
				Used:        c.usedGPUByType(v.GPUType),
				MemoryMB:    v.MemoryMB,
				Utilization: v.Utilization,
				Health:      v.Health,
			})
		}
	}
	return out, nil
}

// usedGPUByType 统计某型号已用 GPU（从已部署资源的 spec 计算，简化）
func (c *RealKubeClient) usedGPUByType(gpuType string) int32 {
	return 0 // MVP 简化：真实集群由 K8s 调度保证，不超卖
}

// NodeGPUCapacity 返回指定型号容量
func (c *RealKubeClient) NodeGPUCapacity(ctx context.Context, gpuType string) (total, allocatable, used int32, err error) {
	nodes, err := c.ListGPUNodes(ctx)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, n := range nodes {
		if n.GPUType == gpuType {
			total += n.Total
			allocatable += n.Allocatable
			used += n.Used
		}
	}
	return total, allocatable, used, nil
}

// ---- Deployment 操作 ----

// deploymentList 部署列表响应（含状态）
type deploymentList struct {
	Items []struct {
		Metadata struct {
			Name   string            `json:"name"`
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Replicas int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas          int32 `json:"replicas"`
			ReadyReplicas     int32 `json:"readyReplicas"`
			AvailableReplicas int32 `json:"availableReplicas"`
			Conditions        []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	} `json:"items"`
}

// CreateDeployment 幂等创建 Deployment + Service
func (c *RealKubeClient) CreateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error) {
	ns := spec.Namespace
	if ns == "" {
		ns = c.namespace
	}
	image := spec.Image
	if image == "" {
		image = c.deployImage
	}
	if image == "" {
		image = "vllm/vllm-openai:latest"
	}

	// 复用 renderer 生成 manifest
	// 虚拟 GPU 池模式（无真实 GPU 节点）不声明 nvidia.com/gpu，避免调度失败
	gpuEnabled := len(c.virtualGPUs) == 0
	res, err := renderDeploymentManifests(spec, ns, image, gpuEnabled)
	if err != nil {
		return nil, err
	}

	// 幂等：已存在则直接返回状态
	if _, err := c.GetDeployment(ctx, spec.Name, ns); err == nil {
		if err := c.reconcileScaledObject(ctx, spec, ns); err != nil {
			return nil, err
		}
		return c.GetDeployment(ctx, spec.Name, ns)
	}

	// 创建 Deployment
	if err := c.do(ctx, "POST", "/apis/apps/v1/namespaces/"+ns+"/deployments", res.Deployment, nil); err != nil {
		return nil, fmt.Errorf("创建 Deployment 失败: %w", err)
	}
	// 创建 Service（幂等：已存在则忽略）
	_ = c.do(ctx, "POST", "/api/v1/namespaces/"+ns+"/services", res.Service, nil)
	if err := c.reconcileScaledObject(ctx, spec, ns); err != nil {
		return nil, err
	}

	return c.GetDeployment(ctx, spec.Name, ns)
}

// UpdateDeployment 更新现有 Deployment 的 Pod template；Service 保持不变。
func (c *RealKubeClient) UpdateDeployment(ctx context.Context, spec *DeploymentSpec) (*DeploymentResult, error) {
	ns := spec.Namespace
	if ns == "" {
		ns = c.namespace
	}
	image := spec.Image
	if image == "" {
		image = c.deployImage
	}
	if image == "" {
		image = "vllm/vllm-openai:latest"
	}
	res, err := renderDeploymentManifests(spec, ns, image, len(c.virtualGPUs) == 0)
	if err != nil {
		return nil, err
	}
	patch := map[string]interface{}{
		"metadata": map[string]interface{}{"labels": res.Deployment.Metadata.Labels},
		"spec":     res.Deployment.Spec,
	}
	if err := c.do(ctx, "PATCH", "/apis/apps/v1/namespaces/"+ns+"/deployments/"+spec.Name, patch, nil); err != nil {
		return nil, fmt.Errorf("更新 Deployment 失败: %w", err)
	}
	if err := c.reconcileScaledObject(ctx, spec, ns); err != nil {
		return nil, err
	}
	return c.GetDeployment(ctx, spec.Name, ns)
}

// GetDeployment 查询部署状态（含 Pod/事件）
func (c *RealKubeClient) GetDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error) {
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	var dep struct {
		Metadata struct {
			Labels map[string]string `json:"labels"`
		} `json:"metadata"`
		Spec struct {
			Replicas int32 `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas          int32 `json:"replicas"`
			ReadyReplicas     int32 `json:"readyReplicas"`
			AvailableReplicas int32 `json:"availableReplicas"`
			Conditions        []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	err := c.do(ctx, "GET", "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, nil, &dep)
	if err != nil {
		if err == ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("查询 Deployment 失败: %w", err)
	}

	// 状态推导
	st := &k8s.DeploymentStatus{
		Replicas:          dep.Spec.Replicas,
		ReadyReplicas:     dep.Status.ReadyReplicas,
		AvailableReplicas: dep.Status.AvailableReplicas,
		Condition:         "Progressing",
	}
	for _, cond := range dep.Status.Conditions {
		if cond.Type == "Available" && cond.Status == "True" {
			st.Condition = "Available"
			st.Message = ""
			break
		}
		if cond.Type == "Progressing" && cond.Status == "False" {
			st.Condition = "ReplicaFailure"
			st.Message = cond.Message
			if st.Message == "" {
				st.Message = cond.Reason
			}
		}
	}

	pods, _ := c.listPods(ctx, ns, "app="+name)
	events, _ := c.listEvents(ctx, ns, name)
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local", name, ns)
	return &DeploymentResult{
		DeploymentID: dep.Metadata.Labels["carrot.ai/deployment-id"],
		Name:         name,
		Status:       st,
		Pods:         pods,
		Events:       events,
		Endpoint:     endpoint,
		Message:      st.Message,
	}, nil
}

// ListDeployments 按 owner label 扫描全部受管部署（R2-2）
func (c *RealKubeClient) ListDeployments(ctx context.Context, namespace string) ([]*DeploymentResult, error) {
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	var list deploymentList
	labelSelector := "carrot.ai%2Fmanaged-by%3Dcarrot"
	path := "/apis/apps/v1/namespaces/" + ns + "/deployments?labelSelector=" + labelSelector
	if err := c.do(ctx, "GET", path, nil, &list); err != nil {
		return nil, fmt.Errorf("扫描部署失败: %w", err)
	}
	out := make([]*DeploymentResult, 0, len(list.Items))
	for _, item := range list.Items {
		res, err := c.GetDeployment(ctx, item.Metadata.Name, ns)
		if err == nil {
			out = append(out, res)
		}
	}
	return out, nil
}

// ScaleDeployment 更新副本数（merge patch）
func (c *RealKubeClient) ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*DeploymentResult, error) {
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"replicas": replicas,
		},
	}
	if err := c.do(ctx, "PATCH", "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, patch, nil); err != nil {
		return nil, fmt.Errorf("扩缩容失败: %w", err)
	}
	return c.GetDeployment(ctx, name, ns)
}

// RestartDeployment 通过修改 Pod template annotation 触发 Deployment 滚动重启。
func (c *RealKubeClient) RestartDeployment(ctx context.Context, name, namespace string) (*DeploymentResult, error) {
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"annotations": map[string]string{"carrot.ai/restarted-at": time.Now().UTC().Format(time.RFC3339Nano)},
				},
			},
		},
	}
	if err := c.do(ctx, "PATCH", "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, patch, nil); err != nil {
		return nil, fmt.Errorf("重启 Deployment 失败: %w", err)
	}
	return c.GetDeployment(ctx, name, ns)
}

// DeleteDeployment 幂等删除 Deployment + Service
func (c *RealKubeClient) DeleteDeployment(ctx context.Context, name, namespace string) error {
	ns := namespace
	if ns == "" {
		ns = c.namespace
	}
	// Deployment 不存在视为成功（幂等）
	err := c.do(ctx, "DELETE", "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, nil, nil)
	if err != nil && err != ErrNotFound {
		return fmt.Errorf("删除 Deployment 失败: %w", err)
	}
	// Service 一并删除（忽略不存在）
	_ = c.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
	if c.kedaEnabled {
		_ = c.do(ctx, "DELETE", "/apis/keda.sh/v1alpha1/namespaces/"+ns+"/scaledobjects/"+name, nil, nil)
	}
	return nil
}

func (c *RealKubeClient) reconcileScaledObject(ctx context.Context, spec *DeploymentSpec, namespace string) error {
	if !c.kedaEnabled {
		return nil
	}
	if c.prometheusURL == "" {
		return fmt.Errorf("KEDA 已启用但 prometheus-url 为空")
	}
	tenantID := spec.Labels["carrot.ai/tenant-id"]
	labels := fmt.Sprintf(`tenant_id="%s",deployment_id="%s"`, tenantID, spec.DeploymentID)
	object := map[string]interface{}{
		"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]interface{}{"name": spec.Name, "namespace": namespace, "labels": spec.Labels},
		"spec": map[string]interface{}{
			"scaleTargetRef": map[string]string{"name": spec.Name}, "minReplicaCount": 1, "maxReplicaCount": 8, "pollingInterval": 15, "cooldownPeriod": 300,
			"advanced": map[string]interface{}{"horizontalPodAutoscalerConfig": map[string]interface{}{"behavior": map[string]interface{}{"scaleDown": map[string]interface{}{"stabilizationWindowSeconds": 300}}}},
			"triggers": []map[string]interface{}{
				{"type": "prometheus", "metadata": map[string]string{"serverAddress": c.prometheusURL, "metricName": "carrot_inference_qps", "query": fmt.Sprintf(`sum(rate(carrot_inference_requests_total{%s}[2m]))`, labels), "threshold": "5"}},
				{"type": "prometheus", "metadata": map[string]string{"serverAddress": c.prometheusURL, "metricName": "carrot_inference_queue_length", "query": fmt.Sprintf(`max(carrot_inference_queue_length{%s})`, labels), "threshold": "10"}},
			},
		},
	}
	path := "/apis/keda.sh/v1alpha1/namespaces/" + namespace + "/scaledobjects/" + spec.Name
	var existing interface{}
	if err := c.do(ctx, "GET", path, nil, &existing); err == nil {
		if err := c.do(ctx, "PATCH", path, object, nil); err != nil {
			return fmt.Errorf("更新 KEDA ScaledObject 失败: %w", err)
		}
		return nil
	} else if err != ErrNotFound {
		return fmt.Errorf("查询 KEDA ScaledObject 失败: %w", err)
	}
	if err := c.do(ctx, "POST", "/apis/keda.sh/v1alpha1/namespaces/"+namespace+"/scaledobjects", object, nil); err != nil {
		return fmt.Errorf("创建 KEDA ScaledObject 失败（请确认 KEDA CRD 已安装）: %w", err)
	}
	return nil
}

// ---- 辅助 ----

// listPods 列出部署的 Pod
func (c *RealKubeClient) listPods(ctx context.Context, ns, selector string) ([]k8s.Pod, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Ready bool `json:"ready"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	path := "/api/v1/namespaces/" + ns + "/pods?labelSelector=" + selector
	if err := c.do(ctx, "GET", path, nil, &list); err != nil {
		return nil, err
	}
	var pods []k8s.Pod
	for _, p := range list.Items {
		ready := false
		for _, cs := range p.Status.ContainerStatuses {
			if cs.Ready {
				ready = true
			}
		}
		pods = append(pods, k8s.Pod{
			Name:      p.Metadata.Name,
			Namespace: ns,
			Labels:    p.Metadata.Labels,
			Phase:     p.Status.Phase,
			Ready:     ready,
		})
	}
	return pods, nil
}

// listEvents 列出部署相关事件
func (c *RealKubeClient) listEvents(ctx context.Context, ns, name string) ([]k8s.Event, error) {
	var list struct {
		Items []struct {
			Type          string `json:"type"`
			Reason        string `json:"reason"`
			Message       string `json:"message"`
			LastTimestamp string `json:"lastTimestamp"`
		} `json:"items"`
	}
	path := "/api/v1/namespaces/" + ns + "/events?fieldSelector=involvedObject.name%3D" + name
	if err := c.do(ctx, "GET", path, nil, &list); err != nil {
		return nil, err
	}
	var events []k8s.Event
	for _, e := range list.Items {
		events = append(events, k8s.Event{
			Type:     e.Type,
			Reason:   e.Reason,
			Message:  e.Message,
			LastTime: e.LastTimestamp,
		})
	}
	return events, nil
}

// parseInt 字符串转 int32
func parseInt(s string) int32 {
	if s == "" {
		return 0
	}
	var n int32
	var neg bool
	for i, ch := range s {
		if i == 0 && ch == '-' {
			neg = true
			continue
		}
		if ch >= '0' && ch <= '9' {
			n = n*10 + int32(ch-'0')
		}
	}
	if neg {
		return -n
	}
	return n
}

// renderDeploymentManifests 由 spec 生成 K8s manifest（复用 renderer 逻辑）。
// gpuEnabled=false 时（虚拟 GPU 池验证场景）不声明 nvidia.com/gpu，避免无 GPU 集群调度失败。
// 注意：此处不使用 renderer 包的 Render（其输出含 vLLM 固定参数），
// 而是按 spec 直接生成，便于接入自定义镜像。
func renderDeploymentManifests(spec *DeploymentSpec, ns, image string, gpuEnabled bool) (*deployManifests, error) {
	if spec.Name == "" || spec.Resource.GPUCount <= 0 {
		return nil, fmt.Errorf("渲染参数不完整")
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["app"] = spec.Name

	// 启动参数：--model + 用户参数（mock 镜像忽略 --model）
	args := []string{"--model", spec.ModelPath}
	args = append(args, spec.Args...)

	resLimits := map[string]string{"memory": fmt.Sprintf("%dMi", spec.Resource.MemoryMB)}
	resRequests := map[string]string{"memory": fmt.Sprintf("%dMi", spec.Resource.MemoryMB)}
	if gpuEnabled {
		resLimits["nvidia.com/gpu"] = fmt.Sprintf("%d", spec.Resource.GPUCount)
		resRequests["nvidia.com/gpu"] = fmt.Sprintf("%d", spec.Resource.GPUCount)
	}

	dep := &k8s.Deployment{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata:   k8s.ObjectMeta{Name: spec.Name, Namespace: ns, Labels: labels},
		Spec: k8s.DeploymentSpec{
			Replicas: spec.Replicas,
			Selector: &k8s.LabelSelector{MatchLabels: map[string]string{"app": spec.Name}},
			Template: k8s.PodTemplateSpec{
				Metadata: k8s.ObjectMeta{Labels: labels},
				Spec: k8s.PodSpec{
					RestartPolicy: "Always",
					Containers: []k8s.Container{
						{
							Name:  "inference",
							Image: image,
							Args:  args,
							Ports: []k8s.ContainerPort{{Name: "http", ContainerPort: 8000}},
							Resources: k8s.ResourceRequirements{
								Limits:   resLimits,
								Requests: resRequests,
							},
							ReadinessProbe: &k8s.Probe{
								HTTPGet:             &k8s.HTTPGetAction{Path: "/health", Port: 8000},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}
	svc := &k8s.Service{
		APIVersion: "v1",
		Kind:       "Service",
		Metadata:   k8s.ObjectMeta{Name: spec.Name, Namespace: ns, Labels: labels},
		Spec: k8s.ServiceSpec{
			Selector: map[string]string{"app": spec.Name},
			Ports:    []k8s.ServicePort{{Name: "http", Port: 80, TargetPort: 8000}},
			Type:     "ClusterIP",
		},
	}
	return &deployManifests{Deployment: dep, Service: svc}, nil
}

// deployManifests 渲染产物
type deployManifests struct {
	Deployment *k8s.Deployment
	Service    *k8s.Service
}

// 确保 json 引用
var _ = json.Marshal
var _ = time.Now
