package clients

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
)

func TestGatewayClientSendsAudienceBoundIdentity(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := platformauth.AuthenticateService("secret", platformauth.BearerToken(r), "gateway", "controlplane"); err != nil {
			t.Error(err)
		}
		w.Write([]byte(`{"code":0,"data":{}}`))
	}))
	defer api.Close()
	c := NewGatewayClient(api.URL)
	c.SetServiceIdentity("secret", "gateway")
	if err := c.RegisterRoute(context.Background(), "m", "m1", "http://backend", "tenant-a", "d1", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := c.UnregisterRoute(context.Background(), "m"); err != nil {
		t.Fatal(err)
	}
}

// Run via hack/gateway-auth-e2e.sh against isolated real gateway/inference binaries.
func TestGatewayBinaryAuthenticatedLifecycle(t *testing.T) {
	base, inference := os.Getenv("GATEWAY_E2E_URL"), os.Getenv("INFERENCE_E2E_URL")
	if base == "" || inference == "" {
		t.Skip("isolated binaries not configured")
	}
	const secret = "local-gateway-e2e-secret"
	cpURL := os.Getenv("GATEWAY_E2E_CP")
	if cpURL == "" {
		t.Fatal("controlplane required for online tenant checks")
	}
	cp := NewHTTPClient(cpURL)
	if err := cp.do(context.Background(), http.MethodPost, "/api/v1/auth/bootstrap", map[string]string{"id": "admin", "email": "admin@example.test", "password": "Strong-password-123", "tenantId": "tenant-a"}, nil); err != nil {
		t.Fatal(err)
	}
	c := NewGatewayClient(base)
	c.SetServiceIdentity(secret, "gateway")
	if err := c.RegisterRoute(context.Background(), "e2e-model", "m1", inference, "tenant-a", "d1", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	defer c.UnregisterRoute(context.Background(), "e2e-model")
	var session struct {
		AccessToken string `json:"accessToken"`
	}
	if err := cp.do(context.Background(), http.MethodPost, "/api/v1/auth/login", map[string]string{"email": "admin@example.test", "password": "Strong-password-123", "tenantId": "tenant-a"}, &session); err != nil {
		t.Fatal(err)
	}
	admin := session.AccessToken
	cpWrite := func(method, path, body string) {
		t.Helper()
		r, _ := http.NewRequest(method, cpURL+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+admin)
		r.Header.Set("Content-Type", "application/json")
		resp, err := cp.http.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("controlplane %s: %d", path, resp.StatusCode)
		}
	}
	cpWrite("POST", "/api/v1/users", `{"id":"other","email":"other@example.test","password":"Strong-password-123"}`)
	cpWrite("PUT", "/api/v1/tenants/tenant-b/members/other", `{"role":"tenant_admin"}`)
	call := func(method, path, token, body string, want int) []byte {
		t.Helper()
		r, _ := http.NewRequest(method, base+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var result json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, result)
		}
		return result
	}
	call("POST", "/internal/routes", admin, `{}`, 401)
	call("GET", "/api/v1/keys", "", "", 401)
	result := call("POST", "/api/v1/keys", admin, `{"tenantId":"tenant-a"}`, 200)
	var issued struct {
		Data struct {
			Key string `json:"key"`
			ID  string `json:"keyId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result, &issued); err != nil || issued.Data.Key == "" {
		t.Fatalf("issue=%s err=%v", result, err)
	}
	result = call("GET", "/v1/models", issued.Data.Key, "", 200)
	if !strings.Contains(string(result), "e2e-model") {
		t.Fatal("registered route missing")
	}
	call("POST", "/v1/chat/completions", issued.Data.Key, `{"model":"e2e-model","messages":[{"role":"user","content":"hello"}]}`, 200)
	if obsURL := os.Getenv("OBSERVABILITY_E2E_URL"); obsURL != "" {
		obs := NewObservabilityClient(obsURL)
		obs.SetServiceIdentity(secret, "observability")
		deadline := time.Now().Add(5 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			rows, err := obs.Billing(context.Background(), "tenant-a", "", "")
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.TenantID == "tenant-a" && row.DeploymentID == "d1" && row.RequestCount > 0 {
					found = true
				}
			}
			if found {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !found {
			t.Fatal("authenticated metrics did not reach usage ledger")
		}
		wrong := NewObservabilityClient(obsURL)
		wrong.SetServiceIdentity(secret, "gateway")
		if _, err := wrong.Billing(context.Background(), "tenant-a", "", ""); err == nil {
			t.Fatal("wrong audience accessed billing")
		}
	}
	other, _ := platformauth.IssueAccessToken(secret, "other", "tenant-b", platformauth.RoleTenantAdmin, "controlplane", time.Now(), time.Minute)
	call("POST", "/api/v1/keys/"+issued.Data.ID+"/disable", other, "", 404)
	call("GET", "/api/v1/keys", other, "", 200)
	cpWrite("PUT", "/api/v1/tenants/tenant-b/members/other", `{"role":"viewer"}`)
	call("GET", "/api/v1/keys", other, "", 401)
	call("POST", "/api/v1/keys/"+issued.Data.ID+"/disable", admin, "", 200)
	call("GET", "/v1/models", issued.Data.Key, "", 403)
	// Keep a second Key enabled: tenant disable must revoke inference independently.
	activeKey := call("POST", "/api/v1/keys", admin, `{"tenantId":"tenant-a"}`, 200)
	if err := json.Unmarshal(activeKey, &issued); err != nil {
		t.Fatal(err)
	}
	call("GET", "/v1/models", issued.Data.Key, "", 200)
	platformToken, _ := platformauth.IssueAccessToken(secret, "admin", "tenant-a", platformauth.RolePlatformAdmin, "controlplane", time.Now(), time.Minute)
	disableReq, _ := http.NewRequest(http.MethodPost, cpURL+"/api/v1/tenants/tenant-a/disable", nil)
	disableReq.Header.Set("Authorization", "Bearer "+platformToken)
	disableResp, err := cp.http.Do(disableReq)
	if err != nil {
		t.Fatal(err)
	}
	disableResp.Body.Close()
	if disableResp.StatusCode != 200 {
		t.Fatalf("disable status=%d", disableResp.StatusCode)
	}
	call("GET", "/v1/models", issued.Data.Key, "", 403)
	call("POST", "/v1/chat/completions", issued.Data.Key, `{"model":"e2e-model","messages":[]}`, 403)
	if err := c.UnregisterRoute(context.Background(), "e2e-model"); err != nil {
		t.Fatal(err)
	}
}
