// Package biz 模型注册中心业务逻辑
package biz

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"kk-infra/lib/apitypes"
	"kk-infra/lib/domain"
	"kk-infra/lib/errcode"
	"kk-infra/services/modelregistry/internal/artifact"
	"kk-infra/services/modelregistry/internal/data"
)

// Registry 模型注册业务
type Registry struct {
	repo      data.Repository
	artifacts artifact.Verifier
}

// NewRegistry 创建业务对象
func NewRegistry(repo data.Repository, verifiers ...artifact.Verifier) *Registry {
	verifier := artifact.Verifier(artifact.DevelopmentVerifier{})
	if len(verifiers) > 0 && verifiers[0] != nil {
		verifier = verifiers[0]
	}
	return &Registry{repo: repo, artifacts: verifier}
}

// newID 生成全局唯一 ID（crypto/rand，不依赖外部库）
func newID(prefix string) string {
	b := make([]byte, 10)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

// CreateModel 注册模型
func (r *Registry) CreateModel(req *apitypes.CreateModelRequest) (*domain.Model, error) {
	if req.Name == "" {
		return nil, errcode.New(errcode.ErrBadRequest, "模型名称必填")
	}
	now := time.Now()
	m := &domain.Model{
		ID:          newID("m"),
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := r.repo.CreateModel(m); err != nil {
		if err == data.ErrConflict {
			return nil, errcode.New(errcode.ErrConflict, "模型名称已存在: "+req.Name)
		}
		return nil, data.ToErrCode(err)
	}
	return m, nil
}

// GetModel 查询模型
func (r *Registry) GetModel(id string) (*domain.Model, error) {
	m, err := r.repo.GetModel(id)
	if err != nil {
		return nil, data.ToErrCode(err)
	}
	return m, nil
}

// ListModels 模型列表
func (r *Registry) ListModels() ([]*domain.Model, error) {
	return r.repo.ListModels()
}

// DeleteModel 删除模型（级联删除版本）
func (r *Registry) DeleteModel(id string) error {
	versions, err := r.repo.ListVersions(id)
	if err != nil {
		return data.ToErrCode(err)
	}
	for _, version := range versions {
		if err := r.ensureVersionDeletable(version); err != nil {
			return err
		}
	}
	if err := r.repo.DeleteModel(id); err != nil {
		return data.ToErrCode(err)
	}
	return nil
}

// CreateVersion 注册模型版本
func (r *Registry) CreateVersion(modelID string, req *apitypes.CreateModelVersionRequest) (*domain.ModelVersion, error) {
	if _, err := r.repo.GetModel(modelID); err != nil {
		return nil, errcode.New(errcode.ErrModelNotFound, "模型不存在: "+modelID)
	}
	if req.Version == "" || req.ArtifactURI == "" || req.GPUType == "" || req.GPUCount <= 0 {
		return nil, errcode.New(errcode.ErrBadRequest, "版本号/权重地址/GPU 类型/GPU 数量必填")
	}
	if _, _, err := artifact.ParseS3URI(req.ArtifactURI); err != nil {
		return nil, errcode.New(errcode.ErrBadRequest, err.Error())
	}
	runtime := req.Runtime
	if runtime == "" {
		runtime = domain.RuntimeVLLM
	}
	if !domain.IsSupportedRuntime(runtime) {
		return nil, errcode.New(errcode.ErrBadRequest, "不支持的运行时: "+runtime)
	}
	now := time.Now()
	v := &domain.ModelVersion{
		ID:             newID("v"),
		ModelID:        modelID,
		Version:        req.Version,
		ArtifactURI:    req.ArtifactURI,
		ArtifactDigest: req.ArtifactDigest,
		StorageType:    "s3",
		Runtime:        runtime,
		GPUType:        req.GPUType,
		GPUCount:       req.GPUCount,
		MemoryMB:       req.MemoryMB,
		ContextLength:  req.ContextLength,
		Status:         domain.ModelStatusRegistered,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := r.repo.CreateVersion(v); err != nil {
		if err == data.ErrConflict {
			return nil, errcode.New(errcode.ErrConflict, "模型版本已存在: "+req.Version)
		}
		return nil, data.ToErrCode(err)
	}
	return v, nil
}

// GetVersion 按版本 ID 查询（供控制面部署校验）
func (r *Registry) GetVersion(versionID string) (*domain.ModelVersion, error) {
	v, err := r.repo.GetVersion(versionID)
	if err != nil {
		return nil, data.ToErrCode(err)
	}
	// 填充冗余模型名
	if m, err := r.repo.GetModel(v.ModelID); err == nil {
		v.ModelName = m.Name
	}
	return v, nil
}

// ListVersions 模型版本列表
func (r *Registry) ListVersions(modelID string) ([]*domain.ModelVersion, error) {
	if _, err := r.repo.GetModel(modelID); err != nil {
		return nil, errcode.New(errcode.ErrModelNotFound, "模型不存在: "+modelID)
	}
	return r.repo.ListVersions(modelID)
}

// ValidateVersion 标记版本校验通过（REGISTERED → VALIDATED）
func (r *Registry) ValidateVersion(ctx context.Context, versionID string) (*domain.ModelVersion, error) {
	v, err := r.repo.GetVersion(versionID)
	if err != nil {
		return nil, data.ToErrCode(err)
	}
	if !domain.CanTransitionVersion(v.Status, domain.ModelStatusValidated) {
		return nil, errcode.New(errcode.ErrIllegalState,
			"版本状态为 "+v.Status+"，无法流转到 VALIDATED")
	}
	metadata, err := r.artifacts.Verify(ctx, v.ArtifactURI, v.ArtifactDigest)
	if err != nil {
		return nil, errcode.Wrap(errcode.ErrIllegalState, "artifact 校验失败", err)
	}
	if err := r.repo.UpdateArtifactMetadata(versionID, metadata.Digest, metadata.Size, metadata.VerifiedAt); err != nil {
		return nil, data.ToErrCode(err)
	}
	if err := r.repo.UpdateVersionStatus(versionID, domain.ModelStatusValidated); err != nil {
		return nil, data.ToErrCode(err)
	}
	v.Status = domain.ModelStatusValidated
	v.ArtifactDigest = metadata.Digest
	v.ArtifactSize = metadata.Size
	v.ArtifactVerifiedAt = &metadata.VerifiedAt
	return v, nil
}

// ReleaseVersion 发布版本（VALIDATED → RELEASED）。
// 发布后版本可部署；发布记录写入 audit（由调用方记录）。
func (r *Registry) ReleaseVersion(versionID string) (*domain.ModelVersion, error) {
	v, err := r.repo.GetVersion(versionID)
	if err != nil {
		return nil, data.ToErrCode(err)
	}
	if !domain.CanTransitionVersion(v.Status, domain.ModelStatusReleased) {
		return nil, errcode.New(errcode.ErrIllegalState,
			"版本状态为 "+v.Status+"，仅 VALIDATED 可发布")
	}
	if err := r.repo.UpdateVersionStatus(versionID, domain.ModelStatusReleased); err != nil {
		return nil, data.ToErrCode(err)
	}
	v.Status = domain.ModelStatusReleased
	return v, nil
}

// DeleteVersion 删除模型版本
func (r *Registry) DeleteVersion(modelID, version string) error {
	v, err := r.repo.GetVersionByModelAndVersion(modelID, version)
	if err != nil {
		return data.ToErrCode(err)
	}
	if err := r.ensureVersionDeletable(v); err != nil {
		return err
	}
	return data.ToErrCode(r.repo.DeleteVersion(v.ID))
}

func (r *Registry) ensureVersionDeletable(v *domain.ModelVersion) error {
	referenced, err := r.repo.HasVersionReferences(v.ID)
	if err != nil {
		return data.ToErrCode(err)
	}
	if referenced {
		return errcode.New(errcode.ErrIllegalState, "模型版本仍被部署引用")
	}
	if v.Status == domain.ModelStatusValidated || v.Status == domain.ModelStatusReleased {
		return errcode.New(errcode.ErrIllegalState, "已校验或发布的版本不可删除，请先下线关联服务")
	}
	return nil
}
