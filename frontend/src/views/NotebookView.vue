<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { fetchNotebook, startNotebook, deleteNotebook } from '../api'
import type { NotebookWorkspace } from '../api'
import { useAuth } from '../composables/useAuth'

const { canWrite, tenantId } = useAuth()
const workspace = ref<NotebookWorkspace | null>(null)
const error = ref('')
const busy = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
let disposed = false
let operation = 0
async function load() {
  if (disposed) return
  const version = operation
  try {
    const result = await fetchNotebook()
    if (!disposed && !busy.value && version === operation) { workspace.value = result; error.value = '' }
  } catch (e) {
    if (!disposed && !busy.value && version === operation) error.value = e instanceof Error ? e.message : '查询失败'
  }
  if (!disposed) timer = setTimeout(load, 5000)
}
async function operate(remove: boolean) {
  if (remove && !window.confirm('停止并移除工作空间？持久卷按部署策略保留，不会自动擦除个人文件。')) return
  busy.value = true
  operation++
  error.value = ''
  try { workspace.value = await (remove ? deleteNotebook() : startNotebook()) }
  catch (e) { error.value = e instanceof Error ? e.message : '操作未确认，请刷新状态' }
  finally { busy.value = false }
}
onMounted(load)
onUnmounted(() => { disposed = true; clearTimeout(timer) })
</script>

<template>
  <section class="card">
    <h2>个人 Notebook 工作空间</h2>
    <p>租户：{{ tenantId }}。工作空间仅属于当前账号，启动配置由平台管理。</p>
    <p v-if="error" class="error-box" role="alert">{{ error }}</p>
    <p>状态：{{ workspace?.status || '查询中' }}</p>
    <div class="flex">
      <button class="primary" :disabled="busy || !canWrite || !workspace || !['ABSENT', 'STOPPED'].includes(workspace.status)" @click="operate(false)">启动工作空间</button>
      <button :disabled="busy || !canWrite || !workspace || ['ABSENT', 'STOPPING'].includes(workspace.status)" @click="operate(true)">停止并移除</button>
      <a v-if="workspace?.status === 'RUNNING' && workspace.url" :href="workspace.url" target="_blank" rel="noopener noreferrer">打开 JupyterLab</a>
    </div>
    <p>Hub 登录用户名为「邮箱|租户 ID」，使用平台密码。管理令牌不会传递到浏览器或 URL。</p>
  </section>
</template>
