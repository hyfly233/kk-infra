// Package server controlplane HTTP 服务
package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kk-infra/lib/apitypes"
	platformauth "kk-infra/lib/auth"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/lib/middleware"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
	"kk-infra/services/controlplane/internal/identity"
)

// Server 控制面 HTTP 服务
type Server struct {
	deployments   *biz.DeploymentUseCase
	resources     *biz.ResourceUseCase
	quotas        *biz.QuotaUseCase
	audit         *biz.AuditUseCase
	repo          data.DeploymentRepository
	logger        *slog.Logger
	observability *clients.ObservabilityClient // 可为 nil（未配置时返回占位）
	identity      *identity.Service
	provisioner   interface {
		ProvisionTenant(context.Context, string) error
	}
	clusters *clusters.Service
	notebook *clients.NotebookHubClient
}

// NewServer 创建服务
func NewServer(deployments *biz.DeploymentUseCase, resources *biz.ResourceUseCase, quotas *biz.QuotaUseCase, audit *biz.AuditUseCase, repo data.DeploymentRepository, logger *slog.Logger) *Server {
	return &Server{deployments: deployments, resources: resources, quotas: quotas, audit: audit, repo: repo, logger: logger}
}

// SetObservabilityClient 注入可观测性客户端（未配置时指标返回占位）。
func (s *Server) SetObservabilityClient(c *clients.ObservabilityClient) {
	s.observability = c
}

func (s *Server) SetIdentityService(service *identity.Service)     { s.identity = service }
func (s *Server) SetClusterService(service *clusters.Service)      { s.clusters = service }
func (s *Server) SetNotebookHub(client *clients.NotebookHubClient) { s.notebook = client }
func (s *Server) SetTenantProvisioner(p interface {
	ProvisionTenant(context.Context, string) error
}) {
	s.provisioner = p
}

