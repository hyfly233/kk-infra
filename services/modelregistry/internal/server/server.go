// Package server modelregistry HTTP 服务
package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/modelregistry/internal/biz"
)

// Server 模型注册 HTTP 服务
type Server struct {
	registry *biz.Registry
	logger   *slog.Logger
}

// NewServer 创建服务
func NewServer(registry *biz.Registry, logger *slog.Logger) *Server {
	return &Server{registry: registry, logger: logger}
}

// Handler 返回路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// 模型
	mux.HandleFunc("POST /api/v1/models", s.handleCreateModel)
	mux.HandleFunc("GET /api/v1/models", s.handleListModels)
	mux.HandleFunc("GET /api/v1/models/{id}", s.handleGetModel)
	mux.HandleFunc("DELETE /api/v1/models/{id}", s.handleDeleteModel)

	// 版本
	mux.HandleFunc("POST /api/v1/models/{id}/versions", s.handleCreateVersion)
	mux.HandleFunc("GET /api/v1/models/{id}/versions", s.handleListVersions)
	mux.HandleFunc("GET /api/v1/versions/{versionId}", s.handleGetVersion)
	mux.HandleFunc("POST /api/v1/versions/{versionId}/validate", s.handleValidateVersion)
	mux.HandleFunc("POST /api/v1/versions/{versionId}/release", s.handleReleaseVersion)
	mux.HandleFunc("DELETE /api/v1/models/{id}/versions/{version}", s.handleDeleteVersion)

	// 中间件链：RequestID → Recover → 访问日志
	return middleware.WithRequestID(
		middleware.Recover(s.logger,
			middleware.AccessLog(s.logger, mux),
		),
	)
}

// ---- 模型 ----

func (s *Server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CreateModelRequest
	if !decodeBody(w, r, &req) {
		return
	}
	m, err := s.registry.CreateModel(&req)
	apitypes.WriteResult(w, r, m, err)
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	list, err := s.registry.ListModels()
	apitypes.WriteResult(w, r, list, err)
}

func (s *Server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	m, err := s.registry.GetModel(r.PathValue("id"))
	apitypes.WriteResult(w, r, m, err)
}

func (s *Server) handleDeleteModel(w http.ResponseWriter, r *http.Request) {
	err := s.registry.DeleteModel(r.PathValue("id"))
	apitypes.WriteResult(w, r, map[string]bool{"deleted": true}, err)
}

// ---- 版本 ----

func (s *Server) handleCreateVersion(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CreateModelVersionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	v, err := s.registry.CreateVersion(r.PathValue("id"), &req)
	apitypes.WriteResult(w, r, v, err)
}

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.registry.ListVersions(r.PathValue("id"))
	apitypes.WriteResult(w, r, list, err)
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.registry.GetVersion(r.PathValue("versionId"))
	apitypes.WriteResult(w, r, v, err)
}

func (s *Server) handleValidateVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.registry.ValidateVersion(r.Context(), r.PathValue("versionId"))
	apitypes.WriteResult(w, r, v, err)
}

func (s *Server) handleReleaseVersion(w http.ResponseWriter, r *http.Request) {
	v, err := s.registry.ReleaseVersion(r.PathValue("versionId"))
	apitypes.WriteResult(w, r, v, err)
}

func (s *Server) handleDeleteVersion(w http.ResponseWriter, r *http.Request) {
	err := s.registry.DeleteVersion(r.PathValue("id"), r.PathValue("version"))
	apitypes.WriteResult(w, r, map[string]bool{"deleted": true}, err)
}

// ---- 工具 ----

func decodeBody(w http.ResponseWriter, r *http.Request, v interface{}) bool {
	if r.Body == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体为空"))
		return false
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(v); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return false
	}
	return true
}
