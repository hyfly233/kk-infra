// Package biz 控制面业务逻辑
package biz

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/services/controlplane/internal/clients"
	"kk-infra/services/controlplane/internal/clusters"
	"kk-infra/services/controlplane/internal/data"
)

// ModelRegistryClient 模型注册接口（便于测试替换）
type ModelRegistryClient interface {
	GetVersion(ctx context.Context, versionID string) (*domain.ModelVersion, error)
}

// K8sClient K8s 适配器接口（便于测试替换）
type K8sClient interface {
	ListGPUs(ctx context.Context) ([]domain.GPUResource, error)
	CreateDeployment(ctx context.Context, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error)
	UpdateDeployment(ctx context.Context, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error)
	GetDeployment(ctx context.Context, name, namespace string) (*clients.K8sDeploymentResult, error)
	ListDeployments(ctx context.Context, namespace string) ([]*clients.K8sDeploymentResult, error)
	ScaleDeployment(ctx context.Context, name, namespace string, replicas int32) (*clients.K8sDeploymentResult, error)
	RestartDeployment(ctx context.Context, name, namespace string) (*clients.K8sDeploymentResult, error)
	DeleteDeployment(ctx context.Context, name, namespace string) error
}

// GatewayClient 管理部署完成后的推理路由。
type GatewayClient interface {
	RegisterRoute(ctx context.Context, model, modelID, endpoint, tenantID, deploymentID, clusterID, stableEndpoint, canaryEndpoint, rolloutStatus string) error
	UnregisterRoute(ctx context.Context, model string) error
}
type TenantState interface{ TenantActive(tenantID string) bool }

type ClusterDeploymentClient interface {
	CreateDeploymentForCluster(context.Context, string, *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error)
	UpdateDeploymentForCluster(context.Context, string, *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error)
	GetDeploymentForCluster(context.Context, string, string, string) (*clients.K8sDeploymentResult, error)
	ListDeploymentsForCluster(context.Context, string, string) ([]*clients.K8sDeploymentResult, error)
	ScaleDeploymentForCluster(context.Context, string, string, string, int32) (*clients.K8sDeploymentResult, error)
	RestartDeploymentForCluster(context.Context, string, string, string) (*clients.K8sDeploymentResult, error)
	DeleteDeploymentForCluster(context.Context, string, string, string) error
}

type ClusterSelector interface {
	ReserveCapacity(string, clusters.GPUReservation, string) error
	ReleaseCapacity(string, string) error
	GrowCapacity(string, string, string, string, int32) error
	Get(string) (clusters.Cluster, error)
	Select(domain.Resource, string, string) (clusters.Cluster, error)
	SelectWithServingRoute(domain.Resource, string, string) (clusters.Cluster, error)
	ResolveServingEndpoint(string, string, string) (string, error)
	CheckCapacity(string, domain.Resource) error
	CheckHealth(string) error
	CheckRuntime(string, string, string) error
}

// DeploymentUseCase 部署业务用例
type DeploymentUseCase struct {
	repo   data.DeploymentRepository
	models ModelRegistryClient
	kube   K8sClient
	sm     *domain.DeploymentStateMachine
	now    func() time.Time
	// 默认租户（MVP 单租户）
	defaultTenant string
	// 部署镜像（空则使用 k8sadapter 默认；本机验证用 mock 镜像）
	deploymentImage string
	// 租户配额（R2-4，可为 nil 表示不启用）
	quota *QuotaUseCase
	// 审计日志（R2-4，可为 nil 表示不记录）
	audit *AuditUseCase
	// gateway 可为 nil，供不启动推理网关的单元测试使用。
	gateway         GatewayClient
	tenants         TenantState
	clusterKube     ClusterDeploymentClient
	clusterSelector ClusterSelector
}

// NewDeploymentUseCase 创建用例
func NewDeploymentUseCase(repo data.DeploymentRepository, models ModelRegistryClient, kube K8sClient) *DeploymentUseCase {
	return &DeploymentUseCase{
		repo:          repo,
		models:        models,
		kube:          kube,
		sm:            domain.NewDeploymentStateMachine(),
		now:           time.Now,
		defaultTenant: "default",
	}
}

// SetQuota 启用租户配额校验
func (uc *DeploymentUseCase) SetQuota(quota *QuotaUseCase) {
	uc.quota = quota
}

// SetAudit 启用审计日志
func (uc *DeploymentUseCase) SetAudit(audit *AuditUseCase) {
	uc.audit = audit
}

// SetGateway 注入网关路由客户端。
func (uc *DeploymentUseCase) SetGateway(gateway GatewayClient) {
	uc.gateway = gateway
}
func (uc *DeploymentUseCase) SetTenantState(tenants TenantState) { uc.tenants = tenants }
func (uc *DeploymentUseCase) SetClusterPlacement(selector ClusterSelector, kube ClusterDeploymentClient) {
	uc.clusterSelector, uc.clusterKube = selector, kube
}

func (uc *DeploymentUseCase) createK8sDeployment(ctx context.Context, clusterID string, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return nil, fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.CreateDeploymentForCluster(ctx, clusterID, spec)
	}
	return uc.kube.CreateDeployment(ctx, spec)
}
func (uc *DeploymentUseCase) updateK8sDeployment(ctx context.Context, clusterID string, spec *clients.CreateDeploymentSpec) (*clients.K8sDeploymentResult, error) {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return nil, fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.UpdateDeploymentForCluster(ctx, clusterID, spec)
	}
	return uc.kube.UpdateDeployment(ctx, spec)
}
func (uc *DeploymentUseCase) getK8sDeployment(ctx context.Context, clusterID, name, namespace string) (*clients.K8sDeploymentResult, error) {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return nil, fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.GetDeploymentForCluster(ctx, clusterID, name, namespace)
	}
	return uc.kube.GetDeployment(ctx, name, namespace)
}
func (uc *DeploymentUseCase) scaleK8sDeployment(ctx context.Context, clusterID, name, namespace string, replicas int32) (*clients.K8sDeploymentResult, error) {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return nil, fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.ScaleDeploymentForCluster(ctx, clusterID, name, namespace, replicas)
	}
	return uc.kube.ScaleDeployment(ctx, name, namespace, replicas)
}
func (uc *DeploymentUseCase) restartK8sDeployment(ctx context.Context, clusterID, name, namespace string) (*clients.K8sDeploymentResult, error) {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return nil, fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.RestartDeploymentForCluster(ctx, clusterID, name, namespace)
	}
	return uc.kube.RestartDeployment(ctx, name, namespace)
}
func (uc *DeploymentUseCase) deleteK8sDeployment(ctx context.Context, clusterID, name, namespace string) error {
	if clusterID != "" {
		if uc.clusterKube == nil {
			return fmt.Errorf("cluster adapter pool is not configured")
		}
		return uc.clusterKube.DeleteDeploymentForCluster(ctx, clusterID, name, namespace)
	}
	return uc.kube.DeleteDeployment(ctx, name, namespace)
}

