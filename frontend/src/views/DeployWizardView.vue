<script setup lang="ts">
// 部署服务向导：Step1 选择模型 → Step2 配置资源 → Step3 配置服务（管理员）
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { createDeployment, fetchGPUs, fetchModels, fetchVersions } from '../api'
import type { GPUResourcesView, Model, ModelVersion } from '../types'

const router = useRouter()

const step = ref(1)
const error = ref('')
const submitting = ref(false)

// Step1 数据
const models = ref<Model[]>([])
const versions = ref<ModelVersion[]>([])
const loadingModels = ref(true)
const selectedModel = ref('')
const selectedVersion = ref('')

// Step2 数据
const gpus = ref<GPUResourcesView | null>(null)
const gpuType = ref('A100')
const gpuCount = ref(1)
const replicas = ref(1)
const memoryMB = ref(32768)

// Step3 数据
const svcName = ref('')
const contextLength = ref(8192)
const startupArgs = ref('')
// 幂等键（Step4 确认页展示）
const idempotencyKey = ref(`deploy-${Date.now()}`)

// 可用 GPU 检查
const availableGPU = computed(() => {
  const s = gpus.value?.summary
  return s?.byType?.[gpuType.value]?.available ?? 0
})
const needGPU = computed(() => gpuCount.value * replicas.value)
const gpuEnough = computed(() => availableGPU.value >= needGPU.value)

const selectedVersionObj = computed(() =>
  versions.value.find((v) => v.id === selectedVersion.value),
)
const deploymentTenant = computed(() => selectedVersionObj.value?.tenantId || '')

async function loadModels() {
  loadingModels.value = true
  try {
    models.value = (await fetchModels()).filter((model) => model.tenantId)
    const g = await fetchGPUs()
    gpus.value = g
    // 默认选第一个模型
    if (models.value.length) {
      selectedModel.value = models.value[0].id
      await loadVersions()
    }
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loadingModels.value = false
  }
}

async function loadVersions() {
  versions.value = []
  selectedVersion.value = ''
  if (!selectedModel.value) return
  try {
    const vs = await fetchVersions(selectedModel.value)
    // 只显示已发布（RELEASED）版本
    versions.value = vs.filter((v) => v.status === 'RELEASED')
    if (versions.value.length) {
      selectedVersion.value = versions.value[0].id
      const v = versions.value[0]
      gpuType.value = v.gpuType
      gpuCount.value = v.gpuCount
      memoryMB.value = v.memoryMB
      contextLength.value = v.contextLength
    }
  } catch (e) {
    error.value = (e as Error).message
  }
}

function onModelChange() {
  loadVersions()
}

function next() {
  error.value = ''
  if (step.value === 1 && !selectedVersion.value) {
    error.value = '请选择模型版本（需已校验通过）'
    return
  }
  if (step.value === 2 && !gpuEnough.value) {
    error.value = `GPU 资源不足：需要 ${needGPU.value} 张 ${gpuType.value}，当前可用 ${availableGPU.value} 张`
    return
  }
  if (step.value === 3 && !svcName.value.trim()) {
    error.value = '服务名称必填'
    return
  }
  step.value++
}

function back() {
  step.value--
}

async function deploy() {
  error.value = ''
  submitting.value = true
  const args = startupArgs.value
    .split(/\s+/)
    .filter((a) => a.trim())
  try {
    const d = await createDeployment({
      idempotencyKey: idempotencyKey.value,
      name: svcName.value.trim(),
      modelVersionId: selectedVersion.value,
      tenantId: deploymentTenant.value,
      replicas: replicas.value,
      startupArgs: args,
    })
    router.push(`/deployments/${d.id}`)
  } catch (e) {
    error.value = (e as Error).message
    submitting.value = false
  }
}

onMounted(loadModels)
</script>

