package biz

import (
	"log/slog"
	"time"

	"kk-infra/services/controlplane/internal/data"
)

// AuditUseCase 审计日志业务（R2-4）
type AuditUseCase struct {
	store  data.AuditStore
	logger *slog.Logger
}

// NewAuditUseCase 创建审计用例
func NewAuditUseCase(store data.AuditStore, logger *slog.Logger) *AuditUseCase {
	return &AuditUseCase{store: store, logger: logger}
}

// Record 记录审计日志（异步写，失败仅记日志不影响主流程）
func (uc *AuditUseCase) Record(action, actor, tenantID, resource, requestID, detail string) {
	e := &data.AuditEntry{
		Action:    action,
		Actor:     actor,
		TenantID:  tenantID,
		Resource:  resource,
		RequestID: requestID,
		Detail:    detail,
		CreatedAt: time.Now(),
	}
	if err := uc.store.Write(e); err != nil {
		uc.logger.Warn("审计日志写入失败", "action", action, "err", err)
	}
}

// List 查询审计日志
func (uc *AuditUseCase) List(limit int) ([]data.AuditEntry, error) {
	return uc.store.List(limit)
}

func (uc *AuditUseCase) ListTenant(tenantID string, limit int) ([]data.AuditEntry, error) {
	all, err := uc.store.List(limit)
	if err != nil {
		return nil, err
	}
	result := make([]data.AuditEntry, 0)
	for _, entry := range all {
		if entry.TenantID == tenantID {
			result = append(result, entry)
		}
	}
	return result, nil
}