// SetDeploymentImage 设置部署镜像（验证环境注入 mock 镜像）
func (uc *DeploymentUseCase) SetDeploymentImage(image string) {
	uc.deploymentImage = image
}

// CreateDeployment 创建部署（幂等：同名且未删除时返回同一部署；已删除同名允许重建）
func (uc *DeploymentUseCase) CreateDeployment(ctx context.Context, req *apitypes.CreateDeploymentRequest) (*domain.ModelDeployment, error) {
	// 幂等：按名称查已存在部署（跳过已删除的，允许同名重建）
	if existing, err := uc.repo.GetByName(req.Name); err == nil && existing.Status != domain.DeploymentStatusDeleted {
		return existing, nil
	}

	// 1. 校验模型版本
	version, err := uc.models.GetVersion(ctx, req.ModelVersionID)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrModelVersionFound, "模型版本校验失败", err)
	}
	if !version.Deployable() {
		return nil, errcode.New(errcode.ErrModelNotDeployable,
			"模型版本不可部署（状态="+version.Status+"），请先完成校验")
	}
	// 版本对应模型 ID（MVP：版本接口返回 ModelID）
	modelID := version.ModelID
	servingMode := req.ServingMode
	if servingMode == "" {
		servingMode = domain.ServingModeUnified
	}
	if servingMode != domain.ServingModeUnified && servingMode != domain.ServingModeDisaggregated {
		return nil, errcode.New(errcode.ErrBadRequest, "servingMode 必须为 unified 或 disaggregated")
	}
	if servingMode == domain.ServingModeDisaggregated && version.Runtime != domain.RuntimeVLLM {
		return nil, errcode.New(errcode.ErrBadRequest, "disaggregated 模式仅支持 vLLM")
	}
	resource := domain.Resource{GPUType: version.GPUType, GPUCount: version.GPUCount, MemoryMB: version.MemoryMB}
	clusterID := ""

	// 2. 校验资源配额（GPU 足够 + 租户配额）
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = uc.defaultTenant
	}
	if uc.tenants != nil && !uc.tenants.TenantActive(tenantID) {
		return nil, errcode.New(errcode.ErrUnauthorized, "租户已禁用，不能创建部署")
	}
	if uc.clusterSelector != nil {
		placementResource := resource
		placementReplicas := req.Replicas
		if placementReplicas <= 0 {
			placementReplicas = 1
		}
		placementResource.GPUCount *= placementReplicas
		var cluster clusters.Cluster
		var err error
		if uc.gateway != nil {
			cluster, err = uc.clusterSelector.SelectWithServingRoute(placementResource, version.Runtime, tenantID)
		} else {
			cluster, err = uc.clusterSelector.Select(placementResource, version.Runtime, tenantID)
		}
		if err != nil {
			return nil, errcode.Wrap(errcode.ErrIllegalState, "没有符合条件的集群", err)
		}
		clusterID = cluster.ID
	} else {
		if err := uc.checkGPUQuota(ctx, version.GPUType, version.GPUCount, req.Replicas); err != nil {
			return nil, err
		}
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = "tenant-" + tenantID
	}
	if clusterID != "" && uc.gateway != nil {
		if _, err := uc.clusterSelector.ResolveServingEndpoint(clusterID, req.Name, namespace); err != nil {
			return nil, errcode.Wrap(errcode.ErrIllegalState, "集群推理入口不可用", err)
		}
	}
	// R2-4：租户配额预留
	if uc.quota != nil {
		need := version.GPUCount * req.Replicas
		if need <= 0 {
			need = version.GPUCount
		}
		if err := uc.quota.Reserve(ctx, tenantID, version.GPUType, need); err != nil {
			return nil, err
		}
	}

	// 3. 构建部署对象
	now := uc.now()
	d := &domain.ModelDeployment{
		ID:             req.IdempotencyKey,
		Name:           req.Name,
		ModelID:        modelID,
		ModelVersionID: version.ID,
		ModelName:      version.ModelName,
		ModelVersion:   version.Version,
		TenantID:       tenantID,
		Namespace:      namespace,
		ClusterID:      clusterID,
		Replicas:       req.Replicas,
		Resource: domain.Resource{
			GPUType:  version.GPUType,
			GPUCount: version.GPUCount,
			MemoryMB: version.MemoryMB,
		},
		Runtime:     version.Runtime,
		ServingMode: servingMode,
		StartupArgs: req.StartupArgs,
		Status:      domain.DeploymentStatusNew,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if d.Replicas <= 0 {
		d.Replicas = 1
	}

	// 4. 写入状态
	if err := uc.repo.Create(d); err != nil {
		if uc.quota != nil {
			uc.quota.Release(ctx, tenantID, version.GPUType, d.Replicas*version.GPUCount)
		}
		if err == data.ErrConflict {
			return uc.repo.GetByName(req.Name)
		}
		return nil, errcode.Wrap(errcode.ErrInternal, "写入部署失败", err)
	}
	if err := uc.recordRevision(d); err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "记录部署修订失败", err)
	}
	if d.ClusterID != "" {
		reservation := clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, ModelVersionID: d.ModelVersionID, TemplateGeneration: d.Generation, GPUType: d.Resource.GPUType, GPUCount: d.Replicas * d.Resource.GPUCount}
		if err := uc.clusterSelector.ReserveCapacity(d.ClusterID, reservation, d.Runtime); err != nil {
			uc.failDeployment(d.ID, "集群容量预留失败: "+err.Error())
			return nil, errcode.Wrap(errcode.ErrIllegalState, "集群容量预留失败；部署已记录，可删除后重试", err)
		}
	}
	uc.recordEvent(d.ID, "", domain.DeploymentStatusNew, "创建部署请求", getRequestID(ctx), "")
	// R2-4：审计
	if uc.audit != nil {
		uc.audit.Record("deployment.create", "console", tenantID, d.ID, getRequestID(ctx),
			"创建部署 "+d.Name+" (版本 "+version.Version+", "+itoa32(d.Replicas*d.Resource.GPUCount)+" GPU)")
	}

	// 5. 异步提交（VALIDATING → SUBMITTING → K8s）
	// 注意：不能复用请求 ctx（请求返回后即取消），使用独立后台 ctx
	go uc.submit(context.Background(), d)
	return d, nil
}