// Handler 路由
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/bootstrap", s.handleBootstrap)
	mux.HandleFunc("POST /api/v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("POST /api/v1/auth/introspect", s.handleIntrospect)
	mux.HandleFunc("POST /internal/notebooks/introspect", s.handleNotebookIntrospect)
	mux.HandleFunc("GET /internal/resources/gpus", s.handleInternalGPUs)
	mux.HandleFunc("GET /internal/tenants/{tenantId}/serving-status", s.handleTenantServingStatus)
	mux.HandleFunc("POST /api/v1/notebooks/hub-token", s.handleNotebookHubToken)
	mux.HandleFunc("GET /api/v1/notebooks/workspace", s.handleNotebookWorkspace)
	mux.HandleFunc("POST /api/v1/notebooks/workspace", s.handleNotebookWorkspace)
	mux.HandleFunc("DELETE /api/v1/notebooks/workspace", s.handleNotebookWorkspace)
	mux.HandleFunc("POST /api/v1/clusters", s.handleRegisterCluster)
	mux.HandleFunc("GET /api/v1/clusters", s.handleListClusters)
	mux.HandleFunc("GET /api/v1/clusters/alerts", s.handleClusterAlerts)
	mux.HandleFunc("POST /api/v1/clusters/monitor-token", s.handleClusterMonitorToken)
	mux.HandleFunc("GET /internal/clusters/metrics", s.handleClusterAlertMetrics)
	mux.HandleFunc("POST /api/v1/clusters/{id}/heartbeat", s.handleClusterHeartbeat)
	mux.HandleFunc("POST /api/v1/clusters/{id}/agent-token", s.handleClusterAgentToken)
	mux.HandleFunc("PUT /api/v1/clusters/{id}/serving-route", s.handleUpdateClusterServingRoute)
	mux.HandleFunc("PUT /api/v1/clusters/{id}/placement-policy", s.handleUpdateClusterPolicy)
	mux.HandleFunc("PUT /api/v1/clusters/{id}/credentials", s.handleRotateClusterCredentials)
	mux.HandleFunc("DELETE /api/v1/clusters/{id}", s.handleDeleteCluster)
	mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	mux.HandleFunc("GET /api/v1/tenants/{tenantId}/members", s.handleListMembers)
	mux.HandleFunc("PUT /api/v1/tenants/{tenantId}/members/{userId}", s.handleSetMember)
	mux.HandleFunc("POST /api/v1/tenants/{tenantId}/disable", s.handleDisableTenant)

	// 部署
	mux.HandleFunc("POST /api/v1/deployments", s.handleCreateDeployment)
	mux.HandleFunc("GET /api/v1/deployments", s.handleListDeployments)
	mux.HandleFunc("GET /api/v1/deployments/{id}", s.handleGetDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/scale", s.handleScaleDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/restart", s.handleRestartDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/rebuild", s.handleRebuildDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{id}/orphan-reservations", s.handleOrphanReservations)
	mux.HandleFunc("POST /api/v1/deployments/{id}/orphan-cleanup", s.handleOrphanCleanup)
	mux.HandleFunc("POST /api/v1/deployments/{id}/upgrade", s.handleUpgradeDeployment)
	mux.HandleFunc("POST /api/v1/deployments/{id}/rollback", s.handleRollbackDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{id}/revisions", s.handleDeploymentRevisions)
	mux.HandleFunc("DELETE /api/v1/deployments/{id}", s.handleDeleteDeployment)
	mux.HandleFunc("GET /api/v1/deployments/{id}/metrics", s.handleDeploymentMetrics)

	// 资源
	mux.HandleFunc("GET /api/v1/resources/gpus", s.handleListGPUs)

	// 租户配额（R2-4）
	mux.HandleFunc("GET /api/v1/quotas", s.handleListQuotas)
	mux.HandleFunc("PUT /api/v1/quotas/{tenantId}", s.handleSetQuota)

	// 审计日志（R2-4）
	mux.HandleFunc("GET /api/v1/audit", s.handleListAudit)
	mux.HandleFunc("GET /api/v1/billing", s.handleBilling)
	mux.HandleFunc("GET /api/v1/billing.csv", s.handleBillingCSV)
	mux.HandleFunc("PUT /api/v1/rate-cards/{tenantId}", s.handleSetRateCard)

	base := http.Handler(mux)
	if s.identity != nil {
		base = s.authRequired(base)
	}
	return middleware.WithRequestID(
		middleware.Recover(s.logger,
			middleware.AccessLog(s.logger, base),
		),
	)
}

func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		var req struct {
			Token string `json:"token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		token = req.Token
	}
	response := map[string]any{"active": false}
	if s.identity != nil && token != "" {
		if claims, err := s.identity.Authenticate(token); err == nil {
			expiresAt := int64(0)
			if claims.ExpiresAt != nil {
				expiresAt = claims.ExpiresAt.Unix()
			}
			response = map[string]any{"active": true, "sub": claims.Subject, "tenantId": claims.TenantID, "role": claims.Role, "exp": expiresAt}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (s *Server) clusterAdmin(w http.ResponseWriter, r *http.Request) bool {
	if s.clusters == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "集群注册服务未配置加密密钥"))
		return false
	}
	claims := claimsFrom(r.Context())
	if s.identity == nil || claims == nil || claims.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return false
	}
	return true
}

func (s *Server) handleRegisterCluster(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	var req struct {
		ID                 string            `json:"id"`
		Name               string            `json:"name"`
		Endpoint           string            `json:"endpoint"`
		AdapterURL         string            `json:"adapterUrl"`
		ServingURLTemplate string            `json:"servingUrlTemplate"`
		Kubeconfig         string            `json:"kubeconfig"`
		Labels             map[string]string `json:"labels"`
		SupportedRuntimes  []string          `json:"supportedRuntimes"`
		AllowedTenants     []string          `json:"allowedTenants"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	cluster, err := s.clusters.Register(req.ID, req.Name, req.Endpoint, req.AdapterURL, req.Kubeconfig, req.Labels, req.SupportedRuntimes, req.AllowedTenants, req.ServingURLTemplate)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, err.Error()))
		return
	}
	apitypes.WriteResult(w, r, cluster, nil)
}

