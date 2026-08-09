<script setup lang="ts">
// 服务详情：状态/指标/事件/API 示例 + 扩容/重启/删除（管理员）
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { deleteDeployment, fetchDeployment, fetchDeploymentMetrics, restartDeployment, scaleDeployment } from '../api'
import { useAuth } from '../composables/useAuth'
import type { DeploymentView, MetricsView } from '../types'
import { fmtTime, statusBadge } from '../utils/status'

const route = useRoute()
const router = useRouter()
const { isAdmin } = useAuth()

const id = route.params.id as string
const loading = ref(true)
const error = ref('')
const dep = ref<DeploymentView | null>(null)
const metrics = ref<MetricsView | null>(null)
const actionError = ref('')

// 扩容弹窗
const showScale = ref(false)
const scaleReplicas = ref(1)
const scaling = ref(false)

const apiExample = computed(() => {
  if (!dep.value) return ''
  const model = dep.value.name
  return `curl https://api.example.com/v1/chat/completions \\
  -H "Authorization: Bearer sk-carrot-***" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"${model}","messages":[{"role":"user","content":"你好"}]}'`
})

// 服务详情 Tab（对齐 INTERACTION-PROTOTYPE-V2 §3）
const tabs = [
  { id: 'perf', label: '性能' },
  { id: 'gpu', label: 'GPU' },
  { id: 'logs', label: '日志' },
  { id: 'events', label: '事件' },
  { id: 'config', label: '配置' },
  { id: 'api', label: 'API 示例' },
]
const activeTab = ref('perf')
interface PerfMetric { name: string; value: string }
const perfMetrics = computed<PerfMetric[]>(() => {
  const series = metrics.value?.series ?? []
  const latest = (name: string) => series.find((item) => item.name === name)?.points.at(-1)?.val
  const requests = latest('requests')
  const errorRate = latest('errorRate')
  const ttft = latest('ttftMs')
  const tokensPerSec = latest('tokensPerSec')
  return [
    requests === undefined ? null : { name: '请求数', value: requests.toFixed(0) },
    errorRate === undefined ? null : { name: '错误率', value: `${errorRate.toFixed(2)}%` },
    ttft === undefined ? null : { name: 'TTFT', value: `${ttft.toFixed(0)} ms` },
    tokensPerSec === undefined ? null : { name: 'Token/s', value: tokensPerSec.toFixed(2) },
  ].filter((item): item is PerfMetric => item !== null)
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    dep.value = await fetchDeployment(id)
    try {
      metrics.value = await fetchDeploymentMetrics(id, '1h')
    } catch {
      metrics.value = null
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

function openScale() {
  if (dep.value) {
    scaleReplicas.value = dep.value.replicas
    showScale.value = true
  }
}

async function doScale() {
  actionError.value = ''
  scaling.value = true
  try {
    dep.value = await scaleDeployment(id, scaleReplicas.value)
    showScale.value = false
  } catch (e) {
    actionError.value = (e as Error).message
  } finally {
    scaling.value = false
  }
}

async function doRestart() {
  actionError.value = ''
  if (!confirm(`确认重启服务「${dep.value?.name}」？`)) return
  try {
    dep.value = await restartDeployment(id)
  } catch (e) {
    actionError.value = (e as Error).message
  }
}

async function doDelete() {
  const d = dep.value
  if (!d) return
  const gpuCount = d.resource.gpuCount * d.replicas
  if (!confirm(`确认删除服务「${d.name}」？将释放 ${gpuCount} 张 GPU，此操作不可恢复。`)) return
  actionError.value = ''
  try {
    await deleteDeployment(id)
    router.push('/deployments')
  } catch (e) {
    actionError.value = (e as Error).message
  }
}

const canManage = computed(() => isAdmin.value && dep.value && !['DELETED', 'DELETING'].includes(dep.value.status))

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <button class="ghost mb-16" @click="router.push('/deployments')">← 返回服务列表</button>
        <h2>{{ dep?.name ?? '...' }}</h2>
      </div>
      <div v-if="canManage" class="flex">
        <button class="ghost" @click="openScale">扩容</button>
        <button class="ghost" @click="doRestart">重启</button>
        <button class="danger" @click="doDelete">删除</button>
      </div>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="actionError" class="error-box">{{ actionError }}</div>
    <div v-if="loading" class="empty">加载中...</div>

    <template v-else-if="dep">
      <!-- 基本信息 -->
      <div class="panel mb-16">
        <div class="flex-between">
          <div class="flex">
            <span class="badge" :class="statusBadge(dep.status)">{{ dep.status }}</span>
            <span class="dim">Endpoint: <span class="mono">{{ dep.endpoint || '-' }}</span></span>
          </div>
        </div>
        <div class="info-grid mt-16">
          <div><label>模型</label><strong>{{ dep.modelName }}:{{ dep.modelVersion }}</strong></div>
          <div><label>运行时</label><strong>{{ dep.runtime }}</strong></div>
          <div><label>副本数</label><strong>{{ dep.replicas }}</strong></div>
          <div><label>就绪</label>
            <strong>
              {{ dep.podStatus ? dep.podStatus.ready + '/' + dep.podStatus.desired : '-' }}
            </strong>
          </div>
          <div><label>GPU</label><strong>{{ dep.resource.gpuType }} × {{ dep.resource.gpuCount }}</strong></div>
          <div><label>内存</label><strong>{{ (dep.resource.memoryMB / 1024).toFixed(0) }} GB</strong></div>
          <div><label>租户</label><strong>{{ dep.tenantId }}</strong></div>
          <div><label>命名空间</label><strong class="mono">{{ dep.namespace }}</strong></div>
          <div><label>创建时间</label><strong>{{ fmtTime(dep.createdAt) }}</strong></div>
        </div>
        <div v-if="dep.diagnostics" class="error-box mt-16">诊断：{{ dep.diagnostics }}</div>
      </div>

      <!-- Tab 导航：性能/GPU/日志/事件/配置/API 示例 -->
      <div class="panel">
        <div class="tabs">
          <button
            v-for="t in tabs"
            :key="t.id"
            class="tab"
            :class="{ active: activeTab === t.id }"
            @click="activeTab = t.id"
          >{{ t.label }}</button>
        </div>

        <!-- 性能 -->
        <div v-if="activeTab === 'perf'" class="tab-content">
          <div v-if="!perfMetrics.length" class="empty">暂无性能指标，调用 API 后展示</div>
          <div v-else class="perf-grid">
            <div v-for="m in perfMetrics" :key="m.name" class="perf-card">
              <div class="perf-label">{{ m.name }}</div>
              <div class="perf-value">{{ m.value }}</div>
            </div>
          </div>
        </div>

        <!-- GPU -->
        <div v-if="activeTab === 'gpu'" class="tab-content">
          <div class="empty">GPU 指标通过 observability 查询（见 GPU 资源页）</div>
        </div>

        <!-- 日志 -->
        <div v-if="activeTab === 'logs'" class="tab-content">
          <div class="empty">日志对接 K8s 日志（后续轮接入 Loki/EFK）</div>
        </div>

        <!-- 事件 -->
        <div v-if="activeTab === 'events'" class="tab-content">
          <div v-if="!dep.events?.length" class="empty">暂无事件</div>
          <table v-else>
            <thead>
              <tr><th>时间</th><th>变更</th><th>原因</th></tr>
            </thead>
            <tbody>
              <tr v-for="(e, i) in dep.events" :key="i">
                <td class="dim">{{ fmtTime(e.at) }}</td>
                <td>
                  <span v-if="e.reason" class="badge blue">{{ e.reason }}</span>
                </td>
                <td>{{ e.message }}</td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 配置 -->
        <div v-if="activeTab === 'config'" class="tab-content">
          <div class="config-grid">
            <div><label>服务名称</label><strong>{{ dep.name }}</strong></div>
            <div><label>模型版本</label><strong>{{ dep.modelName }}:{{ dep.modelVersion }}</strong></div>
            <div><label>运行时</label><strong>{{ dep.runtime }}</strong></div>
            <div><label>副本数</label><strong>{{ dep.replicas }}</strong></div>
            <div><label>GPU</label><strong>{{ dep.resource.gpuType }} × {{ dep.resource.gpuCount }}</strong></div>
            <div><label>内存</label><strong>{{ (dep.resource.memoryMB / 1024).toFixed(0) }} GB</strong></div>
            <div><label>租户</label><strong>{{ dep.tenantId }}</strong></div>
            <div><label>命名空间</label><strong class="mono">{{ dep.namespace }}</strong></div>
            <div><label>启动参数</label><strong class="mono">{{ dep.startupArgs?.length ? dep.startupArgs.join(' ') : '-' }}</strong></div>
            <div><label>代数</label><strong>{{ dep.generation }}</strong></div>
          </div>
        </div>

        <!-- API 示例 -->
        <div v-if="activeTab === 'api'" class="tab-content">
          <pre>{{ apiExample }}</pre>
        </div>
      </div>
    </template>

    <!-- 扩容弹窗 -->
    <div v-if="showScale" class="modal-mask" @click.self="showScale = false">
      <div class="modal">
        <h3>扩缩容</h3>
        <div class="form-row">
          <label>副本数（当前 {{ dep?.replicas }}）</label>
          <input v-model.number="scaleReplicas" type="number" min="1" />
        </div>
        <div v-if="dep" class="dim">
          变更后 GPU 占用：{{ dep.resource.gpuCount * scaleReplicas }} 张
        </div>
        <div class="flex" style="justify-content: flex-end; margin-top: 16px">
          <button @click="showScale = false">取消</button>
          <button class="primary" :disabled="scaling" @click="doScale">
            {{ scaling ? '提交中...' : '确认' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dim {
  color: var(--text-dim);
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.info-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 16px;
}
.info-grid label {
  margin-bottom: 4px;
}
.tabs {
  display: flex;
  gap: 4px;
  border-bottom: 1px solid var(--border);
  margin-bottom: 16px;
}
.tab {
  background: transparent;
  border: none;
  border-bottom: 2px solid transparent;
  border-radius: 0;
  padding: 10px 16px;
  color: var(--text-dim);
  font-size: 13px;
}
.tab:hover {
  color: var(--text);
}
.tab.active {
  color: var(--primary);
  border-bottom-color: var(--primary);
  font-weight: 600;
}
.tab-content {
  padding: 8px 4px;
}
.perf-grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 12px;
}
.perf-card {
  background: var(--bg);
  border-radius: 8px;
  padding: 14px;
  text-align: center;
}
.perf-label {
  color: var(--text-dim);
  font-size: 12px;
  margin-bottom: 6px;
}
.perf-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--primary);
}
.config-grid {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 14px;
}
.config-grid label {
  margin-bottom: 4px;
}
</style>
