<script setup lang="ts">
// 模型详情：基本信息 + 版本列表 + 注册版本/校验（管理员）
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { approveRelease, createVersion, deleteVersion, fetchLatestRelease, fetchModel, fetchVersions, startRelease, validateVersion } from '../api'
import type { ReleaseRecord } from '../api'
import { useAuth } from '../composables/useAuth'
import type { Model, ModelVersion } from '../types'
import { fmtTime, versionBadge } from '../utils/status'

const route = useRoute()
const router = useRouter()
const { isAdmin, canWrite } = useAuth()

const modelId = route.params.id as string
const loading = ref(true)
const error = ref('')
const model = ref<Model | null>(null)
const versions = ref<ModelVersion[]>([])
const releases = ref<Record<string, ReleaseRecord>>({})
const releaseBusy = ref(false)

// 注册版本弹窗
const showCreate = ref(false)
const createError = ref('')
const submitting = ref(false)
const form = ref({
  version: '',
  artifactUri: '',
  runtime: 'vLLM',
  gpuType: 'A100',
  gpuCount: 1,
  memoryMB: 32768,
  contextLength: 8192,
})

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [m, vs] = await Promise.all([fetchModel(modelId), fetchVersions(modelId)])
    model.value = m
    versions.value = vs
    const latest = await Promise.all(vs.map((version) => fetchLatestRelease(version.id)))
    releases.value = Object.fromEntries(latest.flat().map((record) => [record.modelVersionId, record]))
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function submitCreate() {
  createError.value = ''
  const f = form.value
  if (!f.version.trim() || !f.artifactUri.trim() || !f.gpuType.trim() || f.gpuCount < 1) {
    createError.value = '版本号/权重地址/GPU 类型/GPU 数量必填'
    return
  }
  submitting.value = true
  try {
    await createVersion(modelId, {
      version: f.version.trim(),
      artifactUri: f.artifactUri.trim(),
      runtime: f.runtime,
      gpuType: f.gpuType.trim(),
      gpuCount: f.gpuCount,
      memoryMB: f.memoryMB,
      contextLength: f.contextLength,
    })
    showCreate.value = false
    Object.assign(form.value, {
      version: '', artifactUri: '', gpuCount: 1, memoryMB: 32768, contextLength: 8192,
    })
    await load()
  } catch (e) {
    createError.value = (e as Error).message
  } finally {
    submitting.value = false
  }
}

async function doValidate(v: ModelVersion) {
  try {
    await validateVersion(v.id)
    await load()
  } catch (e) {
    alert(`校验失败: ${(e as Error).message}`)
  }
}

async function doRelease(v: ModelVersion) {
  if (!confirm(`为版本「${v.version}」启动校验、探针和基准流水线？通过后仍需管理员人工审批。`)) return
  releaseBusy.value = true
  try {
    await startRelease(v.id, v.tenantId)
  } catch (e) {
    alert(`流水线失败: ${(e as Error).message}`)
  } finally {
    await load()
    releaseBusy.value = false
  }
}

async function doApproval(v: ModelVersion, approved: boolean) {
  const release = releases.value[v.id]
  if (!release || !confirm(`${approved ? '批准' : '拒绝'}版本「${v.version}」的发布？`)) return
  releaseBusy.value = true
  try {
    await approveRelease(release.id, approved)
  } catch (e) {
    alert(`审批失败: ${(e as Error).message}`)
  } finally {
    await load()
    releaseBusy.value = false
  }
}

