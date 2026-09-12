// Package biz 控制面业务逻辑
package biz

import (
	"context"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/services/controlplane/internal/clients"
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
	RegisterRoute(ctx context.Context, model, modelID, endpoint, tenantID, deploymentID string) error
	UnregisterRoute(ctx context.Context, model string) error
}
type TenantState interface{ TenantActive(tenantID string) bool }

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
	gateway GatewayClient
	tenants TenantState
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

	// 2. 校验资源配额（GPU 足够 + 租户配额）
	tenantID := req.TenantID
	if tenantID == "" {
		tenantID = uc.defaultTenant
	}
	if uc.tenants != nil && !uc.tenants.TenantActive(tenantID) {
		return nil, errcode.New(errcode.ErrUnauthorized, "租户已禁用，不能创建部署")
	}
	if err := uc.checkGPUQuota(ctx, version.GPUType, version.GPUCount, req.Replicas); err != nil {
		return nil, err
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
	namespace := req.Namespace
	if namespace == "" {
		namespace = "tenant-" + tenantID
	}
	d := &domain.ModelDeployment{
		ID:             req.IdempotencyKey,
		Name:           req.Name,
		ModelID:        modelID,
		ModelVersionID: version.ID,
		ModelName:      version.ModelName,
		ModelVersion:   version.Version,
		TenantID:       tenantID,
		Namespace:      namespace,
		Replicas:       req.Replicas,
		Resource: domain.Resource{
			GPUType:  version.GPUType,
			GPUCount: version.GPUCount,
			MemoryMB: version.MemoryMB,
		},
		Runtime:     version.Runtime,
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
		if err == data.ErrConflict {
			return uc.repo.GetByName(req.Name)
		}
		return nil, errcode.Wrap(errcode.ErrInternal, "写入部署失败", err)
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

	// 提交 K8s
	spec := &clients.CreateDeploymentSpec{
		DeploymentID: d.ID,
		Name:         d.Name,
		Namespace:    d.Namespace,
		Replicas:     d.Replicas,
		Resource:     d.Resource,
		Image:        uc.deploymentImage,
		Args:         d.StartupArgs,
		Labels: map[string]string{
			"carrot.ai/deployment-id": d.ID,
			"carrot.ai/model-id":      d.ModelID,
			"carrot.ai/model-version": d.ModelVersion,
			"carrot.ai/tenant-id":     d.TenantID,
			"carrot.ai/managed-by":    "carrot",
		},
		ModelPath: deploymentModelPath(d),
	}
	res, err := uc.kube.CreateDeployment(ctx, spec)
	if err != nil {
		uc.failDeployment(d.ID, "提交 Kubernetes 失败: "+err.Error())
		return
	}
	uc.transition(d.ID, domain.DeploymentStatusSubmitting, domain.DeploymentStatusStarting, "已创建 Kubernetes 资源")

	// 保存 Endpoint
	if res.Endpoint != "" {
		uc.updateDeployment(d.ID, func(dd *domain.ModelDeployment) {
			dd.Endpoint = "http://" + res.Endpoint
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
	uc.recordEvent(id, from, to, reason, getRequestID(context.Background()), "")
	return uc.updateDeployment(id, func(d *domain.ModelDeployment) {
		d.Status = to
		d.UpdatedAt = uc.now()
	})
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
	if err != nil {
		return
	}
	// 幂等重试删除
	if err := uc.kube.DeleteDeployment(ctx, d.Name, d.Namespace); err != nil {
		uc.failDeployment(id, "删除重试失败: "+err.Error())
		return
	}
	uc.recordEvent(id, domain.DeploymentStatusDeleting, domain.DeploymentStatusDeleted, "删除完成（重试）", getRequestID(ctx), "")
	uc.updateDeployment(id, func(dd *domain.ModelDeployment) {
		dd.Status = domain.DeploymentStatusDeleted
	})
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
	res, err := uc.kube.GetDeployment(ctx, d.Name, d.Namespace)
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
// 返回 map[name]deploymentID；name 用于与本地记录匹配。
func (uc *DeploymentUseCase) ListK8sDeployments(ctx context.Context, namespace string) (map[string]bool, error) {
	list, err := uc.kube.ListDeployments(ctx, namespace)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, res := range list {
		if res.Name != "" {
			out[res.Name] = true
		}
	}
	return out, nil
}

// ScaleDeployment 扩缩容（幂等）
func (uc *DeploymentUseCase) ScaleDeployment(ctx context.Context, id string, replicas int32) (*domain.ModelDeployment, error) {
	d, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrNotFound, "部署不存在: "+id, err)
	}
	if d.Status != domain.DeploymentStatusRunning && d.Status != domain.DeploymentStatusFailed {
		return nil, errcode.New(errcode.ErrIllegalState, "当前状态 "+d.Status+" 不允许扩缩容")
	}
	// 配额校验
	if err := uc.checkGPUQuota(ctx, d.Resource.GPUType, d.Resource.GPUCount, replicas); err != nil {
		return nil, err
	}
	// R2-4：租户配额（扩容需增加预留，缩容释放）
	if uc.quota != nil {
		delta := (replicas - d.Replicas) * d.Resource.GPUCount
		if delta > 0 {
			if err := uc.quota.Reserve(ctx, d.TenantID, d.Resource.GPUType, delta); err != nil {
				return nil, err
			}
		} else if delta < 0 {
			uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, -delta)
		}
	}

	// 状态机：Running → SCALING
	if d.Status == domain.DeploymentStatusRunning {
		if err := uc.transition(id, d.Status, domain.DeploymentStatusScaling, "发起扩缩容到 "+itoa32(replicas)); err != nil {
			return nil, err
		}
	}
	d.Replicas = replicas
	d.Generation++
	uc.repo.Update(d)

	// 调 K8s
	res, err := uc.kube.ScaleDeployment(ctx, d.Name, d.Namespace, replicas)
	if err != nil {
		uc.failDeployment(id, "扩缩容失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "扩缩容失败", err)
	}
	_ = res

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
	uc.transition(id, d.Status, domain.DeploymentStatusRestarting, "发起重启")
	if _, err := uc.kube.RestartDeployment(ctx, d.Name, d.Namespace); err != nil {
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
	// 配额：新版本 GPU 需求变化时校验（资源规格可能不同）
	if uc.quota != nil {
		need := version.GPUCount * d.Replicas
		if err := uc.quota.Reserve(ctx, d.TenantID, version.GPUType, need); err != nil {
			return nil, err
		}
		// 释放旧版本占用（GPU 型号/数量可能不同）
		uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, d.Resource.GPUCount*d.Replicas)
	}

	// 更新期望状态
	uc.transition(id, d.Status, domain.DeploymentStatusRestarting, "升级到版本 "+version.Version)
	uc.updateDeployment(id, func(dd *domain.ModelDeployment) {
		dd.ModelVersionID = version.ID
		dd.ModelName = version.ModelName
		dd.ModelVersion = version.Version
		dd.Resource = domain.Resource{
			GPUType:  version.GPUType,
			GPUCount: version.GPUCount,
			MemoryMB: version.MemoryMB,
		}
		dd.Generation++
	})

	updated, err := uc.repo.Get(id)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "读取升级后部署失败", err)
	}
	// 触发 K8s 滚动更新：更新现有 Deployment 的 Pod template。
	spec := &clients.CreateDeploymentSpec{
		DeploymentID: updated.ID,
		Name:         updated.Name,
		Namespace:    updated.Namespace,
		Replicas:     updated.Replicas,
		Resource:     domain.Resource{GPUType: version.GPUType, GPUCount: version.GPUCount, MemoryMB: version.MemoryMB},
		Image:        uc.deploymentImage,
		Args:         updated.StartupArgs,
		Labels: map[string]string{
			"carrot.ai/deployment-id": updated.ID,
			"carrot.ai/model-id":      version.ModelID,
			"carrot.ai/model-version": version.Version,
			"carrot.ai/tenant-id":     updated.TenantID,
			"carrot.ai/managed-by":    "carrot",
		},
		ModelPath: deploymentModelPath(updated),
	}
	if _, err := uc.kube.UpdateDeployment(ctx, spec); err != nil {
		uc.failDeployment(id, "升级失败: "+err.Error())
		return nil, errcode.Wrap(errcode.ErrInternal, "升级失败", err)
	}
	uc.transition(id, domain.DeploymentStatusRestarting, domain.DeploymentStatusRunning, "升级完成")
	uc.SyncFromK8s(ctx, id)

	// 审计
	if uc.audit != nil {
		uc.audit.Record("deployment.upgrade", "console", d.TenantID, id, getRequestID(ctx),
			"升级部署 "+d.Name+" 到版本 "+version.Version)
	}
	return uc.repo.Get(id)
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
	// 记录删除事件（任何状态均可删除）
	uc.recordEvent(id, d.Status, domain.DeploymentStatusDeleting, "发起删除", getRequestID(ctx), "")
	uc.updateDeployment(id, func(dd *domain.ModelDeployment) {
		dd.Status = domain.DeploymentStatusDeleting
	})

	// 调 K8s 删除（幂等）
	if err := uc.kube.DeleteDeployment(ctx, d.Name, d.Namespace); err != nil {
		uc.failDeployment(id, "删除失败: "+err.Error())
		return errcode.Wrap(errcode.ErrInternal, "删除失败", err)
	}
	uc.recordEvent(id, domain.DeploymentStatusDeleting, domain.DeploymentStatusDeleted, "删除完成", getRequestID(ctx), "")
	uc.updateDeployment(id, func(dd *domain.ModelDeployment) {
		dd.Status = domain.DeploymentStatusDeleted
	})
	// R2-4：释放租户配额
	if uc.quota != nil {
		uc.quota.Release(ctx, d.TenantID, d.Resource.GPUType, d.Replicas*d.Resource.GPUCount)
	}
	// R2-4：审计
	if uc.audit != nil {
		uc.audit.Record("deployment.delete", "console", d.TenantID, id, getRequestID(ctx),
			"删除部署 "+d.Name+"，释放 "+itoa32(d.Replicas*d.Resource.GPUCount)+" GPU")
	}
	if uc.gateway != nil {
		if err := uc.gateway.UnregisterRoute(ctx, d.Name); err != nil {
			return errcode.Wrap(errcode.ErrUpstream, "撤销网关路由失败", err)
		}
	}
	return nil
}

// SyncFromK8s 从 K8s 同步部署状态（Reconciler 与提交后首次同步共用）
func (uc *DeploymentUseCase) SyncFromK8s(ctx context.Context, id string) {
	d, err := uc.repo.Get(id)
	if err != nil || d.Status == domain.DeploymentStatusDeleted || d.Status == domain.DeploymentStatusDeleting {
		return
	}
	res, err := uc.kube.GetDeployment(ctx, d.Name, d.Namespace)
	if err != nil {
		// 不存在说明可能还没创建完，忽略
		return
	}
	if res.Status == nil {
		return
	}
	switch res.Status.Condition {
	case "Available":
		if uc.sm.CanTransition(d.Status, domain.DeploymentStatusRunning) {
			uc.transition(id, d.Status, domain.DeploymentStatusRunning, "Pod 就绪")
		}
		if uc.gateway != nil && d.Endpoint != "" {
			if err := uc.gateway.RegisterRoute(ctx, d.Name, d.ModelID, d.Endpoint, d.TenantID, d.ID); err != nil {
				uc.failDeployment(id, "注册网关路由失败: "+err.Error())
			}
		}
	case "ReplicaFailure":
		diag := res.Message
		if diag == "" {
			diag = "Pod 运行失败"
		}
		uc.failDeployment(id, diag)
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