// submit 异步提交流程：校验 → 提交 K8s → 等待启动
func (uc *DeploymentUseCase) submit(ctx context.Context, d *domain.ModelDeployment) {
	// 校验阶段
	if err := uc.transition(d.ID, domain.DeploymentStatusNew, domain.DeploymentStatusValidating, "开始校验"); err != nil {
		uc.failDeployment(d.ID, "状态流转失败: "+err.Error())
		return
	}
	time.Sleep(200 * time.Millisecond) // 模拟校验耗时
	uc.transition(d.ID, domain.DeploymentStatusValidating, domain.DeploymentStatusSubmitting, "校验通过，提交 Kubernetes")
	uc.submitToK8s(ctx, d)
}

// RebuildDeployment explicitly recreates a failed deployment on another cluster.
// The old cluster may still hold orphaned workloads; callers must acknowledge this risk.
func (uc *DeploymentUseCase) RebuildDeployment(ctx context.Context, id, targetCluster, actor string, acknowledgeOrphans bool) (*domain.ModelDeployment, error) {
	if !acknowledgeOrphans || targetCluster == "" {
		return nil, errcode.New(errcode.ErrBadRequest, "targetClusterId 和 acknowledgeOrphanedResources=true 必填")
	}
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.New(errcode.ErrNotFound, "部署不存在")
	}
	if d.ClusterID == "" || d.ClusterID == targetCluster || d.Status != domain.DeploymentStatusFailed || !strings.HasPrefix(d.Diagnostics, "目标集群不可用:") {
		return nil, errcode.New(errcode.ErrIllegalState, "仅允许将目标集群故障导致的失败部署重建到其他集群")
	}
	if uc.clusterSelector == nil {
		return nil, errcode.New(errcode.ErrIllegalState, "集群放置服务未配置")
	}
	old, err := uc.clusterSelector.Get(d.ClusterID)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "查询原集群失败", err)
	}
	if old.HealthStatus == "healthy" {
		return nil, errcode.New(errcode.ErrIllegalState, "原集群已恢复；请先核实旧工作负载状态")
	}
	if err := uc.clusterSelector.CheckRuntime(targetCluster, d.Runtime, d.TenantID); err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "目标集群不可用", err)
	}
	need := d.Resource
	if int64(need.GPUCount)*int64(d.Replicas) > 1<<31-1 {
		return nil, errcode.New(errcode.ErrBadRequest, "重建 GPU 请求总量过大")
	}
	need.GPUCount *= d.Replicas
	if err := uc.clusterSelector.CheckCapacity(targetCluster, need); err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "目标集群容量不足", err)
	}
	if uc.gateway != nil {
		if _, err := uc.clusterSelector.ResolveServingEndpoint(targetCluster, d.Name, d.Namespace); err != nil {
			return nil, errcode.Wrap(errcode.ErrIllegalState, "目标集群推理入口不可用", err)
		}
	}
	oldCluster := d.ClusterID
	claimed, err := uc.repo.ClaimClusterRebuild(id, oldCluster, targetCluster, d.Generation, uc.now())
	if err != nil {
		if err == data.ErrConflict {
			return nil, errcode.New(errcode.ErrIllegalState, "部署状态已改变，请刷新后重试")
		}
		return nil, errcode.Wrap(errcode.ErrInternal, "锁定重建操作失败", err)
	}
	uc.recordEvent(id, domain.DeploymentStatusFailed, domain.DeploymentStatusSubmitting, "人工跨集群重建；原集群 "+oldCluster+" 可能残留资源", getRequestID(ctx), "")
	if uc.audit != nil {
		uc.audit.Record("deployment.cluster_rebuild", actor, d.TenantID, id, getRequestID(ctx), "从 "+oldCluster+" 认领重建到 "+targetCluster+"；需清理原集群残留资源")
	}
	reservation := clusters.GPUReservation{DeploymentID: claimed.ID, TenantID: claimed.TenantID, Namespace: claimed.Namespace, ModelVersionID: claimed.ModelVersionID, TemplateGeneration: claimed.Generation, GPUType: claimed.Resource.GPUType, GPUCount: claimed.Resource.GPUCount * claimed.Replicas}
	if err := uc.clusterSelector.ReserveCapacity(targetCluster, reservation, claimed.Runtime); err != nil {
		diagnostics := "目标集群不可用: 原集群 " + oldCluster + "；重建目标容量预留失败: " + err.Error()
		if restoreErr := uc.repo.AbortClusterRebuild(id, targetCluster, oldCluster, claimed.Generation, diagnostics, uc.now()); restoreErr != nil {
			return nil, errcode.Wrap(errcode.ErrInternal, "重建未提交，但恢复原集群归属失败；请核实部署状态", restoreErr)
		}
		uc.recordEvent(id, domain.DeploymentStatusSubmitting, domain.DeploymentStatusFailed, diagnostics, getRequestID(ctx), "")
		if uc.audit != nil {
			uc.audit.Record("deployment.cluster_rebuild.rejected", actor, d.TenantID, id, getRequestID(ctx), "目标 "+targetCluster+" 容量预留失败，恢复原集群 "+oldCluster+" 归属；未提交工作负载")
		}
		return nil, errcode.Wrap(errcode.ErrIllegalState, "重建容量预留失败；未提交目标工作负载，原集群残留仍需核实", err)
	}
	if err := uc.recordRevision(claimed); err != nil {
		uc.failDeployment(id, "记录重建修订失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "记录重建修订失败", err)
	}
	go uc.submitToK8s(context.Background(), claimed)
	return claimed, nil
}