async function removeVersion(v: ModelVersion) {
  if (!confirm(`确认删除版本「${v.version}」？`)) return
  try {
    await deleteVersion(modelId, v.version)
    await load()
  } catch (e) {
    alert(`删除失败: ${(e as Error).message}`)
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <button class="ghost mb-16" @click="router.push('/models')">← 返回模型列表</button>
        <h2>模型：{{ model?.name ?? '...' }}</h2>
        <p class="dim">{{ model?.description }}</p>
      </div>
      <button v-if="canWrite && model?.tenantId" class="primary" @click="showCreate = true">注册版本</button>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="loading" class="empty">加载中...</div>

    <template v-else>
      <div class="panel">
        <div class="panel-title">版本列表</div>
        <div v-if="!versions.length" class="empty">暂无版本，请先注册模型版本</div>
        <table v-else>
          <thead>
            <tr><th>版本</th><th>权重地址</th><th>运行时</th><th>GPU</th><th>显存</th><th>状态</th><th>创建时间</th><th>操作</th></tr>
          </thead>
          <tbody>
            <tr v-for="v in versions" :key="v.id">
              <td><strong>{{ v.version }}</strong></td>
              <td class="dim mono">{{ v.artifactUri }}</td>
              <td>{{ v.runtime }}</td>
              <td>{{ v.gpuType }} × {{ v.gpuCount }}</td>
              <td>{{ (v.memoryMB / 1024).toFixed(0) }} GB</td>
              <td><span class="badge" :class="versionBadge(v.status)">{{ v.status }}</span></td>
              <td class="dim">{{ fmtTime(v.createdAt) }}</td>
              <td>
                <div class="flex">
                  <button
                    v-if="canWrite && model?.tenantId && v.status === 'REGISTERED'"
                    class="success"
                    @click="doValidate(v)"
                  >校验</button>
                  <button
                    v-if="canWrite && model?.tenantId && ['REGISTERED', 'VALIDATED'].includes(v.status) && releases[v.id]?.status !== 'PENDING_APPROVAL'"
                    class="success"
                    :disabled="releaseBusy"
                    @click="doRelease(v)"
                  >启动发布流水线</button>
                  <template v-if="isAdmin && releases[v.id]?.status === 'PENDING_APPROVAL'">
                    <button :disabled="releaseBusy" class="success" @click="doApproval(v, true)">审批发布</button>
                    <button :disabled="releaseBusy" class="danger" @click="doApproval(v, false)">拒绝</button>
                  </template>
                  <span v-if="releases[v.id]" class="dim">流水线：{{ releases[v.id]?.status }}</span>
                  <button
                    v-if="canWrite && model?.tenantId && v.status !== 'RELEASED' && v.status !== 'VALIDATED'"
                    class="danger"
                    @click="removeVersion(v)"
                  >删除</button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="panel mt-16">
        <div class="panel-title">部署提示</div>
        <p class="dim">
          版本校验通过（VALIDATED）后即可创建模型服务。部署时选择版本，配置 GPU 数量与副本数。
        </p>
      </div>
    </template>

    <!-- 注册版本弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="showCreate = false">
      <div class="modal">
        <h3>注册模型版本</h3>
        <div v-if="createError" class="error-box">{{ createError }}</div>
        <div class="form-row">
          <label>版本号 *</label>
          <input v-model="form.version" placeholder="如 7b、1.0" />
        </div>
        <div class="form-row">
          <label>权重地址 *</label>
          <input v-model="form.artifactUri" placeholder="如 s3://models/qwen-7b" />
        </div>
        <div class="grid-2">
          <div class="form-row">
            <label>运行时</label>
            <select v-model="form.runtime">
              <option value="vLLM">vLLM</option>
            </select>
          </div>
          <div class="form-row">
            <label>GPU 类型 *</label>
            <select v-model="form.gpuType">
              <option value="A100">A100</option>
              <option value="H100">H100</option>
              <option value="L40S">L40S</option>
              <option value="4090">4090</option>
            </select>
          </div>
          <div class="form-row">
            <label>GPU 数量 *</label>
            <input v-model.number="form.gpuCount" type="number" min="1" />
          </div>
          <div class="form-row">
            <label>显存 (MB)</label>
            <input v-model.number="form.memoryMB" type="number" min="1024" step="1024" />
          </div>
          <div class="form-row">
            <label>上下文长度</label>
            <input v-model.number="form.contextLength" type="number" min="1024" step="1024" />
          </div>
        </div>
        <div class="flex" style="justify-content: flex-end">
          <button @click="showCreate = false">取消</button>
          <button class="primary" :disabled="submitting" @click="submitCreate">
            {{ submitting ? '提交中...' : '注册' }}
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
</style>
