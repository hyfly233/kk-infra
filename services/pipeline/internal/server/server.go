package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/pipeline/internal/service"
)

type Server struct {
	service                  *service.Service
	logger                   *slog.Logger
	authSecret, authAudience string
}

func New(s *service.Service, l *slog.Logger) *Server { return &Server{service: s, logger: l} }
func (s *Server) SetAuth(secret, audience string) {
	s.authSecret, s.authAudience = secret, audience
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /api/v1/releases", s.start)
	mux.HandleFunc("GET /api/v1/releases", s.list)
	mux.HandleFunc("GET /api/v1/releases/{id}", s.get)
	mux.HandleFunc("POST /api/v1/releases/{id}/approval", s.approve)
	base := http.Handler(mux)
	if s.authSecret != "" {
		base = s.authRequired(base)
	}
	return middleware.WithRequestID(middleware.Recover(s.logger, middleware.AccessLog(s.logger, base)))
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > 200 {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "limit 必须为 1-200"))
			return
		}
		limit = parsed
	}
	tenantID := r.URL.Query().Get("tenantId")
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin {
		tenantID = claims.TenantID
	}
	records, err := s.service.List(limit, r.URL.Query().Get("modelVersionId"), tenantID)
	apitypes.WriteResult(w, r, records, err)
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelVersionID string `json:"modelVersionId"`
		Operator       string `json:"operator"`
		TenantID       string `json:"tenantId"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体无效"))
		return
	}
	if claims := claimsFrom(r.Context()); claims != nil {
		if claims.Role == platformauth.RoleViewer {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "只读角色不能启动发布"))
			return
		}
		req.Operator = claims.Subject
		if claims.Role != platformauth.RolePlatformAdmin {
			req.TenantID = claims.TenantID
		} else if req.TenantID == "" {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "平台管理员启动发布时 tenantId 必填"))
			return
		}
	}
	record, err := s.service.Start(r.Context(), req.ModelVersionID, req.Operator, req.TenantID)
	apitypes.WriteResult(w, r, record, err)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	record, err := s.service.Get(r.PathValue("id"))
	if err == nil && !canAccessTenant(r.Context(), record.TenantID) {
		err = errcode.New(errcode.ErrUnauthorized, "无权访问其他租户的发布记录")
		record = nil
	}
	apitypes.WriteResult(w, r, record, err)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Approver string `json:"approver"`
		Message  string `json:"message"`
		Approved bool   `json:"approved"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体无效"))
		return
	}
	if claims := claimsFrom(r.Context()); claims != nil {
		if claims.Role != platformauth.RolePlatformAdmin && claims.Role != platformauth.RoleTenantAdmin {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "仅租户管理员可审批发布"))
			return
		}
		record, err := s.service.Get(r.PathValue("id"))
		if err != nil {
			apitypes.WriteResult(w, r, nil, err)
			return
		}
		if !canAccessTenant(r.Context(), record.TenantID) {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "无权审批其他租户的发布"))
			return
		}
		req.Approver = claims.Subject
	}
	if req.Approver == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "approver 必填"))
		return
	}
	record, err := s.service.Approve(r.Context(), r.PathValue("id"), req.Approver, req.Message, req.Approved)
	apitypes.WriteResult(w, r, record, err)
}

type claimsKey struct{}

func claimsFrom(ctx context.Context) *platformauth.Claims {
	claims, _ := ctx.Value(claimsKey{}).(*platformauth.Claims)
	return claims
}

func canAccessTenant(ctx context.Context, tenantID string) bool {
	claims := claimsFrom(ctx)
	return claims == nil || claims.Role == platformauth.RolePlatformAdmin || claims.TenantID == tenantID
}

func (s *Server) authRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "缺少 Bearer Token"))
			return
		}
		claims, err := platformauth.ParseAccessToken(s.authSecret, strings.TrimPrefix(authz, "Bearer "), s.authAudience)
		if err != nil {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "access token 无效"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey{}, claims)))
	})
}