func (uc *DeploymentUseCase) submitToK8s(ctx context.Context, d *domain.ModelDeployment) {
	version, err := uc.models.GetVersion(ctx, d.ModelVersionID)
	if err != nil || !version.Deployable() {
		uc.failDeployment(d.ID, "模型 artifact 元数据不可用")
		return
	}
	artifactURI, artifactDigest := deployableArtifact(version)

	// 提交 K8s
	spec := &clients.CreateDeploymentSpec{
		DeploymentID: d.ID,
		ClusterID:    d.ClusterID,
		Name:         d.Name,
		Namespace:    d.Namespace,
		Replicas:     d.Replicas,
		Resource:     d.Resource,
		Image:        uc.deploymentImage,
		Args:         d.StartupArgs,
		Labels: map[string]string{
			"carrot.ai/deployment-id":       d.ID,
			"carrot.ai/template-generation": strconv.FormatInt(d.Generation, 10),
			"carrot.ai/model-id":            d.ModelID,
			"carrot.ai/model-version":       d.ModelVersion,
			"carrot.ai/tenant-id":           d.TenantID,
			"carrot.ai/managed-by":          "carrot",
		},
		ModelPath:      deploymentModelPath(d),
		ArtifactURI:    artifactURI,
		ArtifactDigest: artifactDigest,
		Runtime:        d.Runtime,
		ServingMode:    d.ServingMode,
	}
	res, err := uc.createK8sDeployment(ctx, d.ClusterID, spec)
	if err != nil {
		uc.failDeployment(d.ID, "提交 Kubernetes 失败: "+err.Error())
		return
	}
	uc.transition(d.ID, domain.DeploymentStatusSubmitting, domain.DeploymentStatusStarting, "已创建 Kubernetes 资源")

	// 保存 Gateway 可达的 Endpoint。
	if res.Endpoint != "" || (d.ClusterID != "" && uc.gateway != nil) {
		endpoint, stable, canary, err := uc.servingEndpoints(d, res)
		if err != nil {
			uc.failDeployment(d.ID, "集群推理入口解析失败: "+err.Error())
			return
		}
		uc.updateDeployment(d.ID, func(dd *domain.ModelDeployment) {
			dd.Endpoint = endpoint
			dd.StableEndpoint = stable
			dd.CanaryEndpoint = canary
			dd.RolloutStatus = res.RolloutStatus
		})
	}

	// 等待 Running（Reconciler 也会推进，这里只做首次同步）
	uc.SyncFromK8s(ctx, d.ID)
}

// transition 执行状态转换并记录事件
func (uc *DeploymentUseCase) transition(id, from, to, reason string) error {
	if err := uc.sm.Transition(from, to); err != nil {
		uc.recordEvent(id, from, to, "非法转换: "+err.Error(), getRequestID(context.Background()), "")
		return err
	}
	current, err := uc.repo.Get(id)
	if err != nil {
		return err
	}
	if err := uc.repo.CompareStatus(id, from, to, current.Generation, uc.now()); err != nil {
		return err
	}
	uc.recordEvent(id, from, to, reason, getRequestID(context.Background()), "")
	return nil
}

// failDeployment 置为失败
func (uc *DeploymentUseCase) failDeployment(id, diag string) {
	uc.FailDeployment(id, diag)
}

// FailDeployment 置为失败（导出，供 Reconciler 超时判定调用）
func (uc *DeploymentUseCase) FailDeployment(id, diag string) {
	uc.updateDeployment(id, func(d *domain.ModelDeployment) {
		from := d.Status
		if uc.sm.CanTransition(from, domain.DeploymentStatusFailed) {
			d.Status = domain.DeploymentStatusFailed
			d.Diagnostics = diag
			d.UpdatedAt = uc.now()
			uc.recordEvent(id, from, domain.DeploymentStatusFailed, "部署失败", getRequestID(context.Background()), diag)
		}
	})
}

// RetryDelete 删除重试（DELETING 超时后由 Reconciler 调用，幂等）
func (uc *DeploymentUseCase) RetryDelete(ctx context.Context, id string) {
	d, err := uc.repo.Get(id)
	if err != nil || d.Status != domain.DeploymentStatusDeleting {
		return
	}
	_ = uc.finishDelete(ctx, d)
}

// updateDeployment 更新部署（乐观并发）
func (uc *DeploymentUseCase) updateDeployment(id string, fn func(*domain.ModelDeployment)) error {
	d, err := uc.repo.Get(id)
	if err != nil {
		return err
	}
	fn(d)
	d.UpdatedAt = uc.now()
	return uc.repo.Update(d)
}

// recordEvent 记录状态事件
func (uc *DeploymentUseCase) recordEvent(id, from, to, reason, requestID, diag string) {
	uc.repo.AddEvent(&domain.StatusEvent{
		DeploymentID: id,
		From:         from,
		To:           to,
		Reason:       reason,
		RequestID:    requestID,
		Diagnostics:  diag,
		At:           uc.now(),
	})
}

// GetDeployment 查询部署
func (uc *DeploymentUseCase) GetDeployment(id string) (*domain.ModelDeployment, error) {
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	return d, nil
}

// GetDeploymentWithK8sStatus 查询部署并附上实际 K8s 状态（Pod 副本就绪情况）。
// 供服务详情页使用；K8s 查询失败时降级返回本地状态（不阻塞详情展示）。
func (uc *DeploymentUseCase) GetDeploymentWithK8sStatus(ctx context.Context, id string) (*domain.ModelDeployment, *apitypes.PodStatusView, error) {
	d, err := uc.GetDeployment(id)
	if err != nil {
		return nil, nil, err
	}
	pv := &apitypes.PodStatusView{Ready: 0, Desired: d.Replicas, Available: 0}
	if d.Status == domain.DeploymentStatusDeleted || d.Status == domain.DeploymentStatusDeleting {
		return d, pv, nil
	}
	res, err := uc.getK8sDeployment(ctx, d.ClusterID, d.Name, d.Namespace)
	if err != nil || res.Status == nil {
		return d, pv, nil // 降级
	}
	pv.Ready = res.Status.ReadyReplicas
	pv.Available = res.Status.AvailableReplicas
	return d, pv, nil
}

// ListDeployments 部署列表
func (uc *DeploymentUseCase) ListDeployments(tenantID string) ([]*domain.ModelDeployment, error) {
	return uc.repo.List(tenantID)
}

// ListK8sDeployments 扫描 K8s 中全部受管部署（R2-2：孤儿检测）
// 返回完整资源身份，避免跨 namespace 或跨集群同名资源互相遮蔽。
func (uc *DeploymentUseCase) ListK8sDeployments(ctx context.Context, namespace string) (map[string]bool, error) {
	var list []*clients.K8sDeploymentResult
	var err error
	if allClusters, ok := uc.clusterKube.(interface {
		ListDeploymentsAcrossClusters(context.Context, string) ([]*clients.K8sDeploymentResult, error)
	}); ok {
		list, err = allClusters.ListDeploymentsAcrossClusters(ctx, namespace)
	} else {
		list, err = uc.kube.ListDeployments(ctx, namespace)
	}
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, res := range list {
		if res.Name != "" && res.Namespace != "" {
			out[res.ClusterID+"/"+res.Namespace+"/"+res.Name] = true
		}
	}
	return out, nil
}

