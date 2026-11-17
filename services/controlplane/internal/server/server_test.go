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
	srv.SetIdentityService(identity.NewService(secret))
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
