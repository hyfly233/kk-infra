package server

import (
	"context"
	"net/http"
	"strings"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/errcode"
)

type managementClaimsKey struct{}

// Empty secret retains the explicitly unauthenticated local/Fake mode.
func (s *Server) SetAuthSecret(secret string) { s.authSecret = secret }

func (s *Server) managementRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.authSecret == "" || (!strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/internal/")) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			if platformauth.AuthenticateService(s.authSecret, platformauth.BearerToken(r), "gateway", "controlplane") != nil {
				apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要控制面服务身份"))
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		c, err := platformauth.ParseAccessToken(s.authSecret, platformauth.BearerToken(r), "controlplane")
		if err != nil || (c.Role != platformauth.RolePlatformAdmin && c.Role != platformauth.RoleTenantAdmin) || (c.Role != platformauth.RolePlatformAdmin && c.TenantID == "") {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要所属租户管理员权限"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), managementClaimsKey{}, c)))
	})
}

func managementClaims(r *http.Request) *platformauth.Claims {
	c, _ := r.Context().Value(managementClaimsKey{}).(*platformauth.Claims)
	return c
}

func (s *Server) keyOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant, err := s.keys.TenantForKey(r.PathValue("keyId"))
	c := managementClaims(r)
	if err != nil || (c != nil && c.Role != platformauth.RolePlatformAdmin && c.TenantID != tenant) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "Key 不存在"))
		return "", false
	}
	return tenant, true
}
