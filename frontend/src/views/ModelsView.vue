<script setup lang="ts">
// 模型管理：列表 + 注册模型
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { createModel, deleteModel, fetchModels } from '../api'
import { useAuth } from '../composables/useAuth'
import type { Model } from '../types'
import { fmtTime } from '../utils/status'

const router = useRouter()
const { canWrite } = useAuth()

const loading = ref(true)
const error = ref('')
const models = ref<Model[]>([])
const search = ref('')

// 注册模型弹窗
const showCreate = ref(false)
const createError = ref('')
const newName = ref('')
const newDesc = ref('')
const submitting = ref(false)

const filteredModels = () => {
  const q = search.value.trim().toLowerCase()
  if (!q) return models.value
  return models.value.filter(
    (m) => m.name.toLowerCase().includes(q) || (m.description ?? '').toLowerCase().includes(q),
  )
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    models.value = await fetchModels()
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

async function submitCreate() {
  createError.value = ''
  if (!newName.value.trim()) {
    createError.value = '模型名称必填'
    return
  }
  submitting.value = true
  try {
    await createModel({ name: newName.value.trim(), description: newDesc.value.trim() })
    showCreate.value = false
    newName.value = ''
    newDesc.value = ''
    await load()
  } catch (e) {
    createError.value = (e as Error).message
  } finally {
    submitting.value = false
  }
}

async function removeModel(m: Model) {
  if (!confirm(`确认删除模型「${m.name}」？关联版本将一并删除。`)) return
  try {
    await deleteModel(m.id)
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
      <h2>模型管理</h2>
      <div class="flex">
        <input v-model="search" placeholder="搜索模型..." class="search-input" />
        <button v-if="canWrite" class="primary" @click="showCreate = true">注册模型</button>
      </div>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="loading" class="empty">加载中...</div>
    <div v-else-if="!filteredModels().length" class="empty">暂无模型</div>

    <div class="panel" v-else>
      <table>
        <thead>
          <tr><th>名称</th><th>说明</th><th>创建时间</th><th>操作</th></tr>
        </thead>
        <tbody>
          <tr v-for="m in filteredModels()" :key="m.id">
            <td class="model-name" @click="router.push(`/models/${m.id}`)">{{ m.name }}</td>
            <td class="dim">{{ m.description || '-' }}</td>
            <td class="dim">{{ fmtTime(m.createdAt) }}</td>
            <td>
              <div class="flex">
                <button class="ghost" @click="router.push(`/models/${m.id}`)">详情</button>
                <button v-if="canWrite && m.tenantId" class="danger" @click="removeModel(m)">删除</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 注册模型弹窗 -->
    <div v-if="showCreate" class="modal-mask" @click.self="showCreate = false">
      <div class="modal">
        <h3>注册模型</h3>
        <div v-if="createError" class="error-box">{{ createError }}</div>
        <div class="form-row">
          <label>模型名称 *</label>
          <input v-model="newName" placeholder="如 qwen、llama" />
        </div>
        <div class="form-row">
          <label>说明</label>
          <textarea v-model="newDesc" rows="3" placeholder="模型用途说明"></textarea>
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
.search-input {
  width: 220px;
}
.model-name {
  font-weight: 600;
  color: var(--primary);
  cursor: pointer;
}
.dim {
  color: var(--text-dim);
}
</style>
