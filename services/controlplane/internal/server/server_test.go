package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
	"kk-infra/services/controlplane/internal/identity"
)

// mockModelClient 模拟 modelregistry
type mockModelClient struct{}

func (m *mockModelClient) GetVersion(ctx context.Context, versionID string) (*domain.ModelVersion, error) {
	return &domain.ModelVersion{
		ID:             versionID,
		ModelID:        "m1",
		ModelName:      "qwen",
		Version:        versionID,
		ArtifactURI:    "s3://models/qwen-7b",
		ArtifactDigest: "sha256:0123456789abcdef",
		Runtime:        domain.RuntimeVLLM,
		GPUType:        "A100",
		GPUCount:       1,
		MemoryMB:       32768,
		Status:         domain.ModelStatusReleased,
	}, nil
}

// mockKubeClient 模拟 k8sadapter：简单内存部署
type mockKubeClient struct {
	deploys   map[string]int32 // name → ready 数
	lastSpec  *clients.CreateDeploymentSpec
	condition string
}

func newMockKube() *mockKubeClient {
	return &mockKubeClient{deploys: make(map[string]int32), condition: "Available"}
}

func (m *mockKubeClient) ListGPUs(ctx context.Context) ([]domain.GPUResource, error) {
	used := int32(0)
	for _, r := range m.deploys {
		used += r
	}
	return []domain.GPUResource{
		{NodeName: "gpu-001", GPUType: "A100", Total: 8, Allocatable: 8, Used: used, MemoryMB: 81920, Utilization: 50, Health: domain.GPUHealthHealthy},
	}, nil
}

func (m *mockKubeClient) CreateDeployment(ctx context.Context, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	m.deploys[spec.Name] = spec.Replicas
	m.lastSpec = spec
	return &clients.K8sDeploymentResult{
		DeploymentID: spec.DeploymentID,
		Status:       &clients.K8sDeploymentStatus{Replicas: spec.Replicas, ReadyReplicas: spec.Replicas, Condition: "Available"},
		Endpoint:     spec.Name + ".tenant-default.svc.cluster.local",
	}, nil
}

func (m *mockKubeClient) UpdateDeployment(ctx context.Context, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	m.deploys[spec.Name] = spec.Replicas
	m.lastSpec = spec
	return m.GetDeployment(ctx, spec.Name, spec.Namespace)
}

func TestDeploymentPassesVerifiedArtifactToAdapter(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	kube := newMockKube()
	useCase := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	_, err := useCase.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{
		IdempotencyKey: "artifact-deploy", Name: "artifact-demo", ModelVersionID: "v1", Replicas: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for kube.lastSpec == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if kube.lastSpec == nil {
		t.Fatal("deployment was not submitted")
	}
	if kube.lastSpec.ArtifactURI != "s3://models/qwen-7b" || kube.lastSpec.ArtifactDigest != "sha256:0123456789abcdef" {
		t.Fatalf("verified artifact metadata not forwarded: %+v", kube.lastSpec)
	}
	if kube.lastSpec.Runtime != domain.RuntimeVLLM || kube.lastSpec.ServingMode != domain.ServingModeUnified {
		t.Fatalf("runtime serving profile not forwarded: %+v", kube.lastSpec)
	}
	if kube.lastSpec.Labels["carrot.ai/template-generation"] != "0" {
		t.Fatalf("initial template generation not forwarded: %+v", kube.lastSpec.Labels)
	}
}

func (m *mockKubeClient) GetDeployment(ctx context.Context, name, namespace string) (*clients.K8sDeploymentResult, error) {
	r, ok := m.deploys[name]
	if !ok {
		return nil, data.ErrNotFound
	}
	return &clients.K8sDeploymentResult{
		DeploymentID: name,
		Status:       &clients.K8sDeploymentStatus{Replicas: r, ReadyReplicas: r, Condition: m.condition, Message: "analysis failed"},
		Endpoint:     name + ".svc.cluster.local",
	}, nil
}

func (m *mockKubeClient) ListDeployments(ctx context.Context, namespace string) ([]*clients.K8sDeploymentResult, error) {
	out := make([]*clients.K8sDeploymentResult, 0, len(m.deploys))
	for name, r := range m.deploys {
		out = append(out, &clients.K8sDeploymentResult{
			DeploymentID: name,
			Status:       &clients.K8sDeploymentStatus{Replicas: r, ReadyReplicas: r, Condition: "Available"},
		})
	}
	return out, nil
}

func (m *mockKubeClient) ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*clients.K8sDeploymentResult, error) {
	m.deploys[name] = replicas
	return &clients.K8sDeploymentResult{
		DeploymentID: name,
		Status:       &clients.K8sDeploymentStatus{Replicas: replicas, ReadyReplicas: replicas, Condition: "Available"},
	}, nil
}

