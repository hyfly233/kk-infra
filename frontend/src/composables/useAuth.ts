import { computed, ref } from 'vue'

export type Role = 'platform_admin' | 'tenant_admin' | 'developer' | 'viewer'
interface Session {
  accessToken: string
  refreshToken: string
  tenantId: string
  role: Role
  expiresAt: string
}
interface AuthState extends Session { username: string }
const STORAGE_KEY = 'carrot-session'
const state = ref<AuthState | null>(null)
const roles: Role[] = ['platform_admin', 'tenant_admin', 'developer', 'viewer']
try {
  const saved = JSON.parse(sessionStorage.getItem(STORAGE_KEY) || 'null') as AuthState | null
  if (saved && roles.includes(saved.role) && saved.accessToken && saved.refreshToken && Number.isFinite(Date.parse(saved.expiresAt))) state.value = saved
} catch { sessionStorage.removeItem(STORAGE_KEY) }

function save(value: AuthState | null) {
  state.value = value
  if (value) sessionStorage.setItem(STORAGE_KEY, JSON.stringify(value))
  else sessionStorage.removeItem(STORAGE_KEY)
}

async function sessionRequest(path: string, body: unknown): Promise<Session> {
  const response = await fetch(`/api/v1/auth/${path}`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body)
  })
  const result = await response.json()
  if (!response.ok || result.code !== 0) {
    throw Object.assign(new Error(result.message || '会话请求失败'), { status: response.status })
  }
  return result.data as Session
}

let refreshing: Promise<void> | null = null
async function refresh() {
  if (!refreshing) {
    const previous = state.value
    if (!previous) throw new Error('请先登录')
    refreshing = sessionRequest('refresh', { refreshToken: previous.refreshToken }).then((session) => {
      if (state.value?.refreshToken !== previous.refreshToken) throw new Error('会话已改变')
      save({ ...session, username: previous.username })
    }).catch((error) => {
      if (error.status === 401 && state.value?.refreshToken === previous.refreshToken) save(null)
      throw error
    }).finally(() => { refreshing = null })
  }
  await refreshing
}

export async function authenticatedFetch(url: string, options: RequestInit = {}): Promise<Response> {
  if (!['/api/', '/model-registry/', '/gateway/', '/pipeline/'].some((prefix) => url.startsWith(prefix)) || url.includes('\\')) throw new Error('不允许向外部地址发送会话凭据')
  if (!state.value) throw new Error('请先登录')
  if (Date.parse(state.value.expiresAt) - Date.now() < 30_000) await refresh()
  const previousToken = state.value?.accessToken
  const send = () => {
    const headers = new Headers(options.headers)
    headers.set('Authorization', `Bearer ${state.value?.accessToken || ''}`)
    return fetch(url, { ...options, headers })
  }
  let response = await send()
  if (response.status === 401) {
    if (state.value?.accessToken !== previousToken) throw new Error('请求期间会话已改变')
    await refresh()
    response = await send()
  }
  return response
}

export function useAuth() {
  const isAdmin = computed(() => state.value?.role === 'platform_admin' || state.value?.role === 'tenant_admin')
  const isLoggedIn = computed(() => state.value !== null)
  const canWrite = computed(() => state.value !== null && state.value.role !== 'viewer')
  const role = computed(() => state.value?.role ?? null)
  const username = computed(() => state.value?.username ?? '')
  const tenantId = computed(() => state.value?.tenantId ?? '')
  async function login(email: string, password: string, tenantId: string) {
    const session = await sessionRequest('login', { email, password, tenantId })
    save({ ...session, username: email })
  }
  async function logout() {
    const token = state.value?.refreshToken
    save(null)
    if (token) {
      const response = await fetch('/api/v1/auth/logout', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refreshToken: token })
      })
      if (!response.ok) throw new Error('服务端退出未确认，当前浏览器会话已清除')
    }
  }
  return { isAdmin, isLoggedIn, canWrite, role, username, tenantId, login, logout }
}
