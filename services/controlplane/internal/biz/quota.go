package biz

import (
	"context"
	"log/slog"

	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/services/controlplane/internal/data"
)

// QuotaUseCase 租户配额业务
type QuotaUseCase struct {
	store  data.QuotaStore
	logger *slog.Logger
}

// NewQuotaUseCase 创建配额用例
func NewQuotaUseCase(store data.QuotaStore, logger *slog.Logger) *QuotaUseCase {
	return &QuotaUseCase{store: store, logger: logger}
}

// List 列出全部配额
func (uc *QuotaUseCase) List() ([]*domain.TenantQuota, error) {
	return uc.store.List()
}

func (uc *QuotaUseCase) ListTenant(tenantID string) ([]*domain.TenantQuota, error) {
	all, err := uc.store.List()
	if err != nil {
		return nil, err
	}
	result := make([]*domain.TenantQuota, 0)
	for _, quota := range all {
		if quota.TenantID == tenantID {
			result = append(result, quota)
		}
	}
	return result, nil
}

// Set 设置配额上限（管理员）
func (uc *QuotaUseCase) Set(tenantID, gpuType string, quota int32) (*domain.TenantQuota, error) {
	if tenantID == "" || gpuType == "" || quota < 0 {
		return nil, errcode.New(errcode.ErrBadRequest, "租户/GPU 型号必填，配额 >= 0")
	}
	if err := uc.store.Set(tenantID, gpuType, quota); err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "设置配额失败", err)
	}
	q, err := uc.store.Get(tenantID, gpuType)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrInternal, "查询配额失败", err)
	}
	return q, nil
}

// Reserve 预留配额（创建/扩容部署时调用）。
// 返回 true 表示配额充足并已占用；false 表示配额不足。
// 失败时返回业务错误。
func (uc *QuotaUseCase) Reserve(ctx context.Context, tenantID, gpuType string, need int32) error {
	if need <= 0 {
		return nil
	}
	q, err := uc.store.Get(tenantID, gpuType)
	if err != nil {
		// 配额不存在：允许部署（默认不限制），但不占用
		if err == data.ErrQuotaNotFound {
			uc.logger.Warn("租户配额未配置，跳过配额校验",
				"tenant", tenantID, "gpuType", gpuType)
			return nil
		}
		return errcode.Wrap(errcode.ErrInternal, "查询配额失败", err)
	}
	if q.Available() < need {
		return errcode.New(errcode.ErrQuotaExceeded,
			"租户配额不足: 需要 "+itoa32(need)+" 张 "+gpuType+
				"，配额 "+itoa32(q.Quota)+"，已用 "+itoa32(q.Used))
	}
	if err := uc.store.AddUsed(tenantID, gpuType, need); err != nil {
		if err == data.ErrQuotaExceeded {
			return errcode.New(errcode.ErrQuotaExceeded, "租户配额不足（并发预留）")
		}
		return errcode.Wrap(errcode.ErrInternal, "占用配额失败", err)
	}
	return nil
}

// ReleaseDeployment uses a persistent receipt for retry-safe deletion.
func (uc *QuotaUseCase) ReleaseDeployment(id, tenantID, gpuType string, need int32) error {
	return uc.store.ReleaseDeployment(id, tenantID, gpuType, need)
}

// Release 归还创建/扩缩容中尚未提交的预留。
func (uc *QuotaUseCase) Release(ctx context.Context, tenantID, gpuType string, need int32) {
	if need <= 0 {
		return
	}
	if err := uc.store.AddUsed(tenantID, gpuType, -need); err != nil {
		uc.logger.Warn("释放配额失败", "tenant", tenantID, "gpuType", gpuType, "need", need, "err", err)
	}
}
