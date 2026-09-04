<script setup lang="ts">
// 告警与审计页（管理员）：审计日志查询
import { onMounted, ref } from 'vue'
import { fetchAudit, type AuditEntry } from '../api'
import { fmtTime } from '../utils/status'

const loading = ref(true)
const error = ref('')
const entries = ref<AuditEntry[]>([])
const search = ref('')
const prometheusAlertsURL = `${import.meta.env.VITE_PROMETHEUS_URL ?? 'http://localhost:9090'}/alerts`

const filtered = () => {
  const q = search.value.trim().toLowerCase()
  if (!q) return entries.value
  return entries.value.filter(
    (e) =>
      e.action.toLowerCase().includes(q) ||
      e.resource.toLowerCase().includes(q) ||
      e.tenantId.toLowerCase().includes(q) ||
      e.detail.toLowerCase().includes(q),
  )
}

// action → badge 样式
function actionBadge(action: string): string {
  if (action.includes('delete')) return 'red'
  if (action.includes('create')) return 'green'
  if (action.includes('scale')) return 'orange'
  return 'blue'
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    entries.value = await fetchAudit(100)
  } catch (e) {
    error.value = (e as Error).message
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2>告警与审计</h2>
	  <div class="flex">
		<a class="alert-link" :href="prometheusAlertsURL" target="_blank" rel="noopener">查看 Prometheus 告警 ↗</a>
        <input v-model="search" placeholder="搜索审计记录..." class="search-input" />
        <button class="ghost" @click="load">刷新</button>
      </div>
    </div>

    <div v-if="error" class="error-box">{{ error }}</div>
    <div v-if="loading" class="empty">加载中...</div>

    <div class="panel" v-else>
      <div v-if="!filtered().length" class="empty">暂无审计记录</div>
      <table v-else>
        <thead>
          <tr><th>时间</th><th>操作</th><th>租户</th><th>资源</th><th>详情</th></tr>
        </thead>
        <tbody>
          <tr v-for="e in filtered()" :key="e.id">
            <td class="dim">{{ fmtTime(e.createdAt) }}</td>
            <td><span class="badge" :class="actionBadge(e.action)">{{ e.action }}</span></td>
            <td>{{ e.tenantId }}</td>
            <td class="mono">{{ e.resource }}</td>
            <td>{{ e.detail }}</td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.search-input {
  width: 220px;
}
.dim {
  color: var(--text-dim);
}
.mono {
  font-family: 'SF Mono', Menlo, monospace;
  font-size: 12px;
}
.alert-link { color:var(--primary);text-decoration:none;font-size:12px }
</style>
