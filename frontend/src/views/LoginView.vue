<script setup lang="ts">
import { ref } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuth } from '../composables/useAuth'

const auth = useAuth()
const router = useRouter()
const route = useRoute()

const email = ref('')
const password = ref('')
const tenantId = ref('')
const busy = ref(false)
const error = ref('')

async function login() {
  if (!email.value.trim() || !password.value || !tenantId.value.trim()) {
    error.value = '请输入邮箱、密码和租户 ID'
    return
  }
  busy.value = true
  error.value = ''
  try {
    await auth.login(email.value.trim(), password.value, tenantId.value.trim())
    password.value = ''
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : '/'
    await router.push(redirect.startsWith('/') && !redirect.startsWith('//') ? redirect : '/')
  } catch (e) { error.value = e instanceof Error ? e.message : '登录失败' }
  finally { busy.value = false }
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <div class="login-logo">
        <span class="logo-dot"></span>
        <h1>Carrot AI Infra</h1>
        <p>AI 推理服务平台</p>
      </div>

      <div v-if="error" class="error-box">{{ error }}</div>

      <div class="form-row">
        <label>邮箱</label>
        <input v-model="email" type="email" autocomplete="username" placeholder="请输入账号邮箱" @keyup.enter="login" />
      </div>
      <div class="form-row">
        <label>密码</label>
        <input v-model="password" type="password" autocomplete="current-password" @keyup.enter="login" />
      </div>

      <div class="form-row">
        <label>租户 ID</label>
        <input v-model="tenantId" placeholder="所属租户 ID" @keyup.enter="login" />
      </div>
      <button class="primary login-btn" :disabled="busy" @click="login">{{ busy ? '登录中…' : '登 录' }}</button>
    </div>
  </div>
</template>

<style scoped>
.login-wrap {
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  background: radial-gradient(ellipse at top, #1a2440 0%, var(--bg) 60%);
}
.login-card {
  width: 400px;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: 14px;
  padding: 36px;
}
.login-logo {
  text-align: center;
  margin-bottom: 28px;
}
.logo-dot {
  display: inline-block;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: var(--primary);
  box-shadow: 0 0 12px var(--primary);
  margin-bottom: 12px;
}
.login-logo h1 {
  font-size: 22px;
  margin-bottom: 6px;
}
.login-logo p {
  color: var(--text-dim);
  font-size: 13px;
}
.form-row {
  margin-bottom: 18px;
}
.login-btn {
  width: 100%;
  padding: 12px;
  font-size: 15px;
  margin-top: 6px;
}
</style>
