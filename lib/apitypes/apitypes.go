// Package apitypes 定义 REST API 请求/响应类型，供各服务复用。
package apitypes

import (
	"kk-infra/lib/domain"
	"time"
)

// 通用响应包装
type Response struct {
	Code      int         `json:"code"`      // 0 表示成功
	Message   string      `json:"message"`   // 错误信息
	RequestID string      `json:"requestId"` // 请求 ID
	Data      interface{} `json:"data,omitempty"`
}

// 分页
type Page struct {
	Total  int64 `json:"total"`
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
}

// ---- 模型 ----

type CreateModelRequest struct {
	TenantID    string `json:"tenantId,omitempty"`
	Name        string `json:"name" binding:"required"`
	Description string `json:"description"`
}

type CreateModelVersionRequest struct {
	Version        string   `json:"version" binding:"required"`
	ArtifactURI    string   `json:"artifactUri" binding:"required"` // 权重地址
	ArtifactDigest string   `json:"artifactDigest"`                 // 可选 sha256:<hex>，校验时强制比对
	Runtime        string   `json:"runtime"`                        // 默认 vLLM
	GPUType        string   `json:"gpuType" binding:"required"`
	GPUCount       int32    `json:"gpuCount" binding:"required"`
	MemoryMB       int64    `json:"memoryMB" binding:"required"`
	ContextLength  int32    `json:"contextLength"`
	StartupArgs    []string `json:"startupArgs"`
}

type ModelVersionView struct {
	domain.ModelVersion
}

type ModelWithVersions struct {
	Model    domain.Model          `json:"model"`
	Versions []domain.ModelVersion `json:"versions"`
}

// ---- 部署 ----

type CreateDeploymentRequest struct {
	// IdempotencyKey 幂等键：客户端生成，重复提交返回同一部署
	IdempotencyKey string   `json:"idempotencyKey" binding:"required"`
	Name           string   `json:"name" binding:"required"`
	ModelVersionID string   `json:"modelVersionId" binding:"required"`
	TenantID       string   `json:"tenantId"`  // 默认 default
	Namespace      string   `json:"namespace"` // 默认 tenant-<id>
	Replicas       int32    `json:"replicas"`  // 默认 1
	StartupArgs    []string `json:"startupArgs"`
	ServingMode    string   `json:"servingMode"`
}

type ScaleDeploymentRequest struct {
	Replicas int32 `json:"replicas" binding:"required"`
}

type DeploymentView struct {
	domain.ModelDeployment
	PodStatus *PodStatusView `json:"podStatus,omitempty"`
	Events    []EventView    `json:"events,omitempty"`
}

type PodStatusView struct {
	Ready     int32 `json:"ready"`
	Desired   int32 `json:"desired"`
	Available int32 `json:"available"`
}

type EventView struct {
	Type    string    `json:"type"`
	Reason  string    `json:"reason"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// ---- 资源 ----

type GPUResourcesView struct {
	Summary domain.GPUSummary    `json:"summary"`
	Nodes   []domain.GPUResource `json:"nodes"`
}

// ---- 指标 ----

type MetricsView struct {
	DeploymentID string         `json:"deploymentId"`
	Range        string         `json:"range"` // 如 1h
	Series       []MetricSeries `json:"series"`
}

type MetricSeries struct {
	Name   string        `json:"name"` // 如 requests, errorRate, ttftMs, tokensPerSec, gpuUtil
	Points []MetricPoint `json:"points"`
}

type MetricPoint struct {
	Ts  int64   `json:"ts"`
	Val float64 `json:"val"`
}
