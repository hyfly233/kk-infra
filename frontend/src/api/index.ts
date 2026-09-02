// 后端 API 封装：统一错误处理与响应解包
import type {
  ApiResponse,
  APIKey,
  Deployment,
  DeploymentView,
	DailyUsage,
  GPUResourcesView,
  IssueKeyResult,
  MetricsView,
  Model,
  ModelVersion,
} from '../types'

// ApiError 业务错误（含 HTTP 状态码）
export class ApiError extends Error {
  code: number
  status: number
  requestId: string

  constructor(message: string, code: number, status: number, requestId: string) {
    super(message)
    this.code = code
    this.status = status
    this.requestId = requestId
  }
}

// request 通用请求封装
async function request<T>(url: string, options: RequestInit = {}): Promise<T> {
  const resp = await fetch(url, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  })
  let body: ApiResponse<T>
  try {
    body = await resp.json()
  } catch {
    throw new ApiError(`响应解析失败: HTTP ${resp.status}`, 1000, resp.status, '')
  }
  if (body.code !== 0) {
    throw new ApiError(body.message || '请求失败', body.code, resp.status, body.requestId)
  }
  return body.data as T
}

// ---- 控制面 API（controlplane :8080，代理 /api） ----

// GPU 资源
export function fetchGPUs(gpuType?: string): Promise<GPUResourcesView> {
  const q = gpuType ? `?gpuType=${encodeURIComponent(gpuType)}` : ''
  return request(`/api/v1/resources/gpus${q}`)
}

// 部署列表
export function fetchDeployments(): Promise<Deployment[]> {
  return request('/api/v1/deployments')
}

// 部署详情
export function fetchDeployment(id: string): Promise<DeploymentView> {
  return request(`/api/v1/deployments/${id}`)
}

// 创建部署
export function createDeployment(body: {
  idempotencyKey: string
  name: string
  modelVersionId: string
  tenantId?: string
  namespace?: string
  replicas?: number
  startupArgs?: string[]
}): Promise<Deployment> {
  return request('/api/v1/deployments', { method: 'POST', body: JSON.stringify(body) })
}

// 扩缩容
export function scaleDeployment(id: string, replicas: number): Promise<Deployment> {
  return request(`/api/v1/deployments/${id}/scale`, {
    method: 'POST',
    body: JSON.stringify({ replicas }),
  })
}

// 重启
export function restartDeployment(id: string): Promise<Deployment> {
  return request(`/api/v1/deployments/${id}/restart`, { method: 'POST' })
}

// 删除部署
export function deleteDeployment(id: string): Promise<{ deleted: boolean }> {
  return request(`/api/v1/deployments/${id}`, { method: 'DELETE' })
}

// 部署指标
export function fetchDeploymentMetrics(id: string, range = '1h'): Promise<MetricsView> {
  return request(`/api/v1/deployments/${id}/metrics?range=${range}`)
}

// ---- 模型注册中心（modelregistry :8081，代理 /model-registry） ----

// 模型列表
export function fetchModels(): Promise<Model[]> {
  return request('/model-registry/v1/models')
}

// 模型详情
export function fetchModel(id: string): Promise<Model> {
  return request(`/model-registry/v1/models/${id}`)
}

// 注册模型
export function createModel(body: { name: string; description?: string }): Promise<Model> {
  return request('/model-registry/v1/models', { method: 'POST', body: JSON.stringify(body) })
}

// 删除模型
export function deleteModel(id: string): Promise<{ deleted: boolean }> {
  return request(`/model-registry/v1/models/${id}`, { method: 'DELETE' })
}

// 模型版本列表
export function fetchVersions(modelId: string): Promise<ModelVersion[]> {
  return request(`/model-registry/v1/models/${modelId}/versions`)
}

// 创建模型版本
export function createVersion(modelId: string, body: {
  version: string
  artifactUri: string
  runtime?: string
  gpuType: string
  gpuCount: number
  memoryMB: number
  contextLength?: number
  startupArgs?: string[]
}): Promise<ModelVersion> {
  return request(`/model-registry/v1/models/${modelId}/versions`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

// 校验版本（REGISTERED → VALIDATED）
export function validateVersion(versionId: string): Promise<ModelVersion> {
  return request(`/model-registry/v1/versions/${versionId}/validate`, { method: 'POST' })
}

// 发布版本（VALIDATED → RELEASED，发布后才可部署）
export function releaseVersion(versionId: string): Promise<ModelVersion> {
  return request(`/model-registry/v1/versions/${versionId}/release`, { method: 'POST' })
}

// 删除版本
export function deleteVersion(modelId: string, version: string): Promise<{ deleted: boolean }> {
  return request(`/model-registry/v1/models/${modelId}/versions/${version}`, { method: 'DELETE' })
}

// ---- 网关（gateway :8083，代理 /gateway） ----

// API Key 列表
export function fetchKeys(): Promise<APIKey[]> {
  return request('/gateway/v1/keys')
}

// 创建 API Key（明文仅返回一次）
export function issueKey(tenantId = 'default'): Promise<IssueKeyResult> {
  return request('/gateway/v1/keys', { method: 'POST', body: JSON.stringify({ tenantId }) })
}

// 禁用 API Key
export function disableKey(keyId: string): Promise<{ disabled: boolean }> {
  return request(`/gateway/v1/keys/${keyId}/disable`, { method: 'POST' })
}

// 轮换 API Key
export function rotateKey(keyId: string, tenantId = 'default'): Promise<IssueKeyResult> {
  return request(`/gateway/v1/keys/${keyId}/rotate?tenant=${tenantId}`, { method: 'POST' })
}

// 设置 Key 模型白名单
export function setKeyModels(keyId: string, models: string[]): Promise<{ updated: boolean }> {
  return request(`/gateway/v1/keys/${keyId}/models`, { method: 'POST', body: JSON.stringify({ models }) })
}

// ---- 租户配额（controlplane） ----

export interface TenantQuota {
  tenantId: string
  gpuType: string
  quota: number
  used: number
}

export function fetchQuotas(): Promise<TenantQuota[]> {
  return request('/api/v1/quotas')
}

export function setQuota(tenantId: string, gpuType: string, quota: number): Promise<TenantQuota> {
  return request(`/api/v1/quotas/${tenantId}`, {
    method: 'PUT',
    body: JSON.stringify({ gpuType, quota }),
  })
}

// ---- 审计日志（controlplane） ----

export interface AuditEntry {
  id: number
  action: string
  actor: string
  tenantId: string
  resource: string
  requestId: string
  detail: string
  createdAt: string
}

export function fetchAudit(limit = 50): Promise<AuditEntry[]> {
  return request(`/api/v1/audit?limit=${limit}`)
}

export function fetchBilling(tenantId: string, from: string, to: string): Promise<DailyUsage[]> {
	const q = new URLSearchParams({ tenantId })
	if (from) q.set('from', from)
	if (to) q.set('to', to)
	return request(`/api/v1/billing?${q}`)
}

export function billingCSVURL(tenantId: string, from: string, to: string): string {
	const q = new URLSearchParams({ tenantId })
	if (from) q.set('from', from)
	if (to) q.set('to', to)
	return `/api/v1/billing.csv?${q}`
}

export function setRateCard(tenantId: string, body: { gpuType: string; inputTokenPerMillion: number; outputTokenPerMillion: number; gpuHour: number }): Promise<unknown> {
	return request(`/api/v1/rate-cards/${encodeURIComponent(tenantId)}`, { method: 'PUT', body: JSON.stringify(body) })
}
