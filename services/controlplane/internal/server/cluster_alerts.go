package server

import (
	"fmt"
	"net/http"
	"strings"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
)

func (s *Server) handleClusterAlerts(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	alerts, err := s.clusters.Alerts()
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询集群告警失败", err))
		return
	}
	apitypes.WriteResult(w, r, alerts, nil)
}

func (s *Server) handleClusterMonitorToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.clusterAdmin(w, r) {
		return
	}
	token, expires, err := s.identity.IssueClusterMonitorToken()
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrInternal, "签发监控 token 失败"))
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.monitor_token.issue", claimsFrom(r.Context()).Subject, "", "", middleware.GetRequestID(r.Context()), "签发仅可读取集群告警指标的凭据")
	}
	apitypes.WriteResult(w, r, map[string]any{"token": token, "expiresAt": expires}, nil)
}

func (s *Server) handleClusterAlertMetrics(w http.ResponseWriter, r *http.Request) {
	authz := r.Header.Get("Authorization")
	if s.identity == nil || !strings.HasPrefix(authz, "Bearer ") || s.identity.AuthenticateClusterMonitor(strings.TrimPrefix(authz, "Bearer ")) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要集群监控专用 token"))
		return
	}
	if s.clusters == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "集群注册服务未启用"))
		return
	}
	metrics, err := s.clusters.AlertMetrics()
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询集群指标失败", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprint(w, metrics)
}
