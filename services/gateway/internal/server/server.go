// Package server gateway HTTP 服务。
// 对外提供 OpenAI 兼容 API 与 API Key / 路由管理接口。
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/gateway/internal/auth"
	"kk-infra/services/gateway/internal/proxy"
	"kk-infra/services/gateway/internal/router"
)

// Server 网关服务
type Server struct {
	keys   *auth.Manager
	routes *router.Table
	proxy  *proxy.Proxy
	logger *slog.Logger
}

// NewServer 创建网关服务
func NewServer(keys *auth.Manager, routes *router.Table, proxy *proxy.Proxy, logger *slog.Logger) *Server {
	return &Server{keys: keys, routes: routes, proxy: proxy, logger: logger}
}

// Handler 路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// OpenAI 兼容 API（需要鉴权）
	mux.HandleFunc("GET /v1/models", s.authRequired(s.handleListModels))
	mux.HandleFunc("POST /v1/chat/completions", s.authRequired(s.handleChatCompletions))

	// 内部管理 API（控制面调用，MVP 不做内部鉴权）
	mux.HandleFunc("POST /internal/routes", s.handleRegisterRoute)
	mux.HandleFunc("DELETE /internal/routes/{model}", s.handleUnregisterRoute)

	// API Key 管理
	mux.HandleFunc("POST /api/v1/keys", s.handleIssueKey)
	mux.HandleFunc("GET /api/v1/keys", s.handleListKeys)
	mux.HandleFunc("POST /api/v1/keys/{keyId}/disable", s.handleDisableKey)
	mux.HandleFunc("POST /api/v1/keys/{keyId}/rotate", s.handleRotateKey)
	mux.HandleFunc("POST /api/v1/keys/{keyId}/models", s.handleSetKeyModels)

	return middleware.WithRequestID(
		middleware.Recover(s.logger,
			middleware.AccessLog(s.logger, mux),
		),
	)
}

// authRequired 鉴权中间件：Bearer Token → 租户
func (s *Server) authRequired(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			s.writeKeyErr(w, r, http.StatusUnauthorized, "缺少 Bearer Token")
			return
		}
		key := strings.TrimPrefix(authz, "Bearer ")
		tenant, err := s.keys.Authenticate(key)
		if err != nil {
			status := http.StatusUnauthorized
			if err == auth.ErrKeyDisabled {
				status = http.StatusForbidden
			}
			s.writeKeyErr(w, r, status, "API Key 无效或已禁用")
			return
		}
		// 注入租户 + API Key 到 context（R2-4 模型授权用）
		ctx := withTenant(r.Context(), tenant)
		ctx = withAPIKey(ctx, key)
		next(w, r.WithContext(ctx))
	}
}

// ---- OpenAI 兼容 API ----

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	routes := s.routes.List()
	data := make([]map[string]interface{}, 0, len(routes))
	for _, rt := range routes {
		data = append(data, map[string]interface{}{
			"id":       rt.Model,
			"object":   "model",
			"created":  time.Now().Unix(),
			"owned_by": rt.TenantID,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"object": "list", "data": data})
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	tenant := tenantFrom(r.Context())
	s.proxy.Forward(w, r, tenant)
}

// ---- 内部路由管理 ----

type registerRouteReq struct {
	Model        string `json:"model"`
	ModelID      string `json:"modelId"`
	Endpoint     string `json:"endpoint"`
	TenantID     string `json:"tenantId"`
	DeploymentID string `json:"deploymentId"`
}

func (s *Server) handleRegisterRoute(w http.ResponseWriter, r *http.Request) {
	var req registerRouteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" || req.Endpoint == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "model 与 endpoint 必填"))
		return
	}
	if err := s.routes.Register(r.Context(), &router.Route{
		Model:        req.Model,
		ModelID:      req.ModelID,
		Endpoint:     req.Endpoint,
		TenantID:     req.TenantID,
		DeploymentID: req.DeploymentID,
	}); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "保存网关路由失败", err))
		return
	}
	s.logger.Info("注册模型路由", "model", req.Model, "endpoint", req.Endpoint)
	apitypes.WriteResult(w, r, map[string]bool{"registered": true}, nil)
}

func (s *Server) handleUnregisterRoute(w http.ResponseWriter, r *http.Request) {
	if err := s.routes.Unregister(r.Context(), r.PathValue("model")); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "删除网关路由失败", err))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"unregistered": true}, nil)
}

// ---- API Key 管理 ----

type issueKeyReq struct {
	TenantID string `json:"tenantId"`
}

func (s *Server) handleIssueKey(w http.ResponseWriter, r *http.Request) {
	var req issueKeyReq
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	if req.TenantID == "" {
		req.TenantID = "default"
	}
	res, err := s.keys.Issue(req.TenantID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "创建 API Key 失败", err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	apitypes.WriteResult(w, r, s.keys.List(), nil)
}

func (s *Server) handleDisableKey(w http.ResponseWriter, r *http.Request) {
	if err := s.keys.Disable(r.PathValue("keyId")); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "Key 不存在"))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"disabled": true}, nil)
}

func (s *Server) handleRotateKey(w http.ResponseWriter, r *http.Request) {
	tenant := r.URL.Query().Get("tenant")
	if tenant == "" {
		tenant = "default"
	}
	res, err := s.keys.Rotate(r.PathValue("keyId"), tenant)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "Key 不存在"))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

// handleSetKeyModels 设置 Key 的模型白名单（R2-4 模型授权）
func (s *Server) handleSetKeyModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Models []string `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	if err := s.keys.SetModels(r.PathValue("keyId"), req.Models); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "Key 不存在"))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"updated": true}, nil)
}

// writeKeyErr 鉴权错误响应（OpenAI 风格）
func (s *Server) writeKeyErr(w http.ResponseWriter, r *http.Request, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]string{
			"message": msg,
			"type":    "authentication_error",
			"code":    "invalid_api_key",
		},
	})
}

// ---- context 租户 ----

type tenantCtxKey struct{}

func withTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantCtxKey{}, tenant)
}

func tenantFrom(ctx context.Context) string {
	if v, ok := ctx.Value(tenantCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// API Key context（R2-4 模型授权）
type apiKeyCtxKey struct{}

func withAPIKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, apiKeyCtxKey{}, key)
}

func apiKeyFrom(ctx context.Context) string {
	if v, ok := ctx.Value(apiKeyCtxKey{}).(string); ok {
		return v
	}
	return ""
}