func (s *Server) handleUpdateClusterServingRoute(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	var req struct {
		ServingURLTemplate string `json:"servingUrlTemplate"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if err := s.clusters.SetServingURLTemplate(r.PathValue("id"), req.ServingURLTemplate); err != nil {
		code := errcode.ErrBadRequest
		if errors.Is(err, clusters.ErrNotFound) {
			code = errcode.ErrNotFound
		}
		apitypes.WriteResult(w, r, nil, errcode.New(code, err.Error()))
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.serving_route.update", claimsFrom(r.Context()).Subject, "", r.PathValue("id"), middleware.GetRequestID(r.Context()), "更新跨集群推理入口模板")
	}
	cluster, err := s.clusters.Get(r.PathValue("id"))
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询集群失败", err))
		return
	}
	apitypes.WriteResult(w, r, cluster, nil)
}

func (s *Server) handleListClusters(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	clusters, err := s.clusters.List()
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询集群失败", err))
		return
	}
	apitypes.WriteResult(w, r, clusters, nil)
}

func (s *Server) handleUpdateClusterPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	var req struct {
		Labels            map[string]string `json:"labels"`
		SupportedRuntimes []string          `json:"supportedRuntimes"`
		AllowedTenants    []string          `json:"allowedTenants"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if err := s.clusters.UpdatePolicy(r.PathValue("id"), req.Labels, req.SupportedRuntimes, req.AllowedTenants); err != nil {
		s.clusterWriteError(w, r, err)
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.policy.update", claimsFrom(r.Context()).Subject, "", r.PathValue("id"), middleware.GetRequestID(r.Context()), "更新集群放置策略")
	}
	c, _ := s.clusters.Get(r.PathValue("id"))
	apitypes.WriteResult(w, r, c, nil)
}

func (s *Server) handleRotateClusterCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.clusterAdmin(w, r) {
		return
	}
	var req struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if err := s.clusters.RotateCredentials(r.PathValue("id"), req.Kubeconfig); err != nil {
		s.clusterWriteError(w, r, err)
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.credentials.rotate", claimsFrom(r.Context()).Subject, "", r.PathValue("id"), middleware.GetRequestID(r.Context()), "轮换集群访问凭据")
	}
	apitypes.WriteResult(w, r, map[string]bool{"rotated": true}, nil)
}

func (s *Server) handleDeleteCluster(w http.ResponseWriter, r *http.Request) {
	if !s.clusterAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	deployments, err := s.deployments.ListDeployments("")
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "查询部署失败", err))
		return
	}
	for _, d := range deployments {
		if d.ClusterID == id && d.Status != domain.DeploymentStatusDeleted {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "集群仍有未删除的部署"))
			return
		}
	}
	if err := s.clusters.Delete(id); err != nil {
		s.clusterWriteError(w, r, err)
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.delete", claimsFrom(r.Context()).Subject, "", id, middleware.GetRequestID(r.Context()), "注销集群")
	}
	apitypes.WriteResult(w, r, map[string]bool{"deleted": true}, nil)
}

func (s *Server) clusterWriteError(w http.ResponseWriter, r *http.Request, err error) {
	code := errcode.ErrBadRequest
	if errors.Is(err, clusters.ErrNotFound) {
		code = errcode.ErrNotFound
	}
	apitypes.WriteResult(w, r, nil, errcode.New(code, err.Error()))
}

func (s *Server) handleClusterAgentToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.clusterAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	if _, err := s.clusters.Get(id); err != nil {
		code := errcode.ErrInternal
		if errors.Is(err, clusters.ErrNotFound) {
			code = errcode.ErrNotFound
		}
		apitypes.WriteResult(w, r, nil, errcode.New(code, "查询集群失败"))
		return
	}
	token, expires, err := s.identity.IssueClusterAgentToken(id)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrInternal, "签发集群 token 失败"))
		return
	}
	if s.audit != nil {
		s.audit.Record("cluster.agent_token.issue", claimsFrom(r.Context()).Subject, "", id, middleware.GetRequestID(r.Context()), "签发集群心跳凭据")
	}
	apitypes.WriteResult(w, r, map[string]any{"token": token, "expiresAt": expires}, nil)
}

