// 与后端 lib/apitypes + lib/domain 对齐的 TS 类型定义

// ---- 通用响应 ----

export interface ApiResponse<T = unknown> {
  code: number
  message: string
  requestId: string
  data?: T
}

// ---- 模型 ----

export interface Model {
  tenantId: string
  id: string
  name: string
  description: string
  createdAt: string
  updatedAt: string
}

export type ModelVersionStatus = 'REGISTERED' | 'VALIDATING' | 'VALIDATED' | 'RELEASED' | 'UNAVAILABLE'

export interface ModelVersion {
  tenantId: string
  id: string
  modelId: string
  modelName?: string
  version: string
  artifactUri: string
  runtime: string
  gpuType: string
  gpuCount: number
  memoryMB: number
  contextLength: number
  status: ModelVersionStatus
  createdAt: string
  updatedAt: string
}

// ---- 资源 ----

export interface GPUResource {
  nodeName: string
  gpuType: string
  total: number
  allocatable: number
  used: number
  /** 可用数 = allocatable - used（后端序列化计算属性） */
  available?: number
  memoryMB: number
  utilization: number
  health: string
}

export interface GPUTypeSummary {
  gpuType: string
  total: number
  available: number
  used: number
  memoryMB: number
  utilization: number
}

export interface GPUSummary {
  totalGpu: number
  availableGpu: number
  usedGpu: number
  errorGpu: number
  nodeCount: number
  byType: Record<string, GPUTypeSummary>
}

export interface GPUResourcesView {
  summary: GPUSummary
  nodes: GPUResource[]
}

// ---- 部署 ----

export type DeploymentStatus =
  | 'NEW' | 'VALIDATING' | 'SUBMITTING' | 'STARTING'
  | 'RUNNING' | 'SCALING' | 'RESTARTING' | 'DELETING' | 'DELETED' | 'FAILED'

export interface Resource {
  memoryMB: number
  vcores: number
  gpuCount: number
  gpuType: string
  gpuMemoryMB: number
}

export interface StatusEvent {
  deploymentId: string
  from: string
  to: string
  reason: string
  requestId: string
  diagnostics?: string
  at: string
}

export interface Deployment {
  id: string
  name: string
  modelId: string
  modelVersionId: string
  modelName: string
  modelVersion: string
  tenantId: string
  namespace: string
  replicas: number
  resource: Resource
  runtime: string
  startupArgs: string[]
  endpoint: string
  status: DeploymentStatus
  generation: number
  diagnostics?: string
  createdAt: string
  updatedAt: string
}

export interface EventView {
  type: string
  reason: string
  message: string
  at: string
}

export interface PodStatusView {
  ready: number
  desired: number
  available: number
}

export interface DeploymentView extends Deployment {
  podStatus?: PodStatusView
  events?: EventView[]
}

// ---- API Key ----

export interface APIKey {
  id: string
  keyHash: string
  tenantId: string
  /** 模型白名单（空 = 全部） */
  models?: string[]
  createdAt: string
  lastUsedAt?: string
  disabled: boolean
}

export interface IssueKeyResult {
  key: string
  keyId: string
  tenant: string
}

// ---- 指标 ----

export interface MetricPoint {
  ts: number
  val: number
}

export interface MetricSeries {
  name: string
  points: MetricPoint[]
}

export interface MetricsView {
  deploymentId: string
  range: string
  series: MetricSeries[]
}

export interface DailyUsage {
	date: string
	tenantId: string
	deploymentId: string
	inputTokens: number
	outputTokens: number
	requestCount: number
	failedCount: number
	gpuReplicaSeconds: number
	estimatedCost: number
}