func (m *mockKubeClient) RestartDeployment(ctx context.Context, name, namespace string) (*clients.K8sDeploymentResult, error) {
	return m.GetDeployment(ctx, name, namespace)
}

func (m *mockKubeClient) DeleteDeployment(ctx context.Context, name, namespace string) error {
	delete(m.deploys, name)
	return nil
}

func newTestServer(t *testing.T) (http.Handler, data.DeploymentRepository) {
	t.Helper()
	repo := data.NewMemoryDeploymentRepository()
	models := &mockModelClient{}
	kube := newMockKube()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	quotaStore := data.NewMemoryQuotaStore()
	quotaUse := biz.NewQuotaUseCase(quotaStore, logger)
	auditUse := biz.NewAuditUseCase(data.NewMemoryAuditStore(), logger)
	deployUse := biz.NewDeploymentUseCase(repo, models, kube)
	deployUse.SetQuota(quotaUse)
	deployUse.SetAudit(auditUse)
	resUse := biz.NewResourceUseCase(kube)
	return NewServer(deployUse, resUse, quotaUse, auditUse, repo, logger).Handler(), repo
}

func doJSON(t *testing.T, h http.Handler, method, path string, body interface{}) (*apitypes.Response, int) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp apitypes.Response
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return &resp, rec.Code
}

