package biz

import (
	"context"
	"kk-infra/lib/errcode"
)

func (uc *DeploymentUseCase) OrphanCleanupState(id string) (string, bool, error) {
	return uc.repo.OrphanCleanupState(id)
}

// CleanupOrphan explicitly deletes a ledger-backed former placement. Its pin
// blocks rebuild until all remote requests and reservation release finish.
func (uc *DeploymentUseCase) CleanupOrphan(ctx context.Context, id, clusterID, actor string, acknowledge bool) error {
	if !acknowledge || clusterID == "" {
		return errcode.New(errcode.ErrBadRequest, "clusterId 和 acknowledgeDelete=true 必填")
	}
	d, err := uc.repo.Get(id)
	if err != nil {
		return errcode.New(errcode.ErrNotFound, "部署不存在")
	}
	if clusterID == d.ClusterID {
		return errcode.New(errcode.ErrIllegalState, "不能清理当前部署集群")
	}
	if uc.clusterSelector == nil {
		return errcode.New(errcode.ErrIllegalState, "集群服务未配置")
	}
	managed, ok := uc.clusterKube.(interface {
		DeleteManagedDeploymentForCluster(context.Context, string, string, string, string) error
	})
	if !ok {
		return errcode.New(errcode.ErrIllegalState, "adapter 不支持身份校验清理")
	}
	c, err := uc.clusterSelector.Get(clusterID)
	if err != nil {
		return errcode.Wrap(errcode.ErrNotFound, "查询旧集群失败", err)
	}
	reserved := false
	for _, item := range c.GPUReservations {
		if item.DeploymentID != id {
			continue
		}
		if item.TenantID != d.TenantID || item.Namespace != d.Namespace {
			return errcode.New(errcode.ErrIllegalState, "预留身份不一致，拒绝清理")
		}
		reserved = true
	}
	pinned, running, err := uc.repo.OrphanCleanupState(id)
	if err != nil {
		return errcode.Wrap(errcode.ErrInternal, "查询清理认领失败", err)
	}
	if running || (pinned != "" && pinned != clusterID) {
		return errcode.New(errcode.ErrIllegalState, "已有清理认领，禁止并发执行")
	}
	if !reserved && pinned == "" {
		return nil
	}
	if err := uc.repo.BeginOrphanCleanup(id, clusterID, d.Generation); err != nil {
		return errcode.Wrap(errcode.ErrIllegalState, "认领清理失败，请刷新部署状态", err)
	}
	if uc.audit != nil {
		uc.audit.Record("deployment.orphan.cleanup.claimed", actor, d.TenantID, id, getRequestID(ctx), "认领旧集群 "+clusterID+" 清理")
	}
	if err := managed.DeleteManagedDeploymentForCluster(ctx, clusterID, d.Name, d.Namespace, d.ID); err != nil {
		if endErr := uc.repo.EndOrphanCleanup(id, clusterID, false); endErr != nil {
			return errcode.Wrap(errcode.ErrInternal, "清理失败且认领状态未恢复，保留锁定", endErr)
		}
		if uc.audit != nil {
			uc.audit.Record("deployment.orphan.cleanup.failed", actor, d.TenantID, id, getRequestID(ctx), "旧集群 "+clusterID+" 删除未确认；保留预留和放置阻止")
		}
		return errcode.Wrap(errcode.ErrUpstream, "旧资源删除未确认，可重试清理；禁止重建", err)
	}
	if err := uc.clusterSelector.ReleaseCapacity(clusterID, id); err != nil {
		if endErr := uc.repo.EndOrphanCleanup(id, clusterID, false); endErr != nil {
			return errcode.Wrap(errcode.ErrInternal, "释放失败且认领状态未恢复，保留锁定", endErr)
		}
		return errcode.Wrap(errcode.ErrInternal, "释放旧集群预留失败，可重试清理", err)
	}
	if err := uc.repo.EndOrphanCleanup(id, clusterID, true); err != nil {
		return errcode.Wrap(errcode.ErrInternal, "资源已清理但认领未解除，保留锁定", err)
	}
	if uc.audit != nil {
		uc.audit.Record("deployment.orphan.cleanup.completed", actor, d.TenantID, id, getRequestID(ctx), "旧集群 "+clusterID+" 资源与预留清理完成；不释放当前部署租户配额")
	}
	return nil
}