func (s *Server) handleClusterHeartbeat(w http.ResponseWriter, r *http.Request) {
	if s.clusters == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "集群注册服务未配置加密密钥"))
		return
	}
	if s.identity == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "身份服务未配置"))
		return
	}
	clusterID := r.PathValue("id")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "缺少 Bearer Token"))
		return
	}
	if _, err := s.identity.AuthenticateClusterAgent(token, clusterID); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "集群 agent token 无效"))
		return
	}
	var req struct {
		HealthStatus  string                            `json:"healthStatus"`
		GPUCapacity   []clusters.Capacity               `json:"gpuCapacity"`
		VolcanoQueues []domain.VolcanoQueueCapacity     `json:"volcanoQueues"`
		Telemetry     *domain.ClusterTelemetry          `json:"telemetry"`
		DeploymentGPU []domain.DeploymentGPUObservation `json:"deploymentGpu"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if err := s.clusters.ReportAttributedSnapshot(r.PathValue("id"), req.HealthStatus, req.GPUCapacity, req.VolcanoQueues, req.Telemetry, req.DeploymentGPU); err != nil {
		code := errcode.ErrBadRequest
		if errors.Is(err, clusters.ErrNotFound) {
			code = errcode.ErrNotFound
		}
		apitypes.WriteResult(w, r, nil, errcode.New(code, err.Error()))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"accepted": true}, nil)
}

func (s *Server) billingRows(w http.ResponseWriter, r *http.Request) ([]clients.DailyUsage, bool) {
	if s.observability == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "observability 未配置"))
		return nil, false
	}
	tenant := r.URL.Query().Get("tenantId")
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin {
		tenant = claims.TenantID
	}
	if tenant == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "tenantId 必填"))
		return nil, false
	}
	rows, err := s.observability.Billing(r.Context(), tenant, r.URL.Query().Get("from"), r.URL.Query().Get("to"))
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return nil, false
	}
	return rows, true
}
func (s *Server) handleBilling(w http.ResponseWriter, r *http.Request) {
	rows, ok := s.billingRows(w, r)
	if ok {
		apitypes.WriteResult(w, r, rows, nil)
	}
}
func (s *Server) handleBillingCSV(w http.ResponseWriter, r *http.Request) {
	rows, ok := s.billingRows(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=billing.csv")
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"date", "tenant_id", "deployment_id", "input_tokens", "output_tokens", "requests", "failed", "gpu_replica_seconds", "estimated_cost"})
	for _, row := range rows {
		_ = writer.Write([]string{row.Date.Format("2006-01-02"), row.TenantID, row.DeploymentID, strconv.FormatInt(row.InputTokens, 10), strconv.FormatInt(row.OutputTokens, 10), strconv.FormatInt(row.RequestCount, 10), strconv.FormatInt(row.FailedCount, 10), strconv.FormatInt(row.GPUReplicaSeconds, 10), strconv.FormatFloat(row.EstimatedCost, 'f', 6, 64)})
	}
	writer.Flush()
}
func (s *Server) handleSetRateCard(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	if s.observability == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "observability 未配置"))
		return
	}
	var card clients.RateCard
	if err := json.NewDecoder(r.Body).Decode(&card); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	card.TenantID = r.PathValue("tenantId")
	if err := s.observability.SetRateCard(r.Context(), card.TenantID, card); err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, card, nil)
}

type claimsContextKey struct{}

func claimsFrom(ctx context.Context) *platformauth.Claims {
	claims, _ := ctx.Value(claimsContextKey{}).(*platformauth.Claims)
	return claims
}

func canAccessTenant(ctx context.Context, tenantID string) bool {
	claims := claimsFrom(ctx)
	return claims == nil || claims.Role == platformauth.RolePlatformAdmin || claims.TenantID == tenantID
}

func (s *Server) authRequired(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/auth/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/internal/clusters/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/internal/notebooks/introspect" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/internal/resources/gpus" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/internal/tenants/") && strings.HasSuffix(r.URL.Path, "/serving-status") {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/v1/clusters/") && strings.HasSuffix(r.URL.Path, "/heartbeat") {
			next.ServeHTTP(w, r)
			return
		}
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "缺少 Bearer Token"))
			return
		}
		claims, err := s.identity.Authenticate(strings.TrimPrefix(authz, "Bearer "))
		if err != nil {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "access token 无效"))
			return
		}
		if r.Method != http.MethodGet && claims.Role == platformauth.RoleViewer {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "只读角色不能修改资源"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsContextKey{}, claims)))
	})
}

func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "身份服务未配置"))
		return
	}
	var req struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Password string `json:"password"`
		TenantID string `json:"tenantId"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if s.provisioner != nil {
		if err := s.provisioner.ProvisionTenant(r.Context(), req.TenantID); err != nil {
			apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrUpstream, "初始化租户 Kubernetes 资源失败", err))
			return
		}
	}
	u, err := s.identity.Bootstrap(req.ID, req.Email, req.Password, req.TenantID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrConflict, "初始化管理员失败"))
		return
	}
	apitypes.WriteResult(w, r, map[string]string{"id": u.ID, "email": u.Email}, nil)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "身份服务未配置"))
		return
	}
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TenantID string `json:"tenantId"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	session, err := s.identity.Login(req.Email, req.Password, req.TenantID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "用户名、密码或租户无效"))
		return
	}
	apitypes.WriteResult(w, r, session, nil)
}
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if s.identity == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "身份服务未配置"))
		return
	}
	session, err := s.identity.Refresh(req.RefreshToken)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "refresh token 无效"))
		return
	}
	apitypes.WriteResult(w, r, session, nil)
}
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refreshToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if s.identity != nil {
		s.identity.Logout(req.RefreshToken)
	}
	apitypes.WriteResult(w, r, map[string]bool{"loggedOut": true}, nil)
}

func (s *Server) requireTenantAdmin(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	claims := claimsFrom(r.Context())
	if claims == nil || (claims.Role != platformauth.RolePlatformAdmin && (claims.Role != platformauth.RoleTenantAdmin || claims.TenantID != tenantID)) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要租户管理员权限"))
		return false
	}
	return true
}

func requirePlatformAdmin(w http.ResponseWriter, r *http.Request) bool {
	claims := claimsFrom(r.Context())
	if claims != nil && claims.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return false
	}
	return true
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrIllegalState, "身份服务未配置"))
		return
	}
	claims := claimsFrom(r.Context())
	if claims == nil || claims.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return
	}
	var req struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	u, err := s.identity.CreateUser(req.ID, req.Email, req.Password)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrConflict, "创建用户失败"))
		return
	}
	apitypes.WriteResult(w, r, map[string]string{"id": u.ID, "email": u.Email}, nil)
}
func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	if !s.requireTenantAdmin(w, r, tenantID) {
		return
	}
	apitypes.WriteResult(w, r, s.identity.Members(tenantID), nil)
}
func (s *Server) handleSetMember(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("tenantId")
	if !s.requireTenantAdmin(w, r, tenantID) {
		return
	}
	var req struct {
		Role platformauth.Role `json:"role"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	if claimsFrom(r.Context()).Role != platformauth.RolePlatformAdmin {
		protected := req.Role == platformauth.RolePlatformAdmin
		for _, member := range s.identity.Members(tenantID) {
			if member.UserID == r.PathValue("userId") && member.Role == platformauth.RolePlatformAdmin {
				protected = true
			}
		}
		if protected {
			apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "只有平台管理员可以授予或修改平台管理员身份"))
			return
		}
	}
	if err := s.identity.SetMember(r.PathValue("userId"), tenantID, req.Role); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "设置成员失败"))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"updated": true}, nil)
}
func (s *Server) handleDisableTenant(w http.ResponseWriter, r *http.Request) {
	claims := claimsFrom(r.Context())
	if claims == nil || claims.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return
	}
	if err := s.identity.DisableTenant(r.PathValue("tenantId")); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.Wrap(errcode.ErrInternal, "禁用租户失败", err))
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"disabled": true}, nil)
}