func (uc *DeploymentUseCase) RecordOrphan(identity string) {
	if uc.audit != nil {
		uc.audit.Record("deployment.orphan.detected", "reconciler", "", identity, "", "受管资源无匹配部署记录；需核实所属集群和命名空间后人工处置")
	}
}

// ScaleDeployment 扩缩容（幂等）
func (uc *DeploymentUseCase) ScaleDeployment(ctx context.Context, id string, replicas int32) (*domain.ModelDeployment, error) {
	if replicas < 0 {
		return nil, errcode.New(errcode.ErrBadRequest, "副本数不能为负数")
	}
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	if d.Status != domain.DeploymentStatusRunning && d.Status != domain.DeploymentStatusFailed {
		return nil, errcode.New(errcode.ErrIllegalState, "当前状态 "+d.Status+" 不允许扩缩容")
	}
	if replicas == d.Replicas && d.Status == domain.DeploymentStatusRunning {
		return d, nil
	}
	if int64(replicas)*int64(d.Resource.GPUCount) > 1<<31-1 {
		return nil, errcode.New(errcode.ErrBadRequest, "GPU 请求总量过大")
	}
	// 配额校验；多集群部署在原集群验证新增容量，不重新放置。
	if d.ClusterID != "" {
		if uc.clusterSelector == nil {
			return nil, errcode.New(errcode.ErrIllegalState, "集群放置服务未配置")
		}
	} else if err := uc.checkGPUQuota(ctx, d.Resource.GPUType, d.Resource.GPUCount, replicas); err != nil {
		return nil, err
	}
	delta := (replicas - d.Replicas) * d.Resource.GPUCount
	previousReplicas, previousStatus := d.Replicas, d.Status
	claimed, err := uc.repo.ClaimScale(id, d.Generation, replicas, uc.now())
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "部署已被其他操作修改，请刷新后重试", err)
	}
	d = claimed
	uc.recordEvent(id, previousStatus, domain.DeploymentStatusScaling, "发起扩缩容到 "+itoa32(replicas), getRequestID(ctx), "")
	// 缩容确认成功后再释放；扩容失败可能已经创建资源，保留预留。
	if uc.quota != nil {
		if delta > 0 {
			if err := uc.quota.Reserve(ctx, d.TenantID, d.Resource.GPUType, delta); err != nil {
				uc.updateDeployment(id, func(dd *domain.ModelDeployment) { dd.Replicas = previousReplicas })
				uc.failDeployment(id, "扩容配额预留失败: "+err.Error())
				return nil, err
			}
		}
	}

	if d.ClusterID != "" && delta > 0 {
		if err := uc.clusterSelector.GrowCapacity(d.ClusterID, d.ID, d.Resource.GPUType, d.ModelVersionID, replicas*d.Resource.GPUCount); err != nil {
			if uc.quota != nil {
				uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, delta)
			}
			uc.updateDeployment(id, func(dd *domain.ModelDeployment) { dd.Replicas = previousReplicas })
			uc.failDeployment(id, "扩容集群容量预留失败: "+err.Error())
			return nil, errcode.Wrap(errcode.ErrIllegalState, "原集群容量不足或无预留账本", err)
		}
	}

	// 调 K8s
	res, err := uc.scaleK8sDeployment(ctx, d.ClusterID, d.Name, d.Namespace, replicas)
	if err != nil {
		if delta < 0 {
			uc.updateDeployment(id, func(dd *domain.ModelDeployment) { dd.Replicas = previousReplicas })
		}
		uc.failDeployment(id, "扩缩容失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "扩缩容失败", err)
	}
	_ = res
	if uc.quota != nil && delta < 0 {
		uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, -delta)
	}

	// SCALING → RUNNING
	if d.Status == domain.DeploymentStatusScaling {
		uc.transition(id, domain.DeploymentStatusScaling, domain.DeploymentStatusRunning, "扩缩容完成")
	}
	// R2-4：审计
	if uc.audit != nil {
		uc.audit.Record("deployment.scale", "console", d.TenantID, id, getRequestID(ctx),
			"扩缩容 "+d.Name+" 到 "+itoa32(replicas)+" 副本")
	}
	return uc.repo.Get(id)
}

// RestartDeployment 触发 Kubernetes Deployment 的滚动重启。
func (uc *DeploymentUseCase) RestartDeployment(ctx context.Context, id string) (*domain.ModelDeployment, error) {
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	if d.Status != domain.DeploymentStatusRunning {
		return nil, errcode.New(errcode.ErrIllegalState, "当前状态 "+d.Status+" 不允许重启")
	}
	if err := uc.repo.CompareStatus(id, d.Status, domain.DeploymentStatusRestarting, d.Generation, uc.now()); err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "部署状态已改变", err)
	}
	uc.recordEvent(id, d.Status, domain.DeploymentStatusRestarting, "发起重启", getRequestID(ctx), "")
	if _, err := uc.restartK8sDeployment(ctx, d.ClusterID, d.Name, d.Namespace); err != nil {
		uc.failDeployment(id, "重启失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "重启失败", err)
	}
	uc.transition(id, domain.DeploymentStatusRestarting, domain.DeploymentStatusStarting, "Kubernetes 已触发滚动重启")
	uc.SyncFromK8s(ctx, id)
	return uc.repo.Get(id)
}

