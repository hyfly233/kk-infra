// Package worker 控制面后台任务：部署状态对账（Reconciler）。
package worker

import (
	"context"
	"log/slog"
	"time"

	"kk-infra/lib/domain"
	"kk-infra/services/controlplane/internal/biz"
	"kk-infra/services/controlplane/internal/data"
)

// Reconciler 对账器：周期性把控制面部署状态与 Kubernetes 实际状态对齐。
// 满足架构要求：Reconcile 幂等、控制面重启后恢复、失败诊断。
type Reconciler struct {
	repo      data.DeploymentRepository
	deployUse *biz.DeploymentUseCase
	logger    *slog.Logger
	interval  time.Duration
	// 部署启动超时：STARTING 状态超过该时长未收敛 → FAILED（R2-5）
	startTimeout time.Duration
	// 删除超时：DELETING 状态超过该时长仍未删除 → 强制失败/重试（R2-5）
	deleteTimeout time.Duration
	orphans       map[string]bool
}

// NewReconciler 创建对账器
func NewReconciler(repo data.DeploymentRepository, deployUse *biz.DeploymentUseCase, logger *slog.Logger, interval time.Duration) *Reconciler {
	return &Reconciler{
		repo:          repo,
		deployUse:     deployUse,
		logger:        logger,
		interval:      interval,
		startTimeout:  5 * time.Minute, // STARTING 5 分钟未就绪判失败
		deleteTimeout: 3 * time.Minute, // DELETING 3 分钟未完成则重试
		orphans:       map[string]bool{},
	}
}

// SetTimeouts 自定义超时（测试用）
func (r *Reconciler) SetTimeouts(start, delete_ time.Duration) {
	r.startTimeout = start
	r.deleteTimeout = delete_
}

// Run 启动对账循环（阻塞，直到 ctx 取消）
func (r *Reconciler) Run(ctx context.Context) {
	r.logger.Info("Reconciler 启动", "interval", r.interval.String())
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// 启动时立即对账一次（控制面重启恢复 + 孤儿检测）
	r.reconcileAll(ctx)
	r.reconcileOrphans(ctx)

	for {
		select {
		case <-ctx.Done():
			r.logger.Info("Reconciler 停止")
			return
		case <-ticker.C:
			r.reconcileAll(ctx)
			r.reconcileOrphans(ctx)
		}
	}
}

// reconcileOrphans 孤儿资源检测（R2-2）：
// K8s 中存在 `carrot.ai/managed-by=carrot` 标签的 Deployment，
// 但本地 repo 无对应记录 → 记录告警（不自动删除，避免误删用户资源）。
func (r *Reconciler) reconcileOrphans(ctx context.Context) {
	local := map[string]bool{}
	deploys, err := r.repo.List("")
	if err != nil {
		r.logger.Warn("孤儿检测：读取部署记录失败", "err", err)
		return
	}
	for _, d := range deploys {
		if d.Status != domain.DeploymentStatusDeleted {
			local[d.ClusterID+"/"+d.Namespace+"/"+d.Name] = true
		}
	}
	remote, err := r.deployUse.ListK8sDeployments(ctx, "*")
	if err != nil {
		r.logger.Warn("孤儿检测：扫描 K8s 部署失败", "err", err)
		return
	}
	found := map[string]bool{}
	for identity := range remote {
		if !local[identity] {
			found[identity] = true
			if !r.orphans[identity] {
				r.logger.Warn("检测到孤儿 K8s 部署（本地无记录）", "resource", identity)
				r.deployUse.RecordOrphan(identity)
			}
		}
	}
	r.orphans = found
}

// reconcileAll 对账所有活跃部署
func (r *Reconciler) reconcileAll(ctx context.Context) {
	deploys, err := r.repo.List("")
	if err != nil {
		r.logger.Error("对账：列出部署失败", "err", err)
		return
	}
	now := time.Now()
	for _, d := range deploys {
		// 终态（DELETED）跳过
		if d.Status == domain.DeploymentStatusDeleted {
			continue
		}
		if d.Status == domain.DeploymentStatusScaling && now.Sub(d.UpdatedAt) > r.startTimeout {
			r.deployUse.FailDeployment(d.ID, "扩缩容操作超时或控制面中断；保留容量预留，请核实实际副本后重试或删除")
			continue
		}
		// R2-5：STARTING 超时 → FAILED
		if d.Status == domain.DeploymentStatusStarting && now.Sub(d.UpdatedAt) > r.startTimeout {
			r.logger.Warn("部署启动超时，置为 FAILED",
				"deployment", d.Name, "timeout", r.startTimeout.String())
			r.deployUse.FailDeployment(d.ID, "启动超时（"+r.startTimeout.String()+"）未就绪")
			continue
		}
		// R2-5：DELETING 超时 → 重试删除
		if d.Status == domain.DeploymentStatusDeleting && now.Sub(d.UpdatedAt) > r.deleteTimeout {
			r.logger.Warn("删除超时，重试删除", "deployment", d.Name)
			r.deployUse.RetryDelete(ctx, d.ID)
			continue
		}
		// RUNNING 也要检查集群心跳和入口变化。
		r.reconcileOne(ctx, d)
	}
}

// reconcileOne 对账单个部署
func (r *Reconciler) reconcileOne(ctx context.Context, d *domain.ModelDeployment) {
	// SyncFromK8s 内部判断状态是否可转换
	r.deployUse.SyncFromK8s(ctx, d.ID)
}
