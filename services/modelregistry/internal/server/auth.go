package server

import (
	"context"
	"net/http"
	"strings"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
)

type registryClaimsKey struct{}

func registryClaims(r *http.Request) *platformauth.Claims {
	c, _ := r.Context().Value(registryClaimsKey{}).(*platformauth.Claims)
	return c
}
func (s *Server) SetAuthSecret(secret string) { s.authSecret = secret }

func (s *Server) authRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authSecret == "" {
			next.ServeHTTP(w, r)
			return
		}
		token := platformauth.BearerToken(r)
		if platformauth.AuthenticateService(s.authSecret, token, "modelregistry", "controlplane", "pipeline") == nil {
			c, _ := platformauth.ParseAccessToken(s.authSecret, token, "service:modelregistry")
			allowed := r.Method == http.MethodGet || (c.Subject == "pipeline" && r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/versions/") && (strings.HasSuffix(r.URL.Path, "/validate") || strings.HasSuffix(r.URL.Path, "/release")))
			if !allowed {
				apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "服务身份无权执行此操作"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		c, err := platformauth.ParseAccessToken(s.authSecret, token, "controlplane")
		if err != nil || (c.TenantID == "" && c.Role != platformauth.RolePlatformAdmin) || (r.Method != http.MethodGet && c.Role == platformauth.RoleViewer) || strings.HasSuffix(r.URL.Path, "/release") {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "模型操作身份无效或权限不足"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), registryClaimsKey{}, c)))
	})
}

func canReadModel(r *http.Request, tenant string) bool {
	c := registryClaims(r)
	return c == nil || c.Role == platformauth.RolePlatformAdmin || (tenant != "" && c.TenantID == tenant)
}

func (s *Server) owned(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var tenant string
		var err error
		if id := r.PathValue("versionId"); id != "" {
			var version *domain.ModelVersion
			version, err = s.registry.GetVersion(id)
			if err == nil {
				tenant = version.TenantID
			}
		} else {
			var model *domain.Model
			model, err = s.registry.GetModel(r.PathValue("id"))
			if err == nil {
				tenant = model.TenantID
			}
		}
		if err != nil || !canReadModel(r, tenant) {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "模型或版本不存在"))
			return
		}
		if s.authSecret != "" && tenant == "" && r.Method != http.MethodGet {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "历史模型未归属，禁止修改或发布"))
			return
		}
		next(w, r)
	}
}

func visibleVersion(r *http.Request, v *domain.ModelVersion) *domain.ModelVersion {
	if v != nil && registryClaims(r) != nil && registryClaims(r).Role == platformauth.RoleViewer {
		copy := *v
		copy.ArtifactURI = ""
		return &copy
	}
	return v
}