// UpgradeDeployment 升级/回滚部署：切换到新模型版本（R3 灰度/回滚基础）。
// 校验新版本 RELEASED + 配额，更新期望状态并触发 K8s 滚动更新。
func (uc *DeploymentUseCase) UpgradeDeployment(ctx context.Context, id, newVersionID string) (*domain.ModelDeployment, error) {
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	if d.Status != domain.DeploymentStatusRunning {
		return nil, errcode.New(errcode.ErrIllegalState, "当前状态 "+d.Status+" 不允许升级")
	}
	if newVersionID == "" || newVersionID == d.ModelVersionID {
		return nil, errcode.New(errcode.ErrBadRequest, "新版本 ID 必填且不能与当前相同")
	}

	// 校验新版本
	version, err := uc.models.GetVersion(ctx, newVersionID)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrModelVersionFound, "模型版本校验失败", err)
	}
	if !version.Deployable() {
		return nil, errcode.New(errcode.ErrModelNotDeployable,
			"新版本不可部署（状态="+version.Status+"），仅 RELEASED 可升级")
	}
	if int64(version.GPUCount)*int64(d.Replicas) > 1<<31-1 {
		return nil, errcode.New(errcode.ErrBadRequest, "升级 GPU 请求总量过大")
	}
	if d.ClusterID != "" {
		if uc.clusterSelector == nil {
			return nil, errcode.New(errcode.ErrIllegalState, "集群放置服务未配置")
		}
		if err := uc.clusterSelector.CheckRuntime(d.ClusterID, version.Runtime, d.TenantID); err != nil {
			return nil, errcode.Wrap(errcode.ErrIllegalState, "原集群不支持升级", err)
		}
		additional := version.GPUCount * d.Replicas
		if version.GPUType == d.Resource.GPUType {
			additional -= d.Resource.GPUCount * d.Replicas
		}
		if additional > 0 {
			if err := uc.clusterSelector.CheckCapacity(d.ClusterID, domain.Resource{GPUType: version.GPUType, GPUCount: additional}); err != nil {
				return nil, errcode.Wrap(errcode.ErrIllegalState, "原集群容量不足", err)
			}
		}
	}
	if d.ServingMode == domain.ServingModeDisaggregated && version.Runtime != domain.RuntimeVLLM {
		return nil, errcode.New(errcode.ErrBadRequest, "disaggregated 部署不能升级到非 vLLM 运行时")
	}
	if err := uc.repo.CompareStatus(id, d.Status, domain.DeploymentStatusRestarting, d.Generation, uc.now()); err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "部署状态已改变", err)
	}
	uc.recordEvent(id, d.Status, domain.DeploymentStatusRestarting, "升级到版本 "+version.Version, getRequestID(ctx), "")
	if d.ClusterID != "" {
		reservation := clusters.GPUReservation{DeploymentID: d.ID, TenantID: d.TenantID, Namespace: d.Namespace, ModelVersionID: version.ID, TemplateGeneration: d.Generation + 1, GPUType: version.GPUType, GPUCount: version.GPUCount * d.Replicas}
		if err := uc.clusterSelector.ReserveCapacity(d.ClusterID, reservation, version.Runtime); err != nil {
			uc.failDeployment(id, "升级集群容量预留失败: "+err.Error())
			return nil, errcode.Wrap(errcode.ErrIllegalState, "升级重叠容量不足；旧模板预留保持不变", err)
		}
	}
	// 配额：新版本 GPU 需求变化时校验（资源规格可能不同）
	if uc.quota != nil {
		need := version.GPUCount * d.Replicas
		if err := uc.quota.Reserve(ctx, d.TenantID, version.GPUType, need); err != nil {
			uc.failDeployment(id, "升级配额预留失败: "+err.Error())
			return nil, err
		}
		// 释放旧版本占用（GPU 型号/数量可能不同）
		uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, d.Resource.GPUCount*d.Replicas)
	}

	// 更新期望状态
	uc.updateDeployment(id, func(dd *domain.ModelDeployment) {
		dd.ModelVersionID = version.ID
		dd.ModelName = version.ModelName
		dd.ModelVersion = version.Version
		dd.Resource = domain.Resource{
			GPUType:  version.GPUType,
			GPUCount: version.GPUCount,
			MemoryMB: version.MemoryMB,
		}
		dd.Runtime = version.Runtime
		dd.Generation++
	})

	updated, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "读取升级后部署失败", err)
	}
	artifactURI, artifactDigest := deployableArtifact(version)
	// 触发 K8s 滚动更新：更新现有 Deployment 的 Pod template。
	spec := &clients.CreateDeploymentSpec{
		DeploymentID: updated.ID,
		ClusterID:    updated.ClusterID,
		Name:         updated.Name,
		Namespace:    updated.Namespace,
		Replicas:     updated.Replicas,
		Resource:     domain.Resource{GPUType: version.GPUType, GPUCount: version.GPUCount, MemoryMB: version.MemoryMB},
		Image:        uc.deploymentImage,
		Args:         updated.StartupArgs,
		Labels: map[string]string{
			"carrot.ai/deployment-id":       updated.ID,
			"carrot.ai/template-generation": strconv.FormatInt(updated.Generation, 10),
			"carrot.ai/model-id":            version.ModelID,
			"carrot.ai/model-version":       version.Version,
			"carrot.ai/tenant-id":           updated.TenantID,
			"carrot.ai/managed-by":          "carrot",
		},
		ModelPath:      deploymentModelPath(updated),
		ArtifactURI:    artifactURI,
		ArtifactDigest: artifactDigest,
		Runtime:        version.Runtime,
		ServingMode:    updated.ServingMode,
	}
	res, err := uc.updateK8sDeployment(ctx, updated.ClusterID, spec)
	if err != nil {
		uc.failDeployment(id, "升级失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "升级失败", err)
	}
	if res.Status != nil && res.Status.Condition == "Available" {
		if err := uc.recordRevision(updated); err != nil && err != data.ErrConflict {
			uc.failDeployment(id, "记录升级修订失败: "+err.Error())
			return nil, errcode.Wrap(errcode.ErrInternal, "记录升级修订失败", err)
		}
		uc.transition(id, domain.DeploymentStatusRestarting, domain.DeploymentStatusRunning, "升级完成")
	}
	uc.SyncFromK8s(ctx, id)

	// 审计
	if uc.audit != nil {
		uc.audit.Record("deployment.upgrade", "console", d.TenantID, id, getRequestID(ctx),
			"升级部署 "+d.Name+" 到版本 "+version.Version)
	}
	return uc.repo.Get(id)
}

func (uc *DeploymentUseCase) RollbackDeployment(ctx context.Context, id, revisionID string) (*domain.ModelDeployment, error) {
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	if d.Status != domain.DeploymentStatusRunning {
		return nil, errcode.New(errcode.ErrIllegalState, "当前状态 "+d.Status+" 不允许回滚")
	}
	revisions, err := uc.repo.Revisions(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "读取修订历史失败", err)
	}
	var target *domain.DeploymentRevision
	for i := range revisions {
		candidate := &revisions[i]
		if revisionID != "" && candidate.ID == revisionID {
			target = candidate
			break
		}
		if revisionID == "" && candidate.ModelVersionID != d.ModelVersionID {
			target = candidate
			break
		}
	}
	if target == nil {
		return nil, errcode.New(errcode.ErrNotFound, "没有可回滚的部署修订")
	}
	if target.ModelVersionID == d.ModelVersionID {
		return nil, errcode.New(errcode.ErrBadRequest, "目标修订与当前版本相同")
	}
	result, err := uc.UpgradeDeployment(ctx, id, target.ModelVersionID)
	if err != nil {
		return nil, err
	}
	uc.recordEvent(id, domain.DeploymentStatusRunning, domain.DeploymentStatusRunning, "回滚到修订 "+target.ID, getRequestID(ctx), "")
	if uc.audit != nil {
		uc.audit.Record("deployment.rollback", "console", d.TenantID, id, getRequestID(ctx), "回滚部署 "+d.Name+" 到修订 "+target.ID)
	}
	return result, nil
}