// ---- 部署 ----

func (s *Server) handleCreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req apitypes.CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin {
		req.TenantID = claims.TenantID
		req.Namespace = ""
	}
	d, err := s.deployments.CreateDeployment(r.Context(), &req)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, d, nil)
}

func (s *Server) handleListDeployments(w http.ResponseWriter, r *http.Request) {
	tenant := r.URL.Query().Get("tenant")
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin {
		tenant = claims.TenantID
	}
	list, err := s.deployments.ListDeployments(tenant)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, list, nil)
}

func (s *Server) handleGetDeployment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if d, err := s.deployments.GetDeployment(id); err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	d, podStatus, err := s.deployments.GetDeploymentWithK8sStatus(r.Context(), id)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	// 附加事件
	view := apitypes.DeploymentView{
		ModelDeployment: *d,
		PodStatus:       podStatus,
	}
	events := s.repo.Events(id)
	for _, e := range events {
		view.Events = append(view.Events, apitypes.EventView{
			Type:    "Status",
			Reason:  e.To,
			Message: e.Reason,
			At:      e.At,
		})
	}
	apitypes.WriteResult(w, r, view, nil)
}

func (s *Server) handleScaleDeployment(w http.ResponseWriter, r *http.Request) {
	var req apitypes.ScaleDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	if d, err := s.deployments.GetDeployment(r.PathValue("id")); err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	d, err := s.deployments.ScaleDeployment(r.Context(), r.PathValue("id"), req.Replicas)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, d, nil)
}