// 完整流程：创建 → 状态推进 → 扩容 → 删除
func TestDeploymentLifecycle(t *testing.T) {
	h, _ := newTestServer(t)

	// 1. 创建部署
	req := apitypes.CreateDeploymentRequest{
		IdempotencyKey: "deploy-001",
		Name:           "qwen-demo",
		ModelVersionID: "v1",
		Replicas:       1,
	}
	resp, code := doJSON(t, h, http.MethodPost, "/api/v1/deployments", req)
	if code != http.StatusOK {
		t.Fatalf("创建部署失败: %d %s", code, resp.Message)
	}
	d, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("响应异常: %+v", resp.Data)
	}
	// 异步提交流程很快，创建后状态应为非终态（NEW/VALIDATING/SUBMITTING/STARTING）
	initial := d["status"].(string)
	switch initial {
	case "NEW", "VALIDATING", "SUBMITTING", "STARTING":
	default:
		t.Fatalf("创建后状态应非终态: %v", initial)
	}
	id := d["id"].(string)

	// 2. 幂等创建（同 IdempotencyKey）
	resp2, code := doJSON(t, h, http.MethodPost, "/api/v1/deployments", req)
	if code != http.StatusOK {
		t.Fatalf("幂等创建失败: %d", code)
	}
	if id2, ok := resp2.Data.(map[string]interface{})["id"].(string); ok && id2 != id {
		t.Fatalf("幂等创建应返回同一部署: %s != %s", id2, id)
	}

	// 3. 查询（异步提交流程会推进状态）
	deadline := time.Now().Add(5 * time.Second)
	var status string
	for {
		resp, code = doJSON(t, h, http.MethodGet, "/api/v1/deployments/"+id, nil)
		if code != http.StatusOK {
			t.Fatalf("查询失败: %d", code)
		}
		if d, ok := resp.Data.(map[string]interface{}); ok {
			status = d["status"].(string)
			if status == "RUNNING" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("部署未进入 RUNNING: %s", status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != "RUNNING" {
		t.Fatalf("最终状态应为 RUNNING: %s", status)
	}

	// 4. 事件记录
	if d, ok := resp.Data.(map[string]interface{}); ok {
		if evs, ok := d["events"].([]interface{}); ok && len(evs) == 0 {
			t.Error("应有状态事件记录")
		}
	}

	// 5. 扩容
	scaleReq := apitypes.ScaleDeploymentRequest{Replicas: 2}
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/deployments/"+id+"/scale", scaleReq)
	if code != http.StatusOK {
		t.Fatalf("扩容失败: %d %s", code, resp.Message)
	}
	if d, ok := resp.Data.(map[string]interface{}); ok {
		if int32(d["replicas"].(float64)) != 2 {
			t.Fatalf("扩容后副本数错误: %v", d["replicas"])
		}
		if d["status"] != "RUNNING" {
			t.Fatalf("扩容后状态应为 RUNNING: %v", d["status"])
		}
	}

	// 6. 删除
	resp, code = doJSON(t, h, http.MethodDelete, "/api/v1/deployments/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("删除失败: %d %s", code, resp.Message)
	}

	// 7. 幂等删除
	resp, code = doJSON(t, h, http.MethodDelete, "/api/v1/deployments/"+id, nil)
	if code != http.StatusOK {
		t.Fatalf("幂等删除失败: %d %s", code, resp.Message)
	}
}

// GPU 资源查询
func TestGPUResources(t *testing.T) {
	h, _ := newTestServer(t)
	resp, code := doJSON(t, h, http.MethodGet, "/api/v1/resources/gpus", nil)
	if code != http.StatusOK {
		t.Fatalf("查询 GPU 失败: %d", code)
	}
	d, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("响应异常: %+v", resp.Data)
	}
	summary, ok := d["summary"].(map[string]interface{})
	if !ok {
		t.Fatalf("缺少 summary: %+v", d)
	}
	if int32(summary["totalGpu"].(float64)) != 8 {
		t.Fatalf("GPU 总量错误: %v", summary["totalGpu"])
	}
}

func TestDeploymentRollbackUsesPersistedRevision(t *testing.T) {
	h, _ := newTestServer(t)
	resp, code := doJSON(t, h, http.MethodPost, "/api/v1/deployments", apitypes.CreateDeploymentRequest{IdempotencyKey: "rollback-d1", Name: "rollback-demo", ModelVersionID: "v1", Replicas: 1})
	if code != http.StatusOK {
		t.Fatalf("create failed: %d %s", code, resp.Message)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, code = doJSON(t, h, http.MethodGet, "/api/v1/deployments/rollback-d1", nil)
		if code == http.StatusOK && resp.Data.(map[string]interface{})["status"] == "RUNNING" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/deployments/rollback-d1/upgrade", map[string]string{"modelVersionId": "v2"})
	if code != http.StatusOK {
		t.Fatalf("upgrade failed: %d %s", code, resp.Message)
	}
	resp, code = doJSON(t, h, http.MethodGet, "/api/v1/deployments/rollback-d1/revisions", nil)
	if code != http.StatusOK {
		t.Fatalf("revisions failed: %d %s", code, resp.Message)
	}
	revisions := resp.Data.([]interface{})
	if len(revisions) != 2 {
		t.Fatalf("expected 2 revisions: %+v", revisions)
	}
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/deployments/rollback-d1/rollback", map[string]string{})
	if code != http.StatusOK {
		t.Fatalf("rollback failed: %d %s", code, resp.Message)
	}
	deployment := resp.Data.(map[string]interface{})
	if deployment["modelVersionId"] != "v1" {
		t.Fatalf("rollback target mismatch: %+v", deployment)
	}
	resp, code = doJSON(t, h, http.MethodGet, "/api/v1/deployments/rollback-d1/revisions", nil)
	if code != http.StatusOK || len(resp.Data.([]interface{})) != 3 {
		t.Fatalf("rollback revision not persisted: %+v", resp.Data)
	}
}

func TestFailedRolloutRestoresStableRevision(t *testing.T) {
	repo := data.NewMemoryDeploymentRepository()
	kube := newMockKube()
	useCase := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	_, err := useCase.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{IdempotencyKey: "failed-rollout", Name: "failed-rollout", ModelVersionID: "v1", Replicas: 1})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := repo.Get("failed-rollout")
		if d.Status == domain.DeploymentStatusRunning {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	kube.condition = "ReplicaFailure"
	result, err := useCase.UpgradeDeployment(context.Background(), "failed-rollout", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if kube.lastSpec.Labels["carrot.ai/template-generation"] != "1" {
		t.Fatalf("upgrade generation not forwarded: %+v", kube.lastSpec.Labels)
	}
	if result.Status != domain.DeploymentStatusFailed || result.ModelVersionID != "v1" {
		t.Fatalf("stable revision was not restored: %+v", result)
	}
	revisions, err := repo.Revisions("failed-rollout")
	if err != nil || len(revisions) != 2 || revisions[0].ModelVersionID != "v1" {
		t.Fatalf("restored revision missing: %+v err=%v", revisions, err)
	}
}

// 不存在的部署查询 → 404
func TestDeploymentNotFound(t *testing.T) {
	h, _ := newTestServer(t)
	_, code := doJSON(t, h, http.MethodGet, "/api/v1/deployments/nonexist", nil)
	if code != http.StatusNotFound {
		t.Fatalf("不存在部署应返回 404: %d", code)
	}
}

func TestJWTIntrospectionReturnsTenantWorkspaceIdentity(t *testing.T) {
	const secret = "jupyterhub-introspection-secret"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(managementIdentity(t, secret))
	token, err := platformauth.IssueAccessToken(secret, "user-1", "tenant-a", platformauth.RoleDeveloper, "controlplane", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/introspect", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["active"] != true || body["sub"] != "user-1" || body["tenantId"] != "tenant-a" || body["role"] != string(platformauth.RoleDeveloper) {
		t.Fatalf("introspection mismatch: %+v", body)
	}

	bad := httptest.NewRecorder()
	srv.Handler().ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/api/v1/auth/introspect", bytes.NewBufferString(`{"token":"invalid"}`)))
	body = nil
	_ = json.NewDecoder(bad.Body).Decode(&body)
	if body["active"] != false {
		t.Fatalf("invalid token must be inactive: %+v", body)
	}
}

func TestClusterRegistrationAndAgentHeartbeat(t *testing.T) {
	const secret = "cluster-api-test-secret"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(managementIdentity(t, secret))
	srv.SetClusterService(mustClusterService(t))
	admin, err := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"gpu-west","name":"west","endpoint":"https://k8s.example.test","adapterUrl":"http://adapter-west:8082","kubeconfig":"private-kubeconfig","labels":{"region":"west"},"supportedRuntimes":["vLLM"]}`
	register := httptest.NewRequest(http.MethodPost, "/api/v1/clusters", bytes.NewBufferString(body))
	register.Header.Set("Authorization", "Bearer "+admin)
	registered := httptest.NewRecorder()
	srv.Handler().ServeHTTP(registered, register)
	if registered.Code != http.StatusOK || bytes.Contains(registered.Body.Bytes(), []byte("private-kubeconfig")) {
		t.Fatalf("cluster registration failed or leaked credentials: status=%d body=%s", registered.Code, registered.Body.String())
	}
	updateRoute := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/gpu-west/serving-route", bytes.NewBufferString(`{"servingUrlTemplate":"https://{service}.{namespace}.west.example.test"}`))
	updateRoute.Header.Set("Authorization", "Bearer "+admin)
	updatedRoute := httptest.NewRecorder()
	srv.Handler().ServeHTTP(updatedRoute, updateRoute)
	if updatedRoute.Code != http.StatusOK {
		t.Fatalf("serving route update failed: %d %s", updatedRoute.Code, updatedRoute.Body.String())
	}
	cluster, err := srv.clusters.Get("gpu-west")
	if err != nil || cluster.ServingURLTemplate != "https://{service}.{namespace}.west.example.test" {
		t.Fatalf("template not persisted: %+v %v", cluster, err)
	}

	tokenReq := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/gpu-west/agent-token", nil)
	tokenReq.Header.Set("Authorization", "Bearer "+admin)
	tokenRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(tokenRes, tokenReq)
	var issued struct {
		Data struct {
			Token     string    `json:"token"`
			ExpiresAt time.Time `json:"expiresAt"`
		} `json:"data"`
	}
	if err := json.NewDecoder(tokenRes.Body).Decode(&issued); err != nil {
		t.Fatal(err)
	}
	if tokenRes.Code != http.StatusOK || issued.Data.Token == "" || tokenRes.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token issuance failed: %d", tokenRes.Code)
	}
	agent := issued.Data.Token
	if _, err := srv.identity.Authenticate(agent); err == nil {
		t.Fatal("agent token authorized for controlplane management")
	}
	if _, err := srv.identity.AuthenticateClusterAgent(agent, "gpu-east"); err == nil {
		t.Fatal("agent token authorized for another cluster")
	}
	heartbeat := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/gpu-west/heartbeat", bytes.NewBufferString(`{"healthStatus":"healthy","gpuCapacity":[{"gpuType":"H100","total":8,"allocatable":7,"used":1}],"deploymentGpu":[{"deploymentId":"deploy-a","tenantId":"tenant-a","namespace":"tenant-a","nodeName":"node-a","gpuType":"H100","gpuCount":1}],"volcanoQueues":[{"name":"tenant-a","state":"Open","capability":{"nvidia.com/gpu":"8"},"allocated":{"nvidia.com/gpu":"1"},"pending":2}]}`))
	heartbeat.Header.Set("Authorization", "Bearer "+agent)
	reported := httptest.NewRecorder()
	srv.Handler().ServeHTTP(reported, heartbeat)
	if reported.Code != http.StatusOK {
		t.Fatalf("heartbeat rejected: status=%d body=%s", reported.Code, reported.Body.String())
	}
	clusters, err := srv.clusters.List()
	if err != nil || len(clusters) != 1 || clusters[0].HealthStatus != "healthy" || clusters[0].GPUCapacity[0].Used != 1 {
		t.Fatalf("heartbeat not stored: %+v err=%v", clusters, err)
	}
	if len(clusters[0].VolcanoQueues) != 1 || clusters[0].VolcanoQueues[0].Pending != 2 || clusters[0].VolcanoQueues[0].Allocated["nvidia.com/gpu"] != "1" {
		t.Fatalf("queue heartbeat not stored: %+v", clusters[0])
	}
	if len(clusters[0].DeploymentGPU) != 1 || clusters[0].DeploymentGPU[0].DeploymentID != "deploy-a" {
		t.Fatalf("attribution not stored: %+v", clusters[0])
	}
	telemetryReq := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/gpu-west/heartbeat", bytes.NewBufferString(`{"healthStatus":"healthy","gpuCapacity":[],"telemetry":{"status":"unhealthy","reason":"Prometheus unavailable","collectedAt":"2026-09-26T00:00:00Z"}}`))
	telemetryReq.Header.Set("Authorization", "Bearer "+agent)
	telemetryRes := httptest.NewRecorder()
	srv.Handler().ServeHTTP(telemetryRes, telemetryReq)
	if telemetryRes.Code != http.StatusOK {
		t.Fatalf("telemetry heartbeat rejected: %s", telemetryRes.Body.String())
	}
	c, err := srv.clusters.Get("gpu-west")
	if err != nil || c.Telemetry == nil || c.Telemetry.Status != "unhealthy" || c.HealthStatus != "healthy" {
		t.Fatalf("telemetry heartbeat not stored: %+v %v", c, err)
	}
	if len(c.DeploymentGPU) != 0 {
		t.Fatal("omitted attribution retained old data")
	}
}

func TestClusterManagementRequiresAdminAndProtectsCredentials(t *testing.T) {
	const secret = "cluster-management-test-secret"
	repo := data.NewMemoryDeploymentRepository()
	srv := NewServer(biz.NewDeploymentUseCase(repo, nil, nil), nil, nil, nil, repo, slog.Default())
	srv.SetIdentityService(managementIdentity(t, secret))
	srv.SetClusterService(mustClusterService(t))
	if _, err := srv.clusters.Register("gpu-west", "west", "https://k8s.example.test", "http://adapter-west:8082", "old-secret", nil, []string{"vLLM"}, nil, ""); err != nil {
		t.Fatal(err)
	}
	admin, _ := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	viewer, _ := platformauth.IssueAccessToken(secret, "viewer", "tenant-a", platformauth.RoleViewer, "controlplane", time.Now(), time.Hour)
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}
	policyPath := "/api/v1/clusters/gpu-west/placement-policy"
	policy := `{"labels":{"region":"west"},"supportedRuntimes":["Triton"],"allowedTenants":["tenant-a"]}`
	if got := call(http.MethodPut, policyPath, policy, viewer); got.Code != http.StatusUnauthorized {
		t.Fatalf("viewer updated policy: %d", got.Code)
	}
	if got := call(http.MethodPut, policyPath, policy, admin); got.Code != http.StatusOK {
		t.Fatalf("policy update: %d %s", got.Code, got.Body.String())
	}
	cluster, err := srv.clusters.Get("gpu-west")
	if err != nil || cluster.Labels["region"] != "west" || cluster.SupportedRuntimes[0] != "Triton" {
		t.Fatalf("policy not updated: %+v %v", cluster, err)
	}
	credentialPath := "/api/v1/clusters/gpu-west/credentials"
	if got := call(http.MethodPut, credentialPath, `{"kubeconfig":"new-secret"}`, viewer); got.Code != http.StatusUnauthorized {
		t.Fatalf("viewer rotated credentials: %d", got.Code)
	}
	if got := call(http.MethodPut, credentialPath, `{"kubeconfig":"new-secret"}`, admin); got.Code != http.StatusOK || bytes.Contains(got.Body.Bytes(), []byte("new-secret")) || got.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("credential rotation: %d %s", got.Code, got.Body.String())
	}
	if got, err := srv.clusters.DecryptCredentials("gpu-west"); err != nil || got != "new-secret" {
		t.Fatalf("credentials not updated: %q %v", got, err)
	}
	if got := call(http.MethodDelete, "/api/v1/clusters/gpu-west", "", viewer); got.Code != http.StatusUnauthorized {
		t.Fatalf("viewer deleted cluster: %d", got.Code)
	}
	d := &domain.ModelDeployment{ID: "active-a", Name: "active-a", ClusterID: "gpu-west", Status: domain.DeploymentStatusFailed}
	if err := repo.Create(d); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodDelete, "/api/v1/clusters/gpu-west", "", admin); got.Code != http.StatusConflict {
		t.Fatalf("cluster with deployment deleted: %d %s", got.Code, got.Body.String())
	}
	d.Status = domain.DeploymentStatusDeleted
	if err := repo.Update(d); err != nil {
		t.Fatal(err)
	}
	if got := call(http.MethodDelete, "/api/v1/clusters/gpu-west", "", admin); got.Code != http.StatusOK {
		t.Fatalf("cluster deletion: %d %s", got.Code, got.Body.String())
	}
}

func TestClusterHeartbeatRejectsWrongAgentAudience(t *testing.T) {
	const secret = "cluster-api-test-secret"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(identity.NewService(secret))
	srv.SetClusterService(mustClusterService(t))
	wrong, _ := platformauth.IssueAccessToken(secret, "gpu-west", "", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/gpu-west/heartbeat", bytes.NewBufferString(`{"healthStatus":"healthy"}`))
	req.Header.Set("Authorization", "Bearer "+wrong)
	res := httptest.NewRecorder()
	srv.Handler().ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("wrong audience token accepted: status=%d", res.Code)
	}
}

func TestClusterAgentTokenRequiresAdminAndExistingCluster(t *testing.T) {
	const secret = "cluster-token-test-secret"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	srv.SetIdentityService(managementIdentity(t, secret))
	srv.SetClusterService(mustClusterService(t))
	for _, tc := range []struct {
		role platformauth.Role
		want int
	}{
		{platformauth.RoleTenantAdmin, http.StatusUnauthorized},
		{platformauth.RoleDeveloper, http.StatusUnauthorized},
		{platformauth.RoleViewer, http.StatusUnauthorized},
		{platformauth.RolePlatformAdmin, http.StatusNotFound},
	} {
		t.Run(string(tc.role), func(t *testing.T) {
			if err := srv.identity.SetMember("user", "tenant-a", tc.role); err != nil {
				t.Fatal(err)
			}
			token, err := platformauth.IssueAccessToken(secret, "user", "tenant-a", tc.role, "controlplane", time.Now(), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/missing/agent-token", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", res.Code, tc.want, res.Body.String())
			}
			if tc.role != platformauth.RolePlatformAdmin {
				update := httptest.NewRequest(http.MethodPut, "/api/v1/clusters/missing/serving-route", bytes.NewBufferString(`{"servingUrlTemplate":"https://{service}.{namespace}.example.test"}`))
				update.Header.Set("Authorization", "Bearer "+token)
				got := httptest.NewRecorder()
				srv.Handler().ServeHTTP(got, update)
				if got.Code != http.StatusUnauthorized {
					t.Fatalf("unauthorized route update status=%d", got.Code)
				}
			}
		})
	}
}

func TestTenantAdminCannotGrantOrModifyPlatformAdmin(t *testing.T) {
	const secret = "member-authorization-test"
	srv := NewServer(nil, nil, nil, nil, nil, slog.Default())
	svc := identity.NewService(secret)
	srv.SetIdentityService(svc)
	for _, user := range []string{"platform", "tenant-admin", "developer"} {
		if _, err := svc.CreateUser(user, user+"@example.test", "correct horse battery staple"); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SetMember("platform", "tenant-a", platformauth.RolePlatformAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMember("tenant-admin", "tenant-a", platformauth.RoleTenantAdmin); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		actor, target   string
		actorRole, role platformauth.Role
		want            int
	}{
		{"tenant-admin", "tenant-admin", platformauth.RoleTenantAdmin, platformauth.RolePlatformAdmin, http.StatusUnauthorized},
		{"tenant-admin", "platform", platformauth.RoleTenantAdmin, platformauth.RoleViewer, http.StatusUnauthorized},
		{"tenant-admin", "developer", platformauth.RoleTenantAdmin, platformauth.RoleDeveloper, http.StatusOK},
		{"platform", "developer", platformauth.RolePlatformAdmin, platformauth.RolePlatformAdmin, http.StatusOK},
	} {
		t.Run(tc.actor+"-"+tc.target+"-"+string(tc.role), func(t *testing.T) {
			token, err := platformauth.IssueAccessToken(secret, tc.actor, "tenant-a", tc.actorRole, "controlplane", time.Now(), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/tenant-a/members/"+tc.target, bytes.NewBufferString(`{"role":"`+string(tc.role)+`"}`))
			req.Header.Set("Authorization", "Bearer "+token)
			res := httptest.NewRecorder()
			srv.Handler().ServeHTTP(res, req)
			if res.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", res.Code, tc.want, res.Body.String())
			}
		})
	}
}

func TestDeploymentSelectsClusterAndCallsItsAdapter(t *testing.T) {
	called := make(chan clients.CreateDeploymentSpec, 1)
	synced := make(chan struct{}, 1)
	routes := &recordedGateway{registered: make(chan routeURLs, 4), unregistered: make(chan string, 1)}
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-a/provision" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
			return
		}
		if r.Method == http.MethodGet {
			select {
			case synced <- struct{}{}:
			default:
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": clients.K8sDeploymentResult{Name: "cluster-aware", Endpoint: "cluster-aware.tenant-tenant-a.svc.cluster.local", StableEndpoint: "cluster-aware-stable.tenant-tenant-a.svc.cluster.local", CanaryEndpoint: "cluster-aware-canary.tenant-tenant-a.svc.cluster.local", RolloutStatus: "Healthy", Status: &clients.K8sDeploymentStatus{Replicas: 2, ReadyReplicas: 2, AvailableReplicas: 2, Condition: "Available"}}})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/deployments" {
			http.NotFound(w, r)
			return
		}
		var spec clients.CreateDeploymentSpec
		if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
			t.Error(err)
		}
		called <- spec
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": clients.K8sDeploymentResult{DeploymentID: spec.DeploymentID, Name: spec.Name, Endpoint: "model.tenant-default.svc.cluster.local", Status: &clients.K8sDeploymentStatus{Replicas: spec.Replicas, ReadyReplicas: spec.Replicas, AvailableReplicas: spec.Replicas, Condition: "Available"}}})
	}))
	defer adapter.Close()
	clusterService := mustClusterService(t)
	if _, err := clusterService.Register("gpu-west", "west", "https://k8s-west.example.test", adapter.URL, "kubeconfig", nil, []string{domain.RuntimeVLLM}, nil, "https://{service}.{namespace}.west.example.test"); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	kube := newMockKube()
	useCase := biz.NewDeploymentUseCase(repo, &mockModelClient{}, kube)
	useCase.SetClusterPlacement(clusterService, clients.NewClusterAdapterPool(clusterService, clients.NewK8sAdapterClient("http://unused.invalid")))
	useCase.SetGateway(routes)
	created, err := useCase.CreateDeployment(context.Background(), &apitypes.CreateDeploymentRequest{IdempotencyKey: "cluster-aware", Name: "cluster-aware", TenantID: "tenant-a", ModelVersionID: "v1", Replicas: 2})
	if err != nil {
		t.Fatal(err)
	}
	if created.ClusterID != "gpu-west" {
		t.Fatalf("deployment placed on %q", created.ClusterID)
	}
	select {
	case spec := <-called:
		if spec.ClusterID != "gpu-west" || spec.Name != created.Name {
			t.Fatalf("cluster assignment not forwarded to adapter: %+v", spec)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("selected cluster adapter was not called")
	}
	select {
	case <-synced:
	case <-time.After(3 * time.Second):
		t.Fatal("deployment status was not read from selected adapter")
	}
	select {
	case route := <-routes.registered:
		if route.endpoint != "https://cluster-aware.tenant-tenant-a.west.example.test" || route.stable != "https://cluster-aware-stable.tenant-tenant-a.west.example.test" || route.canary != "https://cluster-aware-canary.tenant-tenant-a.west.example.test" {
			t.Fatalf("gateway got unreachable endpoints: %+v", route)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("gateway route not registered")
	}
	stored, err := repo.Get(created.ID)
	if err != nil || stored.Endpoint != "https://cluster-aware.tenant-tenant-a.west.example.test" {
		t.Fatalf("stored endpoint=%q err=%v", stored.Endpoint, err)
	}
	if err := clusterService.SetServingURLTemplate("gpu-west", "https://{service}.{namespace}.new-west.example.test"); err != nil {
		t.Fatal(err)
	}
	useCase.SyncFromK8s(context.Background(), created.ID)
	select {
	case route := <-routes.registered:
		if route.endpoint != "https://cluster-aware.tenant-tenant-a.new-west.example.test" {
			t.Fatalf("gateway route not reconciled: %+v", route)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("updated ingress route was not registered")
	}
	if err := clusterService.UpdatePolicy("gpu-west", nil, []string{domain.RuntimeVLLM}, []string{"tenant-b"}); err != nil {
		t.Fatal(err)
	}
	useCase.SyncFromK8s(context.Background(), created.ID)
	if current, err := repo.Get(created.ID); err != nil || current.Status != domain.DeploymentStatusRunning {
		t.Fatalf("policy change disrupted running deployment: %+v %v", current, err)
	}
	select {
	case <-routes.unregistered:
		t.Fatal("policy change removed an existing route")
	default:
	}
	if err := clusterService.Report("gpu-west", "unhealthy", nil); err != nil {
		t.Fatal(err)
	}
	useCase.SyncFromK8s(context.Background(), created.ID)
	select {
	case name := <-routes.unregistered:
		if name != created.Name {
			t.Fatalf("wrong route removed: %s", name)
		}
	default:
		t.Fatal("failed cluster kept gateway route")
	}
	failed, err := repo.Get(created.ID)
	if err != nil || failed.Status != domain.DeploymentStatusFailed {
		t.Fatalf("deployment was not failed after cluster outage: %+v %v", failed, err)
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	useCase.SyncFromK8s(context.Background(), created.ID)
	select {
	case route := <-routes.registered:
		t.Fatalf("failed deployment was silently restored: %+v", route)
	default:
	}
}

func TestManualClusterRebuildRequiresAdminAndSubmitsToTarget(t *testing.T) {
	const secret = "cluster-rebuild-secret"
	called := make(chan clients.CreateDeploymentSpec, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/tenants/tenant-a/provision" {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/v1/deployments" {
			var spec clients.CreateDeploymentSpec
			_ = json.NewDecoder(r.Body).Decode(&spec)
			called <- spec
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": clients.K8sDeploymentResult{Name: spec.Name, Endpoint: "model.svc", Status: &clients.K8sDeploymentStatus{ReadyReplicas: spec.Replicas, Replicas: spec.Replicas, Condition: "Available"}}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": clients.K8sDeploymentResult{Name: "model-a", Endpoint: "model.svc", Status: &clients.K8sDeploymentStatus{ReadyReplicas: 1, Replicas: 1, Condition: "Available"}}})
	}))
	defer target.Close()
	clusterService := mustClusterService(t)
	for _, tc := range []struct{ id, url string }{{"gpu-west", target.URL}, {"gpu-east", target.URL}} {
		if _, err := clusterService.Register(tc.id, tc.id, "https://k8s.example.test", tc.url, "secret", nil, []string{domain.RuntimeVLLM}, nil, "https://{service}.{namespace}.example.test"); err != nil {
			t.Fatal(err)
		}
	}
	if err := clusterService.Report("gpu-west", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.ReserveCapacity("gpu-west", clusters.GPUReservation{DeploymentID: "deployment-a", TenantID: "tenant-a", Namespace: "tenant-tenant-a", ModelVersionID: "v1", GPUType: "A100", GPUCount: 1}, domain.RuntimeVLLM); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-west", "unhealthy", nil); err != nil {
		t.Fatal(err)
	}
	if err := clusterService.Report("gpu-east", "healthy", []clusters.Capacity{{GPUType: "A100", Total: 8, Allocatable: 8}}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewMemoryDeploymentRepository()
	if err := repo.Create(&domain.ModelDeployment{ID: "deployment-a", Name: "model-a", ModelVersionID: "v1", ModelID: "m1", ModelVersion: "v1", TenantID: "tenant-a", Namespace: "tenant-tenant-a", ClusterID: "gpu-west", Runtime: domain.RuntimeVLLM, Replicas: 1, Resource: domain.Resource{GPUType: "A100", GPUCount: 1}, Status: domain.DeploymentStatusFailed, Diagnostics: "目标集群不可用: heartbeat unhealthy"}); err != nil {
		t.Fatal(err)
	}
	uc := biz.NewDeploymentUseCase(repo, &mockModelClient{}, newMockKube())
	uc.SetClusterPlacement(clusterService, clients.NewClusterAdapterPool(clusterService, clients.NewK8sAdapterClient("http://unused.invalid")))
	srv := NewServer(uc, nil, nil, nil, repo, slog.Default())
	srv.SetIdentityService(managementIdentity(t, secret))
	admin, _ := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Hour)
	viewer, _ := platformauth.IssueAccessToken(secret, "viewer", "tenant-a", platformauth.RoleViewer, "controlplane", time.Now(), time.Hour)
	call := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/deployments/deployment-a/rebuild", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		srv.Handler().ServeHTTP(res, req)
		return res
	}
	valid := `{"targetClusterId":"gpu-east","acknowledgeOrphanedResources":true}`
	if got := call(viewer, valid); got.Code != http.StatusUnauthorized {
		t.Fatalf("viewer rebuilt: %d", got.Code)
	}
	if got := call(admin, `{"targetClusterId":"gpu-east"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("missing acknowledgement accepted: %d", got.Code)
	}
	if got := call(admin, valid); got.Code != http.StatusOK {
		t.Fatalf("rebuild rejected: %d %s", got.Code, got.Body.String())
	}
	if got := call(admin, valid); got.Code == http.StatusOK {
		t.Fatal("duplicate rebuild accepted")
	}
	west, _ := clusterService.Get("gpu-west")
	east, _ := clusterService.Get("gpu-east")
	if len(west.GPUReservations) != 1 || len(east.GPUReservations) != 1 || east.GPUReservations[0].TemplateGeneration != 1 || east.GPUReservations[0].ModelVersionID != "v1" {
		t.Fatalf("rebuild reservations: old=%+v target=%+v", west.GPUReservations, east.GPUReservations)
	}
	select {
	case spec := <-called:
		if spec.ClusterID != "gpu-east" || spec.DeploymentID != "deployment-a" {
			t.Fatalf("wrong target: %+v", spec)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("target adapter was not called")
	}
}

type routeURLs struct{ endpoint, stable, canary string }
type recordedGateway struct {
	registered   chan routeURLs
	unregistered chan string
}

func (g *recordedGateway) RegisterRoute(_ context.Context, _, _, endpoint, _, _, _, stable, canary, _ string) error {
	g.registered <- routeURLs{endpoint, stable, canary}
	return nil
}
func (g *recordedGateway) UnregisterRoute(_ context.Context, model string) error {
	if g.unregistered != nil {
		g.unregistered <- model
	}
	return nil
}

func mustClusterService(t *testing.T) *clusters.Service {
	t.Helper()
	service, err := clusters.NewService(clusters.NewMemoryRepository(), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return service
}