func (uc *DeploymentUseCase) Revisions(id string) ([]domain.DeploymentRevision, error) {
	return uc.repo.Revisions(id)
}

func (uc *DeploymentUseCase) recordRevision(d *domain.ModelDeployment) error {
	return uc.repo.AddRevision(&domain.DeploymentRevision{ID: fmt.Sprintf("%s-r%d", d.ID, d.Generation), DeploymentID: d.ID, Generation: d.Generation, ModelVersionID: d.ModelVersionID, ModelVersion: d.ModelVersion, CreatedAt: uc.now().UTC()})
}

// DeleteDeployment 删除部署（幂等：不存在返回成功）
func (uc *DeploymentUseCase) DeleteDeployment(ctx context.Context, id string) error {
	d, err := uc.repo.Get(id)
	if err != nil {
		if err == data.ErrNotFound {
			return nil // 幂等删除
		}
		return errcode.Wrap(errcode.ErrInternal, "查询部署失败", err)
	}
	if d.Status == domain.DeploymentStatusDeleted {
		if uc.gateway != nil {
			return uc.gateway.UnregisterRoute(ctx, d.Name)
		}
		return nil
	}
	if d.Status == domain.DeploymentStatusScaling {
		return errcode.New(errcode.ErrIllegalState, "扩缩容操作进行中，暂不能删除")
	}
	if d.Status == domain.DeploymentStatusDeleting {
		return errcode.New(errcode.ErrIllegalState, "删除已在进行中，后台将重试")
	}
	// 记录删除事件（任何状态均可删除）
	if err := uc.repo.CompareStatus(id, d.Status, domain.DeploymentStatusDeleting, d.Generation, uc.now()); err != nil {
		return errcode.Wrap(errcode.ErrIllegalState, "部署状态已改变", err)
	}
	uc.recordEvent(id, d.Status, domain.DeploymentStatusDeleting, "发起删除", getRequestID(ctx), "")
	return uc.finishDelete(ctx, d)
}

func (uc *DeploymentUseCase) finishDelete(ctx context.Context, d *domain.ModelDeployment) error {
	id := d.ID
	// 调 K8s 删除（幂等）
	if err := uc.deleteK8sDeployment(ctx, d.ClusterID, d.Name, d.Namespace); err != nil {
		uc.recordEvent(id, domain.DeploymentStatusDeleting, domain.DeploymentStatusDeleting, "删除尚未确认，保留预留并等待重试", getRequestID(ctx), err.Error())
		return errcode.Wrap(errcode.ErrInternal, "删除尚未完成，后台将重试", err)
	}
	if d.ClusterID != "" && uc.clusterSelector != nil {
		if err := uc.clusterSelector.ReleaseCapacity(d.ClusterID, d.ID); err != nil {
			return errcode.Wrap(errcode.ErrInternal, "释放集群容量失败，等待删除重试", err)
		}
	}
	if uc.quota != nil {
		if err := uc.quota.ReleaseDeployment(id, d.TenantID, d.Resource.GPUType, d.Replicas*d.Resource.GPUCount); err != nil {
			return errcode.Wrap(errcode.ErrInternal, "释放租户配额失败，等待删除重试", err)
		}
	}
	// Route removal is also retried before entering the terminal state.
	if uc.gateway != nil {
		if err := uc.gateway.UnregisterRoute(ctx, d.Name); err != nil {
			return errcode.Wrap(errcode.ErrUpstream, "撤销网关路由失败，等待删除重试", err)
		}
	}
	if err := uc.repo.CompareStatus(id, domain.DeploymentStatusDeleting, domain.DeploymentStatusDeleted, d.Generation, uc.now()); err != nil {
		current, readErr := uc.repo.Get(id)
		if readErr == nil && current.Status == domain.DeploymentStatusDeleted {
			return nil
		}
		return errcode.Wrap(errcode.ErrInternal, "记录删除完成失败，等待重试", err)
	}
	uc.recordEvent(id, domain.DeploymentStatusDeleting, domain.DeploymentStatusDeleted, "删除完成", getRequestID(ctx), "")
	// R2-4：审计
	if uc.audit != nil {
		uc.audit.Record("deployment.delete", "console", d.TenantID, id, getRequestID(ctx),
			"删除部署 "+d.Name+"，释放 "+itoa32(d.Replicas*d.Resource.GPUCount)+" GPU")
	}
	return nil
}