func (s *Server) handleRestartDeployment(w http.ResponseWriter, r *http.Request) {
	if d, err := s.deployments.GetDeployment(r.PathValue("id")); err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	d, err := s.deployments.RestartDeployment(r.Context(), r.PathValue("id"))
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, d, nil)
}

func (s *Server) handleRebuildDeployment(w http.ResponseWriter, r *http.Request) {
	claims := claimsFrom(r.Context())
	if s.identity == nil || claims == nil || claims.Role != platformauth.RolePlatformAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要平台管理员权限"))
		return
	}
	var req struct {
		TargetClusterID              string `json:"targetClusterId"`
		AcknowledgeOrphanedResources bool   `json:"acknowledgeOrphanedResources"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败"))
		return
	}
	d, err := s.deployments.RebuildDeployment(r.Context(), r.PathValue("id"), req.TargetClusterID, claims.Subject, req.AcknowledgeOrphanedResources)
	apitypes.WriteResult(w, r, d, err)
}

func (s *Server) handleUpgradeDeployment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ModelVersionID string `json:"modelVersionId"`
	}
	if d, err := s.deployments.GetDeployment(r.PathValue("id")); err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ModelVersionID == "" {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "modelVersionId 必填"))
		return
	}
	d, err := s.deployments.UpgradeDeployment(r.Context(), r.PathValue("id"), req.ModelVersionID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, d, nil)
}

func (s *Server) handleRollbackDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.deployments.GetDeployment(r.PathValue("id"))
	if err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	var req struct {
		RevisionID string `json:"revisionId"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	result, err := s.deployments.RollbackDeployment(r.Context(), d.ID, req.RevisionID)
	apitypes.WriteResult(w, r, result, err)
}

func (s *Server) handleDeploymentRevisions(w http.ResponseWriter, r *http.Request) {
	d, err := s.deployments.GetDeployment(r.PathValue("id"))
	if err != nil || !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	revisions, err := s.deployments.Revisions(d.ID)
	apitypes.WriteResult(w, r, revisions, err)
}

func (s *Server) handleDeleteDeployment(w http.ResponseWriter, r *http.Request) {
	if d, err := s.deployments.GetDeployment(r.PathValue("id")); err == nil && !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	if err := s.deployments.DeleteDeployment(r.Context(), r.PathValue("id")); err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, map[string]bool{"deleted": true}, nil)
}

