<script setup lang="ts">
// API Key 管理（管理员专属）：创建（明文展示一次）/禁用/轮换/模型授权
import { onMounted, ref } from 'vue'
import { disableKey, fetchKeys, fetchModels, issueKey, rotateKey, setKeyModels } from '../api'
import type { APIKey, IssueKeyResult, Model } from '../types'
import { fmtTime } from '../utils/status'
import { useAuth } from '../composables/useAuth'

const { tenantId } = useAuth()

const loading = ref(true)
const error = ref('')
const keys = ref<APIKey[]>([])

// 创建弹窗
const showCreate = ref(false)
const creating = ref(false)
const createError = ref('')
// 明文 Key（仅展示一次）
const newKey = ref<IssueKeyResult | null>(null)

// 模型授权弹窗（R2-4）
const showModels = ref(false)
const modelTarget = ref<APIKey | null>(null)
const allModels = ref<Model[]>([])
const selectedModels = ref<string[]>([])
const savingModels = ref(false)
const modelError = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    keys.value = await fetchKeys()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function openModels(k: APIKey) {
  modelTarget.value = k
  modelError.value = ''
  selectedModels.value = [...(k.models ?? [])]
  try {
    const ms = await fetchModels()
    allModels.value = ms
  } catch {
    allModels.value = []
  }
  showModels.value = true
}

async function saveModels() {
  if (!modelTarget.value) return
  modelError.value = ''
  savingModels.value = true
  try {
    await setKeyModels(modelTarget.value.id, selectedModels.value)
    showModels.value = false
    await load()
  } catch (e) {
    modelError.value = (e as Error).message
  } finally {
    savingModels.value = false
  }
}

async function create() {
  createError.value = ''
  creating.value = true
  try {
    newKey.value = await issueKey(tenantId.value)
  } catch (e) {
    createError.value = (e as Error).message
  } finally {
    creating.value = false
  }
}

function closeNewKey() {
  newKey.value = null
  showCreate.value = false
  load()
}

async function doDisable(k: APIKey) {
  if (!confirm(`确认禁用 Key「${k.id}」？禁用后调用将失败。`)) return
  try {
    await disableKey(k.id)
    await load()
  } catch (e) {
    alert(`禁用失败: ${(e as Error).message}`)
  }
}

async function doRotate(k: APIKey) {
  if (!confirm(`确认轮换 Key「${k.id}」？旧 Key 将立即失效。`)) return
  try {
    newKey.value = await rotateKey(k.id)
  } catch (e) {
    alert(`轮换失败: ${(e as Error).message}`)
  }
}

function copyKey(key: string) {
  navigator.clipboard?.writeText(key).then(() => alert('已复制到剪贴板')).catch(() => {})
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>API Key 管理</h2>
      <button class="primary" @click="showCreate = true">创建 API Key</button>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="loading" class="empty">加载中...</div>

    <div class="panel" v-else>
      <div v-if="!keys.length" class="empty">暂无 API Key，点击右上角创建</div>
      <table v-else>
        <thead>
          <tr><th>Key ID</th><th>租户</th><th>创建时间</th><th>最近调用</th><th>状态</th><th>模型授权</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="k in keys" :key="k.id">
            <td class="mono">{{ k.id }}</td>
            <td>{{ k.tenantId }}</td>
            <td class="dim">{{ fmtTime(k.createdAt) }}</td>
            <td class="dim">{{ k.lastUsedAt ? fmtTime(k.lastUsedAt) : '从未调用' }}</td>
            <td>
              <span class="badge" :class="k.disabled ? 'red' : 'green'">
                {{ k.disabled ? '已禁用' : '启用' }}
              </span>
            </td>
            <td class="dim">
              {{ k.models?.length ? k.models.join(', ') : '全部模型' }}
            </td>
            <td>
              <div class="flex">
                <button v-if="!k.disabled" class="ghost" @click="openModels(k)">授权模型</button>
                <button v-if="!k.disabled" class="ghost" @click="doRotate(k)">轮换</button>
                <button v-if="!k.disabled" class="danger" @click="doDisable(k)">禁用</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 创建弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="closeNewKey">
      <div class="modal">
        <h3>创建 API Key</h3>
        <div v-if="createError" class="error-box">{{ createError }}</div>

        <!-- 明文 Key 展示（仅一次） -->
        <div v-if="newKey" class="success-box">
          <p style="margin-bottom: 8px"><strong>⚠️ 请立即保存以下 Key，关闭后将无法再次查看：</strong></p>
          <pre class="key-display">{{ newKey.key }}</pre>
          <div class="flex" style="margin-top: 12px">
            <button class="ghost" @click="copyKey(newKey.key)">复制</button>
            <button class="primary" @click="closeNewKey">我已保存</button>
          </div>
        </div>

        <template v-else>
          <p class="dim mb-16">创建后将生成一个全新 Key，明文只展示一次。Key 用于调用 OpenAI 兼容接口（Authorization: Bearer &lt;key&gt;）。</p>
          <div class="flex" style="justify-content: flex-end">
            <button @click="showCreate = false">取消</button>
            <button class="primary" :disabled="creating" @click="create">
              {{ creating ? '创建中...' : '创建' }}
            </button>
          </div>
        </template>
      </div>
    </div>

    <!-- 模型授权弹窗 -->
    <div v-if="showModels" class="modal-mask" @click.self="showModels = false">
      <div class="modal">
        <h3>授权模型：{{ modelTarget?.id }}</h3>
        <div v-if="modelError" class="error-box">{{ modelError }}</div>
        <p class="dim mb-16">
          选择该 Key 可访问的模型。留空表示可访问全部模型。
        </p>
        <div v-if="!allModels.length" class="empty">暂无模型</div>
        <div v-else class="model-check-list">
          <label v-for="m in allModels" :key="m.id" class="model-check">
            <input
              v-model="selectedModels"
              type="checkbox"
              :value="m.name"
            />
            {{ m.name }}
          </label>
        </div>
        <div class="flex" style="justify-content: flex-end; margin-top: 16px">
          <button @click="showModels = false">取消</button>
          <button class="primary" :disabled="savingModels" @click="saveModels">
            {{ savingModels ? '保存中...' : '保存' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.dim {
  color: var(--text-dim);
}
.key-display {
  background: var(--bg);
  border: 1px dashed var(--success);
  border-radius: 6px;
  padding: 12px;
  font-size: 13px;
  color: var(--success);
  word-break: break-all;
}
.model-check-list {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 8px;
  max-height: 280px;
  overflow-y: auto;
}
.model-check {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  background: var(--bg);
  border-radius: 6px;
  cursor: pointer;
  font-size: 13px;
}
</style>
