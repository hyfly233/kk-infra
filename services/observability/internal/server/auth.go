package server

import (
	"net/http"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/errcode"
)

func (s *Server) SetAuthSecret(secret string) { s.authSecret = secret }

func (s *Server) serviceRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Prometheus exposition is protected by the deployment NetworkPolicy.
		if s.authSecret == "" || (r.Method == http.MethodGet && r.URL.Path == "/metrics") {
			next.ServeHTTP(w, r)
			return
		}
		caller := "controlplane"
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/metrics/requests" {
			caller = "gateway"
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/metrics/gpu" {
			caller = "k8sadapter"
		}
		if platformauth.AuthenticateService(s.authSecret, platformauth.BearerToken(r), "observability", caller) != nil {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要限定用途的内部服务身份"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
