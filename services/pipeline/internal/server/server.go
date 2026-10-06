package server

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/pipeline/internal/service"
)

type Server struct {
	service *service.Service
	logger  *slog.Logger
}

func New(s *service.Service, l *slog.Logger) *Server { return &Server{service: s, logger: l} }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("POST /api/v1/releases", s.start)
	mux.HandleFunc("GET /api/v1/releases/{id}", s.get)
	mux.HandleFunc("POST /api/v1/releases/{id}/approval", s.approve)
	return middleware.WithRequestID(middleware.Recover(s.logger, middleware.AccessLog(s.logger, mux)))
}
func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelVersionID string `json:"modelVersionId"`
		Operator       string `json:"operator"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体无效"))
		return
	}
	record, err := s.service.Start(r.Context(), req.ModelVersionID, req.Operator)
	apitypes.WriteResult(w, r, record, err)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	record, err := s.service.Get(r.PathValue("id"))
	apitypes.WriteResult(w, r, record, err)
}
func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Approver string `json:"approver"`
		Message  string `json:"message"`
		Approved bool   `json:"approved"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Approver == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "approver 必填"))
		return
	}
	record, err := s.service.Approve(r.Context(), r.PathValue("id"), req.Approver, req.Message, req.Approved)
	apitypes.WriteResult(w, r, record, err)
}
