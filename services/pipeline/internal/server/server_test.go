package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformauth "kk-infra/lib/auth"
	"kk-infra/services/pipeline"
	"kk-infra/services/pipeline/internal/data"
	"kk-infra/services/pipeline/internal/service"
)

func TestListReleasesFiltersAndLimits(t *testing.T) {
	store := data.NewMemoryStore()
	base := time.Now().UTC()
	for _, record := range []*pipeline.ReleaseRecord{
		{ID: "old-v1", ModelVersionID: "v1", Status: "FAILED", CreatedAt: base},
		{ID: "new-v2", ModelVersionID: "v2", Status: "RELEASED", CreatedAt: base.Add(2 * time.Second)},
		{ID: "new-v1", ModelVersionID: "v1", Status: "PENDING_APPROVAL", CreatedAt: base.Add(time.Second)},
	} {
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(service.New(store, "", "", ""), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/releases?modelVersionId=v1&limit=1", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", res.Code, res.Body.String())
	}
	var body struct {
		Code int                      `json:"code"`
		Data []pipeline.ReleaseRecord `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Code != 0 || len(body.Data) != 1 || body.Data[0].ID != "new-v1" {
		t.Fatalf("filtered release list mismatch: %+v", body)
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodGet, "/api/v1/releases?limit=201", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid limit returned %d: %s", bad.Code, bad.Body.String())
	}
}

func TestReleaseAuthEnforcesTenantAndApprovalRole(t *testing.T) {
	store := data.NewMemoryStore()
	now := time.Now().UTC()
	for _, record := range []*pipeline.ReleaseRecord{
		{ID: "tenant-a-release", ModelVersionID: "v1", TenantID: "tenant-a", Status: "PENDING_APPROVAL", Operator: "dev-a", CreatedAt: now, UpdatedAt: now, StageResults: []pipeline.RunResult{{Stage: pipeline.StageApproval, Status: "pending"}}},
		{ID: "tenant-b-release", ModelVersionID: "v2", TenantID: "tenant-b", Status: "PENDING_APPROVAL", Operator: "dev-b", CreatedAt: now.Add(time.Second), UpdatedAt: now},
	} {
		if err := store.Create(record); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "release-auth-test-secret"
	srv := New(service.New(store, "", "", ""), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.SetAuth(secret, "controlplane")
	handler := srv.Handler()
	token, err := platformauth.IssueAccessToken(secret, "viewer-a", "tenant-a", platformauth.RoleViewer, "controlplane", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/releases", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("missing token returned %d", unauthorized.Code)
	}
	emptyTenantToken, err := platformauth.IssueAccessToken(secret, "viewer", "", platformauth.RoleViewer, "controlplane", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	emptyTenantReq := httptest.NewRequest(http.MethodGet, "/api/v1/releases", nil)
	emptyTenantReq.Header.Set("Authorization", "Bearer "+emptyTenantToken)
	emptyTenantRes := httptest.NewRecorder()
	handler.ServeHTTP(emptyTenantRes, emptyTenantReq)
	if emptyTenantRes.Code != http.StatusUnauthorized {
		t.Fatalf("empty tenant exposed release list: %d", emptyTenantRes.Code)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/releases", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRes := httptest.NewRecorder()
	handler.ServeHTTP(listRes, listReq)
	var listBody struct {
		Data []pipeline.ReleaseRecord `json:"data"`
	}
	if err := json.NewDecoder(listRes.Body).Decode(&listBody); err != nil || len(listBody.Data) != 1 || listBody.Data[0].TenantID != "tenant-a" {
		t.Fatalf("tenant release isolation failed: status=%d body=%+v err=%v", listRes.Code, listBody, err)
	}

	otherReq := httptest.NewRequest(http.MethodGet, "/api/v1/releases/tenant-b-release", nil)
	otherReq.Header.Set("Authorization", "Bearer "+token)
	otherRes := httptest.NewRecorder()
	handler.ServeHTTP(otherRes, otherReq)
	if otherRes.Code != http.StatusUnauthorized {
		t.Fatalf("cross-tenant read returned %d: %s", otherRes.Code, otherRes.Body.String())
	}

	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/releases/tenant-a-release/approval", strings.NewReader(`{"approver":"spoofed","approved":true}`))
	approveReq.Header.Set("Authorization", "Bearer "+token)
	approveRes := httptest.NewRecorder()
	handler.ServeHTTP(approveRes, approveReq)
	if approveRes.Code != http.StatusUnauthorized {
		t.Fatalf("viewer approval returned %d: %s", approveRes.Code, approveRes.Body.String())
	}
	adminToken, err := platformauth.IssueAccessToken(secret, "admin-a", "tenant-a", platformauth.RoleTenantAdmin, "controlplane", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	crossApprove := httptest.NewRequest(http.MethodPost, "/api/v1/releases/tenant-b-release/approval", strings.NewReader(`{"approved":true}`))
	crossApprove.Header.Set("Authorization", "Bearer "+adminToken)
	crossRes := httptest.NewRecorder()
	handler.ServeHTTP(crossRes, crossApprove)
	if crossRes.Code != http.StatusUnauthorized {
		t.Fatalf("cross-tenant approval returned %d: %s", crossRes.Code, crossRes.Body.String())
	}
}