// SyncFromK8s 从 K8s 同步部署状态（Reconciler 与提交后首次同步共用）
func (uc *DeploymentUseCase) SyncFromK8s(ctx context.Context, id string) {
	d, err := uc.repo.Get(id)
	if err != nil || d.Status == domain.DeploymentStatusDeleted || d.Status == domain.DeploymentStatusDeleting || d.Status == domain.DeploymentStatusScaling {
		return
	}
	clusterFailure := d.Status == domain.DeploymentStatusFailed && strings.HasPrefix(d.Diagnostics, "目标集群不可用:")
	if d.ClusterID != "" && uc.clusterSelector != nil && (d.Status == domain.DeploymentStatusRunning || clusterFailure) {
		if err := uc.clusterSelector.CheckHealth(d.ClusterID); err != nil {
			if uc.gateway != nil {
				_ = uc.gateway.UnregisterRoute(ctx, d.Name)
			}
			if !clusterFailure {
				uc.failDeployment(id, "目标集群不可用: "+err.Error())
				if uc.audit != nil {
					uc.audit.Record("deployment.cluster_unhealthy", "reconciler", d.TenantID, id, getRequestID(ctx), err.Error())
				}
			}
			return
		}
	}
	res, err := uc.getK8sDeployment(ctx, d.ClusterID, d.Name, d.Namespace)
	if err != nil {
		// 不存在说明可能还没创建完，忽略
		return
	}
	if res.Status == nil {
		return
	}
	endpoint, stableEndpoint, canaryEndpoint, err := uc.servingEndpoints(d, res)
	if err != nil {
		if uc.gateway != nil {
			_ = uc.gateway.UnregisterRoute(ctx, d.Name)
		}
		uc.failDeployment(id, "集群推理入口解析失败: "+err.Error())
		return
	}
	routeChanged := d.Endpoint != endpoint || d.StableEndpoint != stableEndpoint || d.CanaryEndpoint != canaryEndpoint || d.RolloutStatus != res.RolloutStatus
	if routeChanged {
		uc.updateDeployment(id, func(current *domain.ModelDeployment) {
			current.Endpoint = endpoint
			current.StableEndpoint = stableEndpoint
			current.CanaryEndpoint = canaryEndpoint
			current.RolloutStatus = res.RolloutStatus
		})
		d, _ = uc.repo.Get(id)
	}
	if d.Status == domain.DeploymentStatusRestarting && uc.gateway != nil && d.Endpoint != "" {
		if err := uc.gateway.RegisterRoute(ctx, d.Name, d.ModelID, d.Endpoint, d.TenantID, d.ID, d.ClusterID, d.StableEndpoint, d.CanaryEndpoint, d.RolloutStatus); err != nil {
			uc.failDeployment(id, "更新网关 rollout 路由失败: "+err.Error())
			return
		}
	}
	switch res.Status.Condition {
	case "Available":
		if d.Status == domain.DeploymentStatusRestarting {
			_ = uc.recordRevision(d)
		}
		if uc.sm.CanTransition(d.Status, domain.DeploymentStatusRunning) {
			uc.transition(id, d.Status, domain.DeploymentStatusRunning, "Pod 就绪")
		}
		if uc.gateway != nil && d.Endpoint != "" && (routeChanged || d.Status != domain.DeploymentStatusRunning) && d.Status != domain.DeploymentStatusFailed {
			if err := uc.gateway.RegisterRoute(ctx, d.Name, d.ModelID, d.Endpoint, d.TenantID, d.ID, d.ClusterID, d.StableEndpoint, d.CanaryEndpoint, d.RolloutStatus); err != nil {
				uc.failDeployment(id, "注册网关路由失败: "+err.Error())
			}
		}
	case "ReplicaFailure":
		diag := res.Message
		if diag == "" {
			diag = "Pod 运行失败"
		}
		uc.restoreLastStableRevision(ctx, d)
		uc.failDeployment(id, diag)
	}
}

func (uc *DeploymentUseCase) servingEndpoints(d *domain.ModelDeployment, res *clients.K8sDeploymentResult) (endpoint, stable, canary string, err error) {
	if res.Endpoint == "" {
		if d.ClusterID != "" && uc.gateway != nil {
			return "", "", "", fmt.Errorf("cluster adapter did not return a serving endpoint")
		}
		return "", "", "", nil
	}
	if d.ClusterID == "" || uc.gateway == nil {
		return endpointURL(res.Endpoint), endpointURL(res.StableEndpoint), endpointURL(res.CanaryEndpoint), nil
	}
	endpoint, err = uc.clusterSelector.ResolveServingEndpoint(d.ClusterID, d.Name, d.Namespace)
	if err != nil {
		return "", "", "", err
	}
	if res.StableEndpoint != "" {
		stable, err = uc.clusterSelector.ResolveServingEndpoint(d.ClusterID, d.Name+"-stable", d.Namespace)
		if err != nil {
			return "", "", "", err
		}
	}
	if res.CanaryEndpoint != "" {
		canary, err = uc.clusterSelector.ResolveServingEndpoint(d.ClusterID, d.Name+"-canary", d.Namespace)
		if err != nil {
			return "", "", "", err
		}
	}
	return endpoint, stable, canary, nil
}

func endpointURL(endpoint string) string {
	if endpoint == "" {
		return ""
	}
	if strings.Contains(endpoint, "://") {
		return endpoint
	}
	return "http://" + endpoint
}

func (uc *DeploymentUseCase) restoreLastStableRevision(ctx context.Context, d *domain.ModelDeployment) {
	revisions, err := uc.repo.Revisions(d.ID)
	if err != nil {
		return
	}
	for _, revision := range revisions {
		if revision.ModelVersionID == d.ModelVersionID {
			continue
		}
		version, err := uc.models.GetVersion(ctx, revision.ModelVersionID)
		if err != nil {
			return
		}
		uc.updateDeployment(d.ID, func(current *domain.ModelDeployment) {
			current.ModelVersionID = version.ID
			current.ModelID = version.ModelID
			current.ModelName = version.ModelName
			current.ModelVersion = version.Version
			current.Runtime = version.Runtime
			current.Resource = domain.Resource{GPUType: version.GPUType, GPUCount: version.GPUCount, MemoryMB: version.MemoryMB}
		})
		restored, err := uc.repo.Get(d.ID)
		if err == nil {
			_ = uc.recordRevision(restored)
		}
		return
	}
}

// checkGPUQuota 校验 GPU 资源与租户配额。
// MVP 单租户：检查集群该 GPU 型号是否有足够可用量。
func (uc *DeploymentUseCase) checkGPUQuota(ctx context.Context, gpuType string, gpuCount, replicas int32) error {
	if replicas <= 0 {
		replicas = 1
	}
	need := gpuCount * replicas
	nodes, err := uc.kube.ListGPUs(ctx)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, "查询 GPU 资源失败", err)
	}
	var available int32
	for _, n := range nodes {
		if n.GPUType == gpuType {
			available += n.Available()
		}
	}
	if available < need {
		return errcode.New(errcode.ErrInsufficientGPU,
			"GPU 资源不足：需要 "+itoa32(need)+" 张 "+gpuType+"，当前可用 "+itoa32(available))
	}
	return nil
}

// deploymentModelPath 计算模型权重路径。
// MVP：模型路径来自版本 ArtifactURI 的简化解析（真实场景由模型存储适配器注入）。
func deploymentModelPath(d *domain.ModelDeployment) string {
	return "/models/" + d.ModelName
}

func deployableArtifact(version *domain.ModelVersion) (string, string) {
	if version != nil && strings.HasPrefix(version.ArtifactDigest, "sha256:") {
		return version.ArtifactURI, version.ArtifactDigest
	}
	return "", ""
}

// getRequestID 从 context 取 RequestID（biz 层不依赖 middleware 包）
func getRequestID(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

type requestIDKey struct{}

// itoa32 int32 转字符串
func itoa32(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
