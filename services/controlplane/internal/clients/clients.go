// Package clients 控制面对外部服务的 HTTP 客户端。
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
)

// HTTPClient 统一 HTTP 客户端
type HTTPClient struct {
	baseURL string
	http    *http.Client
}

// NewHTTPClient 创建客户端
func NewHTTPClient(baseURL string) *HTTPClient {
	return &HTTPClient{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// do 执行请求并解析统一响应
func (c *HTTPClient) do(ctx context.Context, method, path string, body interface{}, out interface{}) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return errcode.Wrap(errcode.ErrInternal, "序列化请求失败", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, &buf)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, "构造请求失败", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return errcode.Wrap(errcode.ErrUpstream, "调用上游服务失败: "+c.baseURL, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return errcode.Wrap(errcode.ErrUpstream, "读取响应失败", err)
	}
	var wrap apitypes.Response
	if err := json.Unmarshal(data, &wrap); err != nil {
		return errcode.Wrap(errcode.ErrUpstream, fmt.Sprintf("解析响应失败: %s", truncate(string(data), 200)), err)
	}
	if wrap.Code != 0 {
		return &errcode.Error{Code: errcode.Code(wrap.Code), Message: wrap.Message, RequestID: wrap.RequestID}
	}
	if out != nil && wrap.Data != nil {
		// 重新编码 Data 到目标类型（Data 是 interface{}）
		raw, err := json.Marshal(wrap.Data)
		if err != nil {
			return errcode.Wrap(errcode.ErrInternal, "序列化响应数据失败", err)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return errcode.Wrap(errcode.ErrInternal, "解析响应数据失败", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---- observability 客户端 ----

// ObservabilityClient 可观测性服务客户端
type ObservabilityClient struct {
	*HTTPClient
}

type RateCard struct {
	TenantID              string  `json:"tenantId"`
	GPUType               string  `json:"gpuType"`
	InputTokenPerMillion  float64 `json:"inputTokenPerMillion"`
	OutputTokenPerMillion float64 `json:"outputTokenPerMillion"`
	GPUHour               float64 `json:"gpuHour"`
}
type DailyUsage struct {
	Date              time.Time `json:"date"`
	TenantID          string    `json:"tenantId"`
	DeploymentID      string    `json:"deploymentId"`
	InputTokens       int64     `json:"inputTokens"`
	OutputTokens      int64     `json:"outputTokens"`
	RequestCount      int64     `json:"requestCount"`
	FailedCount       int64     `json:"failedCount"`
	GPUReplicaSeconds int64     `json:"gpuReplicaSeconds"`
	EstimatedCost     float64   `json:"estimatedCost"`
}

// NewObservabilityClient 创建客户端
func NewObservabilityClient(baseURL string) *ObservabilityClient {
	return &ObservabilityClient{NewHTTPClient(baseURL)}
}

// DeploymentMetrics 查询部署请求指标（转发到 observability）。
func (c *ObservabilityClient) DeploymentMetrics(ctx context.Context, deploymentID, rng string) (*apitypes.MetricsView, error) {
	path := "/api/v1/deployments/" + deploymentID + "/metrics"
	if rng != "" {
		path += "?range=" + rng
	}
	var v apitypes.MetricsView
	if err := c.do(ctx, http.MethodGet, path, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func (c *ObservabilityClient) SetRateCard(ctx context.Context, tenantID string, card RateCard) error {
	return c.do(ctx, http.MethodPut, "/internal/rate-cards/"+url.PathEscape(tenantID), card, nil)
}
func (c *ObservabilityClient) Billing(ctx context.Context, tenantID, from, to string) ([]DailyUsage, error) {
	q := url.Values{"tenantId": {tenantID}}
	if from != "" {
		q.Set("from", from)
	}
	if to != "" {
		q.Set("to", to)
	}
	var rows []DailyUsage
	if err := c.do(ctx, http.MethodGet, "/internal/billing?"+q.Encode(), nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// ---- gateway 客户端 ----

// GatewayClient 管理模型到服务端点的内部路由。
type GatewayClient struct {
	*HTTPClient
}

// NewGatewayClient 创建 gateway 客户端。
func NewGatewayClient(baseURL string) *GatewayClient {
	return &GatewayClient{NewHTTPClient(baseURL)}
}

// RegisterRoute 注册或更新可调用模型路由。
func (c *GatewayClient) RegisterRoute(ctx context.Context, model, modelID, endpoint, tenantID, deploymentID, stableEndpoint, canaryEndpoint, rolloutStatus string) error {
	body := map[string]string{
		"model": model, "modelId": modelID, "endpoint": endpoint, "tenantId": tenantID, "deploymentId": deploymentID,
		"stableEndpoint": stableEndpoint, "canaryEndpoint": canaryEndpoint, "rolloutStatus": rolloutStatus,
	}
	return c.do(ctx, http.MethodPost, "/internal/routes", body, nil)
}

// UnregisterRoute 删除模型路由。
func (c *GatewayClient) UnregisterRoute(ctx context.Context, model string) error {
	return c.do(ctx, http.MethodDelete, "/internal/routes/"+model, nil, nil)
}

// ---- modelregistry 客户端 ----

// ModelRegistryClient 模型注册中心客户端
type ModelRegistryClient struct {
	*HTTPClient
}

// NewModelRegistryClient 创建客户端
func NewModelRegistryClient(baseURL string) *ModelRegistryClient {
	return &ModelRegistryClient{NewHTTPClient(baseURL)}
}

// GetVersion 按版本 ID 查询模型版本
func (c *ModelRegistryClient) GetVersion(ctx context.Context, versionID string) (*domain.ModelVersion, error) {
	var v domain.ModelVersion
	if err := c.do(ctx, http.MethodGet, "/api/v1/versions/"+versionID, nil, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// ---- k8sadapter 客户端 ----

// K8sAdapterClient Kubernetes 适配器客户端
type K8sAdapterClient struct {
	*HTTPClient
}

// NewK8sAdapterClient 创建客户端
func NewK8sAdapterClient(baseURL string) *K8sAdapterClient {
	return &K8sAdapterClient{NewHTTPClient(baseURL)}
}

// CreateDeploymentSpec 创建部署入参
type CreateDeploymentSpec struct {
	DeploymentID   string            `json:"deploymentId"`
	Name           string            `json:"name"`
	Namespace      string            `json:"namespace"`
	Replicas       int32             `json:"replicas"`
	Resource       domain.Resource   `json:"resource"`
	Image          string            `json:"image"`
	Args           []string          `json:"args"`
	Labels         map[string]string `json:"labels"`
	Env            map[string]string `json:"env"`
	ModelPath      string            `json:"modelPath"`
	ArtifactURI    string            `json:"artifactUri,omitempty"`
	ArtifactDigest string            `json:"artifactDigest,omitempty"`
	Runtime        string            `json:"runtime,omitempty"`
}

// K8sDeploymentStatus 同步的部署状态（与 k8sadapter 类型一致）
type K8sDeploymentStatus struct {
	Replicas          int32  `json:"replicas"`
	ReadyReplicas     int32  `json:"readyReplicas"`
	AvailableReplicas int32  `json:"availableReplicas"`
	Condition         string `json:"condition"`
	Message           string `json:"message,omitempty"`
}

// K8sDeploymentResult 部署查询结果
type K8sDeploymentResult struct {
	DeploymentID   string                   `json:"deploymentId"`
	Name           string                   `json:"name"` // 部署名（k8sadapter 返回，R2-2 孤儿检测用）
	Status         *K8sDeploymentStatus     `json:"status"`
	Endpoint       string                   `json:"endpoint"`
	Message        string                   `json:"message"`
	StableEndpoint string                   `json:"stableEndpoint,omitempty"`
	CanaryEndpoint string                   `json:"canaryEndpoint,omitempty"`
	RolloutStatus  string                   `json:"rolloutStatus,omitempty"`
	Pods           []map[string]interface{} `json:"pods"`
	Events         []map[string]interface{} `json:"events"`
}

// ListGPUs 查询 GPU 节点
func (c *K8sAdapterClient) ListGPUs(ctx context.Context) ([]domain.GPUResource, error) {
	var nodes []domain.GPUResource
	if err := c.do(ctx, http.MethodGet, "/v1/resources/gpus", nil, &nodes); err != nil {
		return nil, err
	}
	return nodes, nil
}

// ProvisionTenant 创建租户 Namespace 与隔离资源。
func (c *K8sAdapterClient) ProvisionTenant(ctx context.Context, tenantID string) error {
	return c.do(ctx, http.MethodPost, "/v1/tenants/"+tenantID+"/provision", nil, nil)
}

// CreateDeployment 创建部署
func (c *K8sAdapterClient) CreateDeployment(ctx context.Context, spec *CreateDeploymentSpec) (*K8sDeploymentResult, error) {
	var res K8sDeploymentResult
	if err := c.do(ctx, http.MethodPost, "/v1/deployments", spec, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// UpdateDeployment 更新既有 Kubernetes Deployment。
func (c *K8sAdapterClient) UpdateDeployment(ctx context.Context, spec *CreateDeploymentSpec) (*K8sDeploymentResult, error) {
	var res K8sDeploymentResult
	if err := c.do(ctx, http.MethodPut, "/v1/deployments/"+spec.Name, spec, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetDeployment 查询部署
func (c *K8sAdapterClient) GetDeployment(ctx context.Context, name, namespace string) (*K8sDeploymentResult, error) {
	var res K8sDeploymentResult
	if err := c.do(ctx, http.MethodGet, "/v1/deployments/"+name+"?namespace="+namespace, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// ListDeployments 扫描全部受管部署（R2-2：重启对账/孤儿回收）
func (c *K8sAdapterClient) ListDeployments(ctx context.Context, namespace string) ([]*K8sDeploymentResult, error) {
	var list []*K8sDeploymentResult
	if err := c.do(ctx, http.MethodGet, "/v1/deployments?namespace="+namespace, nil, &list); err != nil {
		return nil, err
	}
	return list, nil
}

// ScaleDeployment 扩缩容
func (c *K8sAdapterClient) ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*K8sDeploymentResult, error) {
	var res K8sDeploymentResult
	body := map[string]int32{"replicas": replicas}
	if err := c.do(ctx, http.MethodPost, "/v1/deployments/"+name+"/scale?namespace="+namespace, body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// RestartDeployment 触发 Kubernetes Deployment 滚动重启。
func (c *K8sAdapterClient) RestartDeployment(ctx context.Context, name, namespace string) (*K8sDeploymentResult, error) {
	var res K8sDeploymentResult
	if err := c.do(ctx, http.MethodPost, "/v1/deployments/"+name+"/restart?namespace="+namespace, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// DeleteDeployment 删除部署（幂等）
func (c *K8sAdapterClient) DeleteDeployment(ctx context.Context, name, namespace string) error {
	return c.do(ctx, http.MethodDelete, "/v1/deployments/"+name+"?namespace="+namespace, nil, nil)
}
