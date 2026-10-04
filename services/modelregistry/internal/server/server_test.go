package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"kk-infra/lib/apitypes"
	"kk-infra/services/modelregistry/internal/biz"
	"kk-infra/services/modelregistry/internal/data"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	repo := data.NewMemoryRepository()
	registry := biz.NewRegistry(repo)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	return NewServer(registry, logger).Handler()
}

func newProtectedTestServer(t *testing.T) http.Handler {
	t.Helper()
	repo := data.NewMemoryRepository()
	registry := biz.NewRegistry(repo)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	server := NewServer(registry, logger)
	server.SetPipelineToken("pipeline-secret")
	return server.Handler()
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

// 完整流程：注册模型 → 创建版本 → 校验 → 查询
func TestModelLifecycle(t *testing.T) {
	h := newTestServer(t)

	// 1. 创建模型
	resp, code := doJSON(t, h, http.MethodPost, "/api/v1/models",
		apitypes.CreateModelRequest{Name: "qwen", Description: "Qwen 系列"})
	if code != http.StatusOK {
		t.Fatalf("创建模型失败: %d %s", code, resp.Message)
	}
	m, ok := resp.Data.(map[string]interface{})
	if !ok || m["id"] == "" {
		t.Fatalf("响应缺少模型 id: %+v", resp.Data)
	}
	modelID := m["id"].(string)

	// 2. 创建版本
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions",
		apitypes.CreateModelVersionRequest{
			Version:     "7b",
			ArtifactURI: "s3://models/qwen-7b",
			Runtime:     "vLLM",
			GPUType:     "A100",
			GPUCount:    1,
			MemoryMB:    32768,
		})
	if code != http.StatusOK {
		t.Fatalf("创建版本失败: %d %s", code, resp.Message)
	}
	v, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("响应缺少版本: %+v", resp.Data)
	}
	versionID := v["id"].(string)
	if v["status"] != "REGISTERED" {
		t.Fatalf("新版本状态应为 REGISTERED: %v", v["status"])
	}

	// 3. 校验版本
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/validate", nil)
	if code != http.StatusOK {
		t.Fatalf("校验版本失败: %d %s", code, resp.Message)
	}

	// 4. 查询版本
	resp, code = doJSON(t, h, http.MethodGet, "/api/v1/versions/"+versionID, nil)
	if code != http.StatusOK {
		t.Fatalf("查询版本失败: %d %s", code, resp.Message)
	}
	if v2, ok := resp.Data.(map[string]interface{}); ok {
		if v2["status"] != "VALIDATED" {
			t.Fatalf("校验后状态应为 VALIDATED: %v", v2["status"])
		}
		if v2["artifactDigest"] == "" || v2["artifactVerifiedAt"] == nil {
			t.Fatalf("校验后应持久化 artifact 元数据: %+v", v2)
		}
	} else {
		t.Fatalf("查询版本响应异常: %+v", resp.Data)
	}

	// 5. 重名校验失败
	resp, code = doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/validate", nil)
	if code != http.StatusConflict {
		t.Fatalf("重复校验应冲突: %d", code)
	}
}

// 名称唯一性
func TestModelNameUnique(t *testing.T) {
	h := newTestServer(t)
	_, code := doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "dup"})
	if code != http.StatusOK {
		t.Fatalf("首次创建失败: %d", code)
	}
	_, code = doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "dup"})
	if code != http.StatusConflict {
		t.Fatalf("重名应返回 409: %d", code)
	}
}

func TestReleaseRequiresPipelineToken(t *testing.T) {
	h := newProtectedTestServer(t)
	resp, _ := doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "protected"})
	modelID := resp.Data.(map[string]interface{})["id"].(string)
	resp, _ = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions", apitypes.CreateModelVersionRequest{Version: "1", ArtifactURI: "s3://models/protected.tar", GPUType: "A100", GPUCount: 1})
	versionID := resp.Data.(map[string]interface{})["id"].(string)
	_, _ = doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/validate", nil)
	_, code := doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/release", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("release without pipeline token should be unauthorized: %d", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/versions/"+versionID+"/release", nil)
	req.Header.Set("X-Pipeline-Token", "pipeline-secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("pipeline release failed: %d %s", rec.Code, rec.Body.String())
	}
}

