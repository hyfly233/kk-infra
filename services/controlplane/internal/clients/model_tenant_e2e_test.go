package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
)

func TestModelTenantBinaryLifecycle(t *testing.T) {
	registry, cp, pipeline := os.Getenv("MODEL_E2E_REGISTRY"), os.Getenv("MODEL_E2E_CP"), os.Getenv("MODEL_E2E_PIPELINE")
	if registry == "" || cp == "" || pipeline == "" {
		t.Skip("isolated binaries not configured")
	}
	const secret = "local-model-tenant-test-secret"
	call := func(base, method, path, token, body string, want int) map[string]any {
		t.Helper()
		r, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %+v", method, path, resp.StatusCode, result)
		}
		return result
	}
	call(cp, "POST", "/api/v1/auth/bootstrap", "", `{"id":"admin","email":"admin@example.test","password":"Local-only-pass123!","tenantId":"tenant-a"}`, 200)
	login := call(cp, "POST", "/api/v1/auth/login", "", `{"email":"admin@example.test","password":"Local-only-pass123!","tenantId":"tenant-a"}`, 200)
	admin := login["data"].(map[string]any)["accessToken"].(string)
	dev, _ := platformauth.IssueAccessToken(secret, "developer", "tenant-a", platformauth.RoleDeveloper, "controlplane", time.Now(), time.Minute)
	other, _ := platformauth.IssueAccessToken(secret, "other", "tenant-b", platformauth.RoleTenantAdmin, "controlplane", time.Now(), time.Minute)
	call(registry, "GET", "/api/v1/models", "", "", 401)
	model := call(registry, "POST", "/api/v1/models", dev, `{"name":"tenant-model","tenantId":"tenant-b"}`, 200)["data"].(map[string]any)
	if model["tenantId"] != "tenant-a" {
		t.Fatal("model tenant chosen by request")
	}
	version := call(registry, "POST", "/api/v1/models/"+model["id"].(string)+"/versions", dev, `{"version":"v1","artifactUri":"s3://fixture/weights","runtime":"vLLM","gpuType":"A100","gpuCount":1,"memoryMB":1024}`, 200)["data"].(map[string]any)
	vid := version["id"].(string)
	call(registry, "GET", "/api/v1/versions/"+vid, other, "", 404)
	call(registry, "POST", "/api/v1/versions/"+vid+"/release", admin, "", 401)
	call(pipeline, "POST", "/api/v1/releases", other, `{"modelVersionId":"`+vid+`","tenantId":"tenant-a"}`, 401)
	// A separately validated version is checked again by the pipeline, not bypassed.
	call(registry, "POST", "/api/v1/versions/"+vid+"/validate", dev, "", 200)
	release := call(pipeline, "POST", "/api/v1/releases", dev, `{"modelVersionId":"`+vid+`","tenantId":"tenant-b"}`, 200)["data"].(map[string]any)
	if release["tenantId"] != "tenant-a" || release["status"] != "PENDING_APPROVAL" {
		t.Fatalf("release not gated: %+v", release)
	}
	rid := release["id"].(string)
	call(pipeline, "POST", "/api/v1/releases/"+rid+"/approval", dev, `{"approved":true}`, 401)
	call(pipeline, "POST", "/api/v1/releases/"+rid+"/approval", other, `{"approved":true}`, 401)
	call(pipeline, "POST", "/api/v1/releases/"+rid+"/approval", admin, `{"approved":true}`, 200)
	c := NewModelRegistryClient(registry)
	c.SetServiceIdentity(secret, "modelregistry")
	metadata, err := c.GetVersion(context.Background(), vid)
	if err != nil || metadata.TenantID != "tenant-a" || metadata.Status != "RELEASED" {
		t.Fatalf("version=%+v err=%v", metadata, err)
	}
	body := `{"idempotencyKey":"tenant-e2e","name":"tenant-e2e","modelVersionId":"` + vid + `","tenantId":"tenant-b","replicas":1}`
	call(cp, "POST", "/api/v1/deployments", other, body, 401)
	call(cp, "POST", "/api/v1/deployments", admin, strings.ReplaceAll(body, "tenant-b", "tenant-a"), 200)
	deadline, running := time.Now().Add(15*time.Second), false
	for time.Now().Before(deadline) {
		deployment := call(cp, "GET", "/api/v1/deployments/tenant-e2e", admin, "", 200)["data"].(map[string]any)
		// Detail responses wrap the deployment in deployment.
		if nested, ok := deployment["deployment"].(map[string]any); ok {
			deployment = nested
		}
		if deployment["status"] == "RUNNING" {
			running = true
			break
		}
		if deployment["status"] == "FAILED" {
			t.Fatalf("Fake deployment failed: %+v", deployment)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !running {
		t.Fatal("Fake deployment never became RUNNING")
	}
	call(cp, "DELETE", "/api/v1/deployments/tenant-e2e", admin, "", 200)
	// Real HTTP role changes invalidate the original administrator access token.
	refreshToken := login["data"].(map[string]any)["refreshToken"].(string)
	call(cp, "PUT", "/api/v1/tenants/tenant-a/members/admin", admin, `{"role":"viewer"}`, 200)
	call(cp, "GET", "/api/v1/tenants/tenant-a/members", admin, "", 401)
	fresh := call(cp, "POST", "/api/v1/auth/refresh", "", `{"refreshToken":"`+refreshToken+`"}`, 200)["data"].(map[string]any)
	if fresh["role"] != "viewer" {
		t.Fatalf("refresh retained old role: %+v", fresh)
	}
	call(cp, "GET", "/api/v1/tenants/tenant-a/members", fresh["accessToken"].(string), "", 401)
}
