<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { billingCSVURL, fetchBilling, setRateCard } from '../api'
import type { DailyUsage } from '../types'
import { useAuth } from '../composables/useAuth'

const { isAdmin } = useAuth()
const tenantId = ref('default')
const today = new Date().toISOString().slice(0, 10)
const start = new Date(Date.now() - 29 * 86400000).toISOString().slice(0, 10)
const from = ref(start)
const to = ref(today)
const rows = ref<DailyUsage[]>([])
const loading = ref(false)
const error = ref('')
const gpuType = ref('default')
const inputRate = ref(0)
const outputRate = ref(0)
const gpuRate = ref(0)

const totals = computed(() => rows.value.reduce((sum, row) => ({
  requests: sum.requests + row.requestCount,
  tokens: sum.tokens + row.inputTokens + row.outputTokens,
  gpuHours: sum.gpuHours + row.gpuReplicaSeconds / 3600,
  cost: sum.cost + row.estimatedCost,
}), { requests: 0, tokens: 0, gpuHours: 0, cost: 0 }))

async function load() {
  loading.value = true
  error.value = ''
  try { rows.value = await fetchBilling(tenantId.value, from.value, to.value) }
  catch (e) { error.value = (e as Error).message }
  finally { loading.value = false }
}

async function saveRate() {
  error.value = ''
  try {
    await setRateCard(tenantId.value, { gpuType: gpuType.value, inputTokenPerMillion: inputRate.value, outputTokenPerMillion: outputRate.value, gpuHour: gpuRate.value })
    await load()
  } catch (e) { error.value = (e as Error).message }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header"><h2>用量与账单</h2><a class="button" :href="billingCSVURL(tenantId, from, to)">导出 CSV</a></div>
    <div class="panel filters">
      <label>租户<input v-model="tenantId" /></label>
      <label>开始日期<input v-model="from" type="date" /></label>
      <label>结束日期<input v-model="to" type="date" /></label>
      <button class="primary" :disabled="loading" @click="load">查询</button>
    </div>
    <div v-if="error" class="error-box">{{ error }}</div>
    <div class="summary-grid">
      <div class="panel"><span>请求</span><strong>{{ totals.requests.toLocaleString() }}</strong></div>
      <div class="panel"><span>Token</span><strong>{{ totals.tokens.toLocaleString() }}</strong></div>
      <div class="panel"><span>GPU 副本小时</span><strong>{{ totals.gpuHours.toFixed(2) }}</strong></div>
      <div class="panel"><span>估算费用</span><strong>¥ {{ totals.cost.toFixed(4) }}</strong></div>
    </div>
    <div class="panel">
      <div v-if="loading" class="empty">加载中...</div>
      <div v-else-if="!rows.length" class="empty">所选范围暂无用量</div>
      <table v-else><thead><tr><th>日期</th><th>部署</th><th>请求/失败</th><th>输入/输出 Token</th><th>GPU 小时</th><th>费用</th></tr></thead><tbody><tr v-for="row in rows" :key="row.date + row.deploymentId"><td>{{ row.date.slice(0,10) }}</td><td>{{ row.deploymentId }}</td><td>{{ row.requestCount }} / {{ row.failedCount }}</td><td>{{ row.inputTokens }} / {{ row.outputTokens }}</td><td>{{ (row.gpuReplicaSeconds/3600).toFixed(2) }}</td><td>¥ {{ row.estimatedCost.toFixed(4) }}</td></tr></tbody></table>
    </div>
    <div v-if="isAdmin" class="panel rate-card"><h3>设置费率</h3><label>GPU 型号<input v-model="gpuType" placeholder="default 或 A100" /></label><label>输入 / 百万 Token<input v-model.number="inputRate" type="number" min="0" /></label><label>输出 / 百万 Token<input v-model.number="outputRate" type="number" min="0" /></label><label>GPU / 小时<input v-model.number="gpuRate" type="number" min="0" /></label><button class="primary" @click="saveRate">保存</button></div>
  </div>
</template>

<style scoped>
.filters,.rate-card{display:flex;align-items:end;gap:14px;flex-wrap:wrap;margin-bottom:16px}.filters label,.rate-card label{display:flex;flex-direction:column;gap:6px;color:var(--text-dim);font-size:12px}.summary-grid{display:grid;grid-template-columns:repeat(4,1fr);gap:12px;margin-bottom:16px}.summary-grid .panel{display:flex;flex-direction:column;gap:8px}.summary-grid span{color:var(--text-dim);font-size:12px}.summary-grid strong{font-size:22px}.button{display:inline-flex;padding:8px 14px;border:1px solid var(--border);border-radius:6px;color:var(--text);text-decoration:none}@media(max-width:900px){.summary-grid{grid-template-columns:repeat(2,1fr)}}
</style>