// 版本号唯一性
func TestVersionUnique(t *testing.T) {
	h := newTestServer(t)
	resp, _ := doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "m"})
	modelID := resp.Data.(map[string]interface{})["id"].(string)

	body := apitypes.CreateModelVersionRequest{
		Version: "1.0", ArtifactURI: "s3://x/model", Runtime: "vLLM", GPUType: "A100", GPUCount: 1, MemoryMB: 1024,
	}
	_, code := doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions", body)
	if code != http.StatusOK {
		t.Fatalf("首次创建版本失败: %d", code)
	}
	_, code = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions", body)
	if code != http.StatusConflict {
		t.Fatalf("同版本号应返回 409: %d", code)
	}
}

// 必填字段校验
func TestVersionRequiredFields(t *testing.T) {
	h := newTestServer(t)
	resp, _ := doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "m2"})
	modelID := resp.Data.(map[string]interface{})["id"].(string)

	// 缺权重地址
	_, code := doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions",
		apitypes.CreateModelVersionRequest{Version: "1.0", Runtime: "vLLM", GPUType: "A100", GPUCount: 1, MemoryMB: 1024})
	if code != http.StatusBadRequest {
		t.Fatalf("缺权重地址应返回 400: %d", code)
	}

	// 非 vLLM 运行时
	_, code = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions",
		apitypes.CreateModelVersionRequest{Version: "1.0", ArtifactURI: "s3://x/model", Runtime: "Triton", GPUType: "A100", GPUCount: 1, MemoryMB: 1024})
	if code != http.StatusBadRequest {
		t.Fatalf("非 vLLM 应返回 400: %d", code)
	}

	_, code = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions",
		apitypes.CreateModelVersionRequest{Version: "2.0", ArtifactURI: "https://example.com/model", Runtime: "vLLM", GPUType: "A100", GPUCount: 1})
	if code != http.StatusBadRequest {
		t.Fatalf("非 S3 artifact 应返回 400: %d", code)
	}
}

// 删除保护：已校验版本不可删
func TestDeleteProtectedVersion(t *testing.T) {
	h := newTestServer(t)
	resp, _ := doJSON(t, h, http.MethodPost, "/api/v1/models", apitypes.CreateModelRequest{Name: "m3"})
	modelID := resp.Data.(map[string]interface{})["id"].(string)

	resp, _ = doJSON(t, h, http.MethodPost, "/api/v1/models/"+modelID+"/versions",
		apitypes.CreateModelVersionRequest{Version: "1.0", ArtifactURI: "s3://x/model", Runtime: "vLLM", GPUType: "A100", GPUCount: 1, MemoryMB: 1024})
	versionID := resp.Data.(map[string]interface{})["id"].(string)

	_, code := doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/validate", nil)
	if code != http.StatusOK {
		t.Fatalf("校验失败: %d", code)
	}

	_, code = doJSON(t, h, http.MethodDelete, "/api/v1/models/"+modelID+"/versions/1.0", nil)
	if code != http.StatusConflict {
		t.Fatalf("已校验版本删除应冲突: %d", code)
	}

	_, code = doJSON(t, h, http.MethodDelete, "/api/v1/models/"+modelID, nil)
	if code != http.StatusConflict {
		t.Fatalf("包含受保护版本的模型删除应冲突: %d", code)
	}

	_, code = doJSON(t, h, http.MethodPost, "/api/v1/versions/"+versionID+"/release", nil)
	if code != http.StatusOK {
		t.Fatalf("发布失败: %d", code)
	}
	_, code = doJSON(t, h, http.MethodDelete, "/api/v1/models/"+modelID+"/versions/1.0", nil)
	if code != http.StatusConflict {
		t.Fatalf("已发布版本删除应冲突: %d", code)
	}
}
