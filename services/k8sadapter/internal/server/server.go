// Package server k8sadapter HTTP 服务。
// 暴露给 controlplane 的 K8s 操作 API。
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/k8sadapter/internal/client"
)

// Server K8s 适配器 HTTP 服务
type Server struct {
	kube   client.KubeClient
	logger *slog.Logger
}

// NewServer 创建服务
func NewServer(kube client.KubeClient, logger *slog.Logger) *Server {
	return &Server{kube: kube, logger: logger}
}

// Handler 路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/resources/gpus", s.handleListGPUs)
	mux.HandleFunc("POST /v1/tenants/{tenantId}/provision", s.handleProvisionTenant)
	mux.HandleFunc("POST /v1/deployments", s.handleCreateDeployment)
	mux.HandleFunc("PUT /v1/deployments/{name}", s.handleUpdateDeployment)
	mux.HandleFunc("GET /v1/deployments/{name}", s.handleGetDeployment)
	mux.HandleFunc("GET /v1/deployments", s.handleListDeployments)
	mux.HandleFunc("POST /v1/deployments/{name}/scale", s.handleScaleDeployment)
	mux.HandleFunc("POST /v1/deployments/{name}/restart", s.handleRestartDeployment)
	mux.HandleFunc("DELETE /v1/deployments/{name}", s.handleDeleteDeployment)
	return middleware.WithRequestID(
		middleware.Recover(s.logger,
			middleware.AccessLog(s.logger, mux),
		),
	)
}

func (s *Server) handleProvisionTenant(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	if err := s.kube.ProvisionTenant(ctx, r.PathValue("tenantId")); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "初始化租户 Kubernetes 资源失败", err))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"provisioned": true}, nil)
}

// 带超时的上下文
func (s *Server) reqCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 30*time.Second)
}

func (s *Server) handleListGPUs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	nodes, err := s.kube.ListGPUNodes(ctx)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询 GPU 失败", err))
		return
	}
	apitypes.WriteResult(w, r, nodes, nil)
}

func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var spec client.DeploymentSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	res, err := s.kube.CreateDeployment(ctx, &spec)
	if err != nil {
		s.logger.Error("创建部署失败", "err", err, "name", spec.Name, "ns", spec.Namespace)
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "创建部署失败: "+err.Error(), err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleUpdateDeployment(w http.ResponseWriter, r *http.Request) {
	var spec client.DeploymentSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	if spec.Name != "" && spec.Name != r.PathValue("name") {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "路径与部署名称不一致"))
		return
	}
	spec.Name = r.PathValue("name")
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	res, err := s.kube.UpdateDeployment(ctx, &spec)
	if err != nil {
		if err == client.ErrNotFound {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
			return
		}
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "更新部署失败", err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	res, err := s.kube.GetDeployment(ctx, r.PathValue("name"), ns)
	if err != nil {
		if err == client.ErrNotFound {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
			return
		}
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询部署失败", err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	list, err := s.kube.ListDeployments(ctx, ns)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "扫描部署失败", err))
		return
	}
	apitypes.WriteResult(w, r, list, nil)
}

func (s *Server) handleScaleDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Replicas int32 `json:"replicas"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Replicas < 0 {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "replicas 必填且 >= 0"))
		return
	}
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	res, err := s.kube.ScaleDeployment(ctx, r.PathValue("name"), ns, req.Replicas)
	if err != nil {
		if err == client.ErrNotFound {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
			return
		}
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "扩缩容失败", err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleRestartDeployment(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	res, err := s.kube.RestartDeployment(ctx, r.PathValue("name"), ns)
	if err != nil {
		if err == client.ErrNotFound {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
			return
		}
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "重启部署失败", err))
		return
	}
	apitypes.WriteResult(w, r, res, nil)
}

func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	ctx, cancel := s.reqCtx(r)
	defer cancel()
	// 幂等删除：不存在也返回成功
	if err := s.kube.DeleteDeployment(ctx, r.PathValue("name"), ns); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "删除部署失败", err))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"deleted": true}, nil)
}

// 工具：字符串转 int32（保留给分页等场景）
func parseInt32(s string) int32 {
	n, _ := strconv.ParseInt(s, 10, 32)
	return int32(n)
}