<template>
  <div class="page" style="max-width: 720px; margin: 0 auto">
    <div class="page-header">
      <div>
        <button class="ghost mb-16" @click="router.push('/deployments')">← 返回服务列表</button>
        <h2>部署模型服务</h2>
      </div>
    </div>

    <!-- 步骤条 -->
    <div class="steps">
      <div v-for="(t, i) in ['选择模型', '配置资源', '配置服务', '确认部署']" :key="t"
        class="step" :class="{ active: step === i + 1, done: step > i + 1 }">
        {{ i + 1 }}. {{ t }}
      </div>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="loadingModels" class="empty">加载模型列表...</div>

    <template v-else>
      <!-- Step 1: 选择模型 -->
      <div v-if="step === 1" class="panel">
        <div class="form-row">
          <label>模型</label>
          <select v-model="selectedModel" @change="onModelChange">
            <option v-for="m in models" :key="m.id" :value="m.id">{{ m.name }}</option>
          </select>
        </div>
        <div class="form-row">
          <label>模型版本（仅显示已发布的 RELEASED 版本）</label>
          <select v-model="selectedVersion">
            <option v-for="v in versions" :key="v.id" :value="v.id">
              {{ v.version }} · {{ v.runtime }} · {{ v.gpuType }} × {{ v.gpuCount }} · {{ (v.memoryMB / 1024).toFixed(0) }}GB
            </option>
          </select>
        </div>
        <div v-if="selectedVersionObj" class="version-info">
          <div>权重地址：<span class="mono">{{ selectedVersionObj.artifactUri }}</span></div>
          <div>显存需求：{{ (selectedVersionObj.memoryMB / 1024).toFixed(0) }} GB · 上下文：{{ selectedVersionObj.contextLength }}</div>
        </div>
        <div v-if="!versions.length" class="empty">
          当前模型无可部署版本，请先在模型详情中注册并校验版本
        </div>
        <div class="flex" style="justify-content: flex-end">
          <button class="primary" @click="next">下一步</button>
        </div>
      </div>

      <!-- Step 2: 配置资源 -->
      <div v-if="step === 2" class="panel">
        <div class="grid-2">
          <div class="form-row">
            <label>GPU 类型</label>
            <select v-model="gpuType">
              <option value="A100">A100</option>
              <option value="H100">H100</option>
              <option value="L40S">L40S</option>
              <option value="4090">4090</option>
            </select>
          </div>
          <div class="form-row">
            <label>GPU 数量（每副本）</label>
            <div class="stepper">
              <button @click="gpuCount = Math.max(1, gpuCount - 1)">−</button>
              <span>{{ gpuCount }}</span>
              <button @click="gpuCount++">+</button>
            </div>
          </div>
          <div class="form-row">
            <label>副本数</label>
            <div class="stepper">
              <button @click="replicas = Math.max(1, replicas - 1)">−</button>
              <span>{{ replicas }}</span>
              <button @click="replicas++">+</button>
            </div>
          </div>
          <div class="form-row">
            <label>内存 (MB)</label>
            <input v-model.number="memoryMB" type="number" min="1024" step="1024" />
          </div>
        </div>

        <div class="resource-check" :class="gpuEnough ? 'ok' : 'bad'">
          {{ gpuEnough ? '✓' : '✗' }} 资源检查：需要 {{ needGPU }} 张 {{ gpuType }}，当前可用
          {{ availableGPU }} 张（集群总 {{ gpus?.summary.availableGpu ?? 0 }} 可用）
        </div>

        <div class="flex" style="justify-content: space-between">
          <button @click="back">上一步</button>
          <button class="primary" @click="next">下一步</button>
        </div>
      </div>

      <!-- Step 3: 配置服务 -->
      <div v-if="step === 3" class="panel">
        <div class="form-row">
          <label>服务名称 *</label>
          <input v-model="svcName" placeholder="如 qwen-demo" />
        </div>
        <div class="form-row">
          <label>最大上下文长度</label>
          <input v-model.number="contextLength" type="number" min="1024" step="1024" />
        </div>
        <div class="form-row">
          <label>启动参数（空格分隔）</label>
          <input v-model="startupArgs" placeholder="如 --gpu-memory-utilization 0.9" />
        </div>
        <div class="summary-box">
          <div><strong>服务名称：</strong>{{ svcName || '-' }}</div>
          <div><strong>模型：</strong>{{ selectedVersionObj?.modelName || '' }}:{{ selectedVersionObj?.version }}</div>
          <div><strong>资源：</strong>{{ gpuType }} × {{ gpuCount }} × {{ replicas }} 副本 = {{ needGPU }} GPU</div>
          <div><strong>内存：</strong>{{ (memoryMB / 1024).toFixed(0) }} GB/副本</div>
        </div>
        <div class="flex" style="justify-content: space-between">
          <button @click="back">上一步</button>
          <button class="primary" @click="next">下一步</button>
        </div>
      </div>

      <!-- Step 4: 确认部署 -->
      <div v-if="step === 4" class="panel">
        <div class="panel-title">确认部署信息</div>
        <div class="summary-box">
          <div><strong>服务名称：</strong>{{ svcName }}</div>
          <div><strong>模型：</strong>{{ selectedVersionObj?.modelName }}:{{ selectedVersionObj?.version }}</div>
          <div><strong>运行时：</strong>{{ selectedVersionObj?.runtime }}</div>
          <div><strong>GPU：</strong>{{ gpuType }} × {{ gpuCount }} × {{ replicas }} 副本 = {{ needGPU }} GPU</div>
          <div><strong>内存：</strong>{{ (memoryMB / 1024).toFixed(0) }} GB/副本</div>
          <div><strong>上下文：</strong>{{ contextLength }}</div>
          <div><strong>启动参数：</strong>{{ startupArgs || '-' }}</div>
          <div><strong>租户：</strong>{{ deploymentTenant }}</div>
          <div><strong>命名空间：</strong>tenant-{{ deploymentTenant }}</div>
          <div><strong>幂等键：</strong><span class="mono">{{ idempotencyKey }}</span></div>
        </div>
        <p class="dim" style="margin-bottom: 16px">
          重复提交相同幂等键将返回同一部署，不会重复创建资源。
        </p>
        <div class="flex" style="justify-content: space-between">
          <button @click="back">上一步</button>
          <button class="primary" :disabled="submitting" @click="deploy">
            {{ submitting ? '部署中...' : '确认部署' }}
          </button>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.version-info {
  background: var(--bg);
  border-radius: 8px;
  padding: 12px;
  margin-bottom: 16px;
  color: var(--text-dim);
  font-size: 13px;
  line-height: 1.8;
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.stepper {
  display: flex;
  align-items: center;
  gap: 12px;
}
.stepper button {
  width: 32px;
  height: 32px;
  padding: 0;
  font-size: 16px;
  background: var(--bg-hover);
}
.stepper span {
  min-width: 32px;
  text-align: center;
  font-weight: 600;
}
.resource-check {
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 13px;
  margin-bottom: 16px;
}
.resource-check.ok {
  background: rgba(34, 197, 94, 0.1);
  color: var(--success);
}
.resource-check.bad {
  background: rgba(239, 68, 68, 0.1);
  color: var(--danger);
}
.summary-box {
  background: var(--bg);
  border-radius: 8px;
  padding: 14px;
  font-size: 13px;
  line-height: 2;
  margin-bottom: 16px;
}
</style>
