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
	if err := c.ensure(ctx, "/api/v1/namespaces/"+ns+"/serviceaccounts/tenant-notebook", "/api/v1/namespaces/"+ns+"/serviceaccounts", map[string]interface{}{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]interface{}{"name": "tenant-notebook", "namespace": ns, "labels": map[string]string{"carrot.ai/tenant-id": tenantID}}, "automountServiceAccountToken": false}); err != nil {
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
	if err := c.ensure(ctx, "/apis/networking.k8s.io/v1/namespaces/"+ns+"/networkpolicies/notebook-access", "/apis/networking.k8s.io/v1/namespaces/"+ns+"/networkpolicies", map[string]interface{}{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]interface{}{"name": "notebook-access", "namespace": ns},
		"spec": map[string]interface{}{
			"podSelector": map[string]interface{}{"matchLabels": map[string]string{"carrot.ai/workload": "notebook"}}, "policyTypes": []string{"Ingress", "Egress"},
			"ingress": []map[string]interface{}{{"from": []map[string]interface{}{{"namespaceSelector": map[string]interface{}{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "jupyterhub"}}}}}},
			"egress":  []map[string]interface{}{{"to": []map[string]interface{}{{"namespaceSelector": map[string]interface{}{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}}}, "ports": []map[string]interface{}{{"protocol": "UDP", "port": 53}, {"protocol": "TCP", "port": 53}}}},
		},
	}); err != nil {
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
			Phase             string `json:"phase"`
			Message           string `json:"message"`
			Replicas          int32  `json:"replicas"`
			ReadyReplicas     int32  `json:"readyReplicas"`
			AvailableReplicas int32  `json:"availableReplicas"`
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

	// 复用 renderer 生成 manifest
	// 虚拟 GPU 池模式（无真实 GPU 节点）不声明 nvidia.com/gpu，避免调度失败
	gpuEnabled := len(c.virtualGPUs) == 0
	res, err := renderDeploymentManifests(spec, ns, image, gpuEnabled, c.artifactConfig(), c.volcanoConfig())
	if err != nil {
		return nil, err
	}

	// 幂等：已存在则直接返回状态
	if _, err := c.GetDeployment(ctx, spec.Name, ns); err == nil {
		if err := c.ensureVolcanoResources(ctx, spec, ns, res.PodGroup); err != nil {
			return nil, err
		}
		if err := c.reconcileScaledObject(ctx, spec, ns); err != nil {
			return nil, err
		}
		return c.GetDeployment(ctx, spec.Name, ns)
	}
	if res.Secret != nil {
		if err := c.ensure(ctx, "/api/v1/namespaces/"+ns+"/secrets/"+res.Secret.Metadata.Name, "/api/v1/namespaces/"+ns+"/secrets", res.Secret); err != nil {
			return nil, fmt.Errorf("创建 artifact Secret 失败: %w", err)
		}
	}
	if err := c.ensureVolcanoResources(ctx, spec, ns, res.PodGroup); err != nil {
		return nil, err
	}
	if c.progressiveEnabled {
		progressive, err := renderProgressiveManifests(spec, res, c.prometheusURL)
		if err != nil {
			return nil, err
		}
		if err := c.ensureProgressiveResources(ctx, spec, ns, res.Service, progressive); err != nil {
			return nil, err
		}
		if err := c.do(ctx, "POST", "/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/rollouts", progressive.Rollout, nil); err != nil {
			return nil, fmt.Errorf("创建 Rollout 失败: %w", err)
		}
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
	res, err := renderDeploymentManifests(spec, ns, image, len(c.virtualGPUs) == 0, c.artifactConfig(), c.volcanoConfig())
	if err != nil {
		return nil, err
	}
	if res.Secret != nil {
		if err := c.ensure(ctx, "/api/v1/namespaces/"+ns+"/secrets/"+res.Secret.Metadata.Name, "/api/v1/namespaces/"+ns+"/secrets", res.Secret); err != nil {
			return nil, fmt.Errorf("更新 artifact Secret 失败: %w", err)
		}
	}
	if err := c.ensureVolcanoResources(ctx, spec, ns, res.PodGroup); err != nil {
		return nil, err
	}
	if c.progressiveEnabled {
		progressive, err := renderProgressiveManifests(spec, res, c.prometheusURL)
		if err != nil {
			return nil, err
		}
		if err := c.ensureProgressiveResources(ctx, spec, ns, res.Service, progressive); err != nil {
			return nil, err
		}
		patch := map[string]interface{}{"metadata": map[string]interface{}{"labels": res.Deployment.Metadata.Labels}, "spec": progressive.Rollout["spec"]}
		if err := c.do(ctx, "PATCH", "/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/rollouts/"+spec.Name, patch, nil); err != nil {
			return nil, fmt.Errorf("更新 Rollout 失败: %w", err)
		}
		if err := c.reconcileScaledObject(ctx, spec, ns); err != nil {
			return nil, err
		}
		return c.GetDeployment(ctx, spec.Name, ns)
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
			Phase             string `json:"phase"`
			Message           string `json:"message"`
			Replicas          int32  `json:"replicas"`
			ReadyReplicas     int32  `json:"readyReplicas"`
			AvailableReplicas int32  `json:"availableReplicas"`
			Conditions        []struct {
				Type    string `json:"type"`
				Status  string `json:"status"`
				Reason  string `json:"reason"`
				Message string `json:"message"`
			} `json:"conditions"`
		} `json:"status"`
	}
	resourcePath, resourceName := "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, "Deployment"
	if c.progressiveEnabled {
		resourcePath = "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/rollouts/" + name
		resourceName = "Rollout"
	}
	err := c.do(ctx, "GET", resourcePath, nil, &dep)
	if err != nil {
		if err == ErrNotFound {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("查询 %s 失败: %w", resourceName, err)
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
	if c.progressiveEnabled {
		switch dep.Status.Phase {
		case "Healthy":
			st.Condition = "Available"
		case "Degraded":
			st.Condition = "ReplicaFailure"
			st.Message = dep.Status.Message
		}
	}

	pods, _ := c.listPods(ctx, ns, "app="+name)
	events, _ := c.listEvents(ctx, ns, name)
	endpoint := fmt.Sprintf("%s.%s.svc.cluster.local", name, ns)
	stableEndpoint, canaryEndpoint, rolloutStatus := "", "", ""
	if c.progressiveEnabled {
		stableEndpoint = fmt.Sprintf("%s-stable.%s.svc.cluster.local", name, ns)
		canaryEndpoint = fmt.Sprintf("%s-canary.%s.svc.cluster.local", name, ns)
		rolloutStatus = dep.Status.Phase
	}
	return &DeploymentResult{
		DeploymentID:   dep.Metadata.Labels["carrot.ai/deployment-id"],
		Name:           name,
		Status:         st,
		Pods:           pods,
		Events:         events,
		Endpoint:       endpoint,
		Message:        st.Message,
		StableEndpoint: stableEndpoint,
		CanaryEndpoint: canaryEndpoint,
		RolloutStatus:  rolloutStatus,
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
	if c.progressiveEnabled {
		path = "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/rollouts?labelSelector=" + labelSelector
	}
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
	path, resourceName := "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, "Deployment"
	if c.progressiveEnabled {
		path = "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/rollouts/" + name
		resourceName = "Rollout"
	}
	if err := c.do(ctx, "PATCH", path, patch, nil); err != nil {
		return nil, fmt.Errorf("扩缩容 %s 失败: %w", resourceName, err)
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
	path, resourceName := "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, "Deployment"
	if c.progressiveEnabled {
		path = "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/rollouts/" + name
		resourceName = "Rollout"
	}
	if err := c.do(ctx, "PATCH", path, patch, nil); err != nil {
		return nil, fmt.Errorf("重启 %s 失败: %w", resourceName, err)
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
	path, resourceName := "/apis/apps/v1/namespaces/"+ns+"/deployments/"+name, "Deployment"
	if c.progressiveEnabled {
		path = "/apis/argoproj.io/v1alpha1/namespaces/" + ns + "/rollouts/" + name
		resourceName = "Rollout"
	}
	err := c.do(ctx, "DELETE", path, nil, nil)
	if err != nil && err != ErrNotFound {
		return fmt.Errorf("删除 %s 失败: %w", resourceName, err)
	}
	// Service 一并删除（忽略不存在）
	_ = c.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/services/"+name, nil, nil)
	if c.progressiveEnabled {
		_ = c.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/services/"+name+"-stable", nil, nil)
		_ = c.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/services/"+name+"-canary", nil, nil)
		_ = c.do(ctx, "DELETE", "/apis/networking.istio.io/v1beta1/namespaces/"+ns+"/virtualservices/"+name+"-traffic", nil, nil)
		_ = c.do(ctx, "DELETE", "/apis/argoproj.io/v1alpha1/namespaces/"+ns+"/analysistemplates/"+name+"-analysis", nil, nil)
	}
	_ = c.do(ctx, "DELETE", "/api/v1/namespaces/"+ns+"/secrets/"+name+"-artifact", nil, nil)
	if c.kedaEnabled {
		_ = c.do(ctx, "DELETE", "/apis/keda.sh/v1alpha1/namespaces/"+ns+"/scaledobjects/"+name, nil, nil)
	}
	if c.volcanoEnabled {
		_ = c.do(ctx, "DELETE", "/apis/scheduling.volcano.sh/v1beta1/namespaces/"+ns+"/podgroups/"+name, nil, nil)
	}
	return nil
}

func (c *RealKubeClient) ensureVolcanoResources(ctx context.Context, spec *DeploymentSpec, namespace string, podGroup map[string]any) error {
	if !c.volcanoEnabled {
		return nil
	}
	if podGroup == nil {
		return fmt.Errorf("Volcano 已启用但 PodGroup 未渲染")
	}
	tenantID := spec.Labels["carrot.ai/tenant-id"]
	queueName := volcanoQueueName(c.volcanoQueuePrefix, tenantID)
	queue := map[string]any{
		"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "Queue",
		"metadata": map[string]any{"name": queueName, "labels": map[string]string{"carrot.ai/tenant-id": tenantID, "carrot.ai/managed-by": "carrot"}},
		"spec":     map[string]any{"weight": 1, "reclaimable": true},
	}
	if err := c.ensure(ctx, "/apis/scheduling.volcano.sh/v1beta1/queues/"+queueName, "/apis/scheduling.volcano.sh/v1beta1/queues", queue); err != nil {
		return fmt.Errorf("创建 Volcano Queue 失败: %w", err)
	}
	if err := c.ensure(ctx, "/apis/scheduling.volcano.sh/v1beta1/namespaces/"+namespace+"/podgroups/"+spec.Name, "/apis/scheduling.volcano.sh/v1beta1/namespaces/"+namespace+"/podgroups", podGroup); err != nil {
		return fmt.Errorf("创建 Volcano PodGroup 失败: %w", err)
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
	scaleTarget := map[string]string{"name": spec.Name}
	if c.progressiveEnabled {
		scaleTarget["apiVersion"] = "argoproj.io/v1alpha1"
		scaleTarget["kind"] = "Rollout"
	}
	object := map[string]interface{}{
		"apiVersion": "keda.sh/v1alpha1", "kind": "ScaledObject",
		"metadata": map[string]interface{}{"name": spec.Name, "namespace": namespace, "labels": spec.Labels},
		"spec": map[string]interface{}{
			"scaleTargetRef": scaleTarget, "minReplicaCount": 1, "maxReplicaCount": 8, "pollingInterval": 15, "cooldownPeriod": 300,
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

func (c *RealKubeClient) ensureProgressiveResources(ctx context.Context, spec *DeploymentSpec, namespace string, entry *k8s.Service, resources *progressiveManifests) error {
	objects := []struct {
		get, create string
		body        any
		label       string
	}{
		{"/api/v1/namespaces/" + namespace + "/services/" + entry.Metadata.Name, "/api/v1/namespaces/" + namespace + "/services", entry, "入口 Service"},
		{"/api/v1/namespaces/" + namespace + "/services/" + resources.StableService.Metadata.Name, "/api/v1/namespaces/" + namespace + "/services", resources.StableService, "stable Service"},
		{"/api/v1/namespaces/" + namespace + "/services/" + resources.CanaryService.Metadata.Name, "/api/v1/namespaces/" + namespace + "/services", resources.CanaryService, "canary Service"},
		{"/apis/argoproj.io/v1alpha1/namespaces/" + namespace + "/analysistemplates/" + spec.Name + "-analysis", "/apis/argoproj.io/v1alpha1/namespaces/" + namespace + "/analysistemplates", resources.AnalysisTemplate, "AnalysisTemplate"},
		{"/apis/networking.istio.io/v1beta1/namespaces/" + namespace + "/virtualservices/" + spec.Name + "-traffic", "/apis/networking.istio.io/v1beta1/namespaces/" + namespace + "/virtualservices", resources.VirtualService, "VirtualService"},
	}
	for _, object := range objects {
		if err := c.ensure(ctx, object.get, object.create, object.body); err != nil {
			return fmt.Errorf("创建 %s 失败: %w", object.label, err)
		}
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
type artifactStorageConfig struct {
	Endpoint, AccessKey, SecretKey string
	Secure                         bool
}

type volcanoConfig struct {
	Enabled     bool
	QueuePrefix string
}

func (c *RealKubeClient) artifactConfig() artifactStorageConfig {
	return artifactStorageConfig{c.artifactEndpoint, c.artifactAccessKey, c.artifactSecretKey, c.artifactSecure}
}

func (c *RealKubeClient) volcanoConfig() volcanoConfig {
	return volcanoConfig{Enabled: c.volcanoEnabled, QueuePrefix: c.volcanoQueuePrefix}
}

func renderDeploymentManifests(spec *DeploymentSpec, ns, image string, gpuEnabled bool, storage artifactStorageConfig, volcanoOptions ...volcanoConfig) (*deployManifests, error) {
	if spec.Name == "" || spec.Resource.GPUCount <= 0 {
		return nil, fmt.Errorf("渲染参数不完整")
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["app"] = spec.Name
	modelPath := spec.ModelPath
	if spec.ArtifactURI != "" {
		modelPath = "/models/model"
	}
	driver, err := runtimeDriver(spec.Runtime)
	if err != nil {
		return nil, err
	}
	runtimeContainer := driver.Build(modelPath, spec.Args)
	if image != "" {
		runtimeContainer.Image = image
	}
	labels["carrot.ai/runtime"] = driver.Runtime()
	if spec.ArtifactURI != "" && (!strings.HasPrefix(spec.ArtifactURI, "s3://") || !strings.HasPrefix(spec.ArtifactDigest, "sha256:")) {
		return nil, fmt.Errorf("artifact 下载要求 s3 URI 和 sha256 digest")
	}
	if spec.ArtifactURI != "" && (storage.Endpoint == "" || storage.AccessKey == "" || storage.SecretKey == "") {
		return nil, fmt.Errorf("artifact 下载未配置 S3 endpoint/credentials")
	}

	resLimits := map[string]string{"memory": fmt.Sprintf("%dMi", spec.Resource.MemoryMB)}
	resRequests := map[string]string{"memory": fmt.Sprintf("%dMi", spec.Resource.MemoryMB)}
	if gpuEnabled {
		resLimits["nvidia.com/gpu"] = fmt.Sprintf("%d", spec.Resource.GPUCount)
		resRequests["nvidia.com/gpu"] = fmt.Sprintf("%d", spec.Resource.GPUCount)
	}

	dep := &k8s.Deployment{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Metadata:   k8s.ObjectMeta{Name: spec.Name, Namespace: ns, Labels: labels, Annotations: map[string]string{"carrot.ai/metrics-path": runtimeContainer.MetricsPath}},
		Spec: k8s.DeploymentSpec{
			Replicas: spec.Replicas,
			Selector: &k8s.LabelSelector{MatchLabels: map[string]string{"app": spec.Name}},
			Template: k8s.PodTemplateSpec{
				Metadata: k8s.ObjectMeta{Labels: labels, Annotations: map[string]string{"prometheus.io/scrape": "true", "prometheus.io/path": runtimeContainer.MetricsPath, "prometheus.io/port": fmt.Sprintf("%d", runtimeContainer.Port)}},
				Spec: k8s.PodSpec{
					RestartPolicy: "Always",
					Containers: []k8s.Container{
						{
							Name:    runtimeContainer.Name,
							Image:   runtimeContainer.Image,
							Command: runtimeContainer.Command,
							Args:    runtimeContainer.Args,
							Ports:   []k8s.ContainerPort{{Name: "http", ContainerPort: runtimeContainer.Port}},
							Resources: k8s.ResourceRequirements{
								Limits:   resLimits,
								Requests: resRequests,
							},
							ReadinessProbe: &k8s.Probe{
								HTTPGet:             &k8s.HTTPGetAction{Path: runtimeContainer.HealthPath, Port: runtimeContainer.Port},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}
	var podGroup map[string]any
	if len(volcanoOptions) > 0 && volcanoOptions[0].Enabled {
		queue := volcanoQueueName(volcanoOptions[0].QueuePrefix, spec.Labels["carrot.ai/tenant-id"])
		dep.Spec.Template.Spec.SchedulerName = "volcano"
		if dep.Spec.Template.Metadata.Annotations == nil {
			dep.Spec.Template.Metadata.Annotations = map[string]string{}
		}
		dep.Spec.Template.Metadata.Annotations["scheduling.volcano.sh/group-name"] = spec.Name
		podGroup = map[string]any{
			"apiVersion": "scheduling.volcano.sh/v1beta1", "kind": "PodGroup",
			"metadata": map[string]any{"name": spec.Name, "namespace": ns, "labels": labels},
			"spec":     map[string]any{"minMember": spec.Replicas, "queue": queue},
		}
	}
	var secret *k8s.Secret
	if spec.ArtifactURI != "" {
		secretName := spec.Name + "-artifact"
		endpoint := storage.Endpoint
		if !strings.Contains(endpoint, "://") {
			if storage.Secure {
				endpoint = "https://" + endpoint
			} else {
				endpoint = "http://" + endpoint
			}
		}
		secret = &k8s.Secret{APIVersion: "v1", Kind: "Secret", Metadata: k8s.ObjectMeta{Name: secretName, Namespace: ns, Labels: labels}, Type: "Opaque", StringData: map[string]string{
			"endpoint": endpoint, "access-key": storage.AccessKey, "secret-key": storage.SecretKey,
		}}
		secretRef := func(key string) *k8s.EnvVarSource {
			return &k8s.EnvVarSource{SecretKeyRef: &k8s.SecretKeySelector{Name: secretName, Key: key}}
		}
		dep.Spec.Template.Spec.Volumes = []k8s.Volume{{Name: "model", EmptyDir: &k8s.EmptyDirVolume{}}}
		dep.Spec.Template.Spec.InitContainers = []k8s.Container{{
			Name: "download-model", Image: "minio/mc:RELEASE.2025-07-21T05-28-08Z", Command: []string{"/bin/sh"},
			Args:         []string{"-ec", `mc alias set storage "$S3_ENDPOINT" "$S3_ACCESS_KEY" "$S3_SECRET_KEY"; mc cp "storage/${ARTIFACT_URI#s3://}" /tmp/model.tar; echo "${ARTIFACT_DIGEST#sha256:}  /tmp/model.tar" | sha256sum -c -; mkdir -p /models/model; tar -xf /tmp/model.tar -C /models/model`},
			Env:          []k8s.EnvVar{{Name: "S3_ENDPOINT", ValueFrom: secretRef("endpoint")}, {Name: "S3_ACCESS_KEY", ValueFrom: secretRef("access-key")}, {Name: "S3_SECRET_KEY", ValueFrom: secretRef("secret-key")}, {Name: "ARTIFACT_URI", Value: spec.ArtifactURI}, {Name: "ARTIFACT_DIGEST", Value: spec.ArtifactDigest}},
			VolumeMounts: []k8s.VolumeMount{{Name: "model", MountPath: "/models"}},
		}}
		dep.Spec.Template.Spec.Containers[0].VolumeMounts = []k8s.VolumeMount{{Name: "model", MountPath: "/models", ReadOnly: true}}
	}
	svc := &k8s.Service{
		APIVersion: "v1",
		Kind:       "Service",
		Metadata:   k8s.ObjectMeta{Name: spec.Name, Namespace: ns, Labels: labels},
		Spec: k8s.ServiceSpec{
			Selector: map[string]string{"app": spec.Name},
			Ports:    []k8s.ServicePort{{Name: "http", Port: 80, TargetPort: runtimeContainer.Port}},
			Type:     "ClusterIP",
		},
	}
	return &deployManifests{Deployment: dep, Service: svc, Secret: secret, PodGroup: podGroup}, nil
}

func volcanoQueueName(prefix, tenantID string) string {
	if tenantID == "" {
		return "default"
	}
	return prefix + tenantID
}

// deployManifests 渲染产物
type deployManifests struct {
	Deployment *k8s.Deployment
	Service    *k8s.Service
	Secret     *k8s.Secret
	PodGroup   map[string]any
}

// 确保 json 引用
var _ = json.Marshal
var _ = time.Now
