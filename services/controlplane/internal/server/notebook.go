package server

import (
	"encoding/json"
	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"net/http"
	"strings"
)

func (s *Server) handleNotebookHubToken(w http.ResponseWriter, r *http.Request) {
	c := claimsFrom(r.Context())
	if s.identity == nil || c == nil || c.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return
	}
	if _, err := s.identity.NotebookIdentity(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "controlplane"); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "管理员身份已失效"))
		return
	}
	token, expires, err := s.identity.IssueNotebookHubToken()
	w.Header().Set("Cache-Control", "no-store")
	if s.audit != nil {
		s.audit.Record("notebook.hub_token.issue", c.Subject, "", "notebook-hub", middleware.GetRequestID(r.Context()), "签发限定 audience 的 Hub 校验令牌")
	}
	apitypes.WriteResult(w, r, map[string]any{"token": token, "expiresAt": expires}, err)
}

func (s *Server) handleNotebookIntrospect(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	service := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if s.identity == nil || s.identity.AuthenticateNotebookHub(service) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要 Notebook Hub 服务身份"))
		return
	}
	var request struct {
		Token string `json:"token"`
		Spawn bool   `json:"spawn"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&request) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "无效校验请求"))
		return
	}
	audience := "controlplane"
	if request.Spawn {
		audience = "notebook-spawn"
	}
	c, err := s.identity.NotebookIdentity(request.Token, audience)
	result := map[string]any{"active": false}
	if err == nil {
		result = map[string]any{"active": true, "sub": c.Subject, "tenantId": c.TenantID, "role": c.Role, "hubUsername": domain.NotebookUsername(c.TenantID, c.Subject)}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (s *Server) handleNotebookWorkspace(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.identity == nil || claimsFrom(r.Context()) == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "Notebook 必须使用真实用户身份"))
		return
	}
	c, err := s.identity.NotebookIdentity(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), "controlplane")
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "Notebook 用户或租户身份已失效"))
		return
	}
	if s.notebook == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "JupyterHub 未配置"))
		return
	}
	var result any
	switch r.Method {
	case http.MethodGet:
		result, err = s.notebook.Get(r.Context(), c.TenantID, c.Subject)
	case http.MethodPost:
		if s.provisioner == nil {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "租户资源服务未配置"))
			return
		}
		if err = s.provisioner.ProvisionTenant(r.Context(), c.TenantID); err == nil {
			var grant string
			grant, err = s.identity.NotebookSpawnToken(c)
			if err == nil {
				result, err = s.notebook.Start(r.Context(), c.TenantID, c.Subject, grant)
			}
		}
	case http.MethodDelete:
		result, err = s.notebook.Delete(r.Context(), c.TenantID, c.Subject)
	}
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrUpstream, "Notebook 操作未确认，请查询状态", err))
		return
	}
	if r.Method != http.MethodGet && s.audit != nil {
		s.audit.Record("notebook.workspace."+strings.ToLower(r.Method), c.Subject, c.TenantID, domain.NotebookUsername(c.TenantID, c.Subject), middleware.GetRequestID(r.Context()), "提交个人 Notebook 生命周期操作")
	}
	apitypes.WriteResult(w, r, result, nil)
}