// handleDeploymentMetrics 指标查询：转发到 observability；未配置时返回占位。
func (s *Server) handleDeploymentMetrics(w http.ResponseWriter, r *http.Request) {
	d, err := s.deployments.GetDeployment(r.PathValue("id"))
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	if !canAccessTenant(r.Context(), d.TenantID) {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrNotFound, "部署不存在"))
		return
	}
	// 已配置 observability：转发真实指标
	if s.observability != nil {
		view, err := s.observability.DeploymentMetrics(r.Context(), r.PathValue("id"), r.URL.Query().Get("range"))
		if err != nil {
			s.logger.Warn("查询 observability 指标失败", "deploymentId", r.PathValue("id"), "err", err)
			apitypes.WriteResult(w, r, nil, err)
			return
		}
		apitypes.WriteResult(w, r, *view, nil)
		return
	}
	// 占位（observability 未启动）
	view := apitypes.MetricsView{
		DeploymentID: r.PathValue("id"),
		Range:        r.URL.Query().Get("range"),
		Series: []apitypes.MetricSeries{
			{Name: "requests", Points: []apitypes.MetricPoint{{Ts: time.Now().Unix(), Val: 0}}},
		},
	}
	apitypes.WriteResult(w, r, view, nil)
}

// ---- 资源 ----

func (s *Server) handleTenantServingStatus(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil || s.identity.AuthenticateService(platformauth.BearerToken(r), "gateway") != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要网关服务身份"))
		return
	}
	tenantID := r.PathValue("tenantId")
	active, err := s.identity.TenantServingActive(r.Context(), tenantID)
	if err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrInternal, "租户状态查询失败"))
		return
	}
	apitypes.WriteResult(w, r, map[string]any{"tenantId": tenantID, "active": active}, nil)
}

func (s *Server) handleInternalGPUs(w http.ResponseWriter, r *http.Request) {
	if s.identity == nil || s.identity.AuthenticateService(platformauth.BearerToken(r), "observability") != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要观测服务身份"))
		return
	}
	s.handleListGPUs(w, r)
}

func (s *Server) handleListGPUs(w http.ResponseWriter, r *http.Request) {
	gpuType := r.URL.Query().Get("gpuType")
	summary, nodes, err := s.resources.ListGPUResources(r.Context(), gpuType)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, apitypes.GPUResourcesView{Summary: *summary, Nodes: nodes}, nil)
}

// ---- 租户配额（R2-4） ----

func (s *Server) handleListQuotas(w http.ResponseWriter, r *http.Request) {
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin && claims.Role != platformauth.RoleTenantAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要管理员权限"))
		return
	}
	var list interface{}
	var err error
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role == platformauth.RoleTenantAdmin {
		list, err = s.quotas.ListTenant(claims.TenantID)
	} else {
		list, err = s.quotas.List()
	}
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, list, nil)
}

func (s *Server) handleSetQuota(w http.ResponseWriter, r *http.Request) {
	if !requirePlatformAdmin(w, r) {
		return
	}
	var req struct {
		GPUType string `json:"gpuType"`
		Quota   int32  `json:"quota"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrBadRequest, "请求体解析失败: "+err.Error()))
		return
	}
	q, err := s.quotas.Set(r.PathValue("tenantId"), req.GPUType, req.Quota)
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, q, nil)
}

// ---- 审计日志（R2-4） ----

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role != platformauth.RolePlatformAdmin && claims.Role != platformauth.RoleTenantAdmin {
		apitypes.WriteResult(w, r, nil, errcode.New(errcode.ErrUnauthorized, "需要管理员权限"))
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	var list interface{}
	var err error
	if claims := claimsFrom(r.Context()); claims != nil && claims.Role == platformauth.RoleTenantAdmin {
		list, err = s.audit.ListTenant(claims.TenantID, limit)
	} else {
		list, err = s.audit.List(limit)
	}
	if err != nil {
		apitypes.WriteResult(w, r, nil, err)
		return
	}
	apitypes.WriteResult(w, r, list, nil)
}

// 辅助：确保 domain 引用（类型断言场景预留）
var _ = domain.RuntimeVLLM
