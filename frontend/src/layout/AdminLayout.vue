<script setup lang="ts">
// 管理后台布局：侧边栏 + 顶栏，按角色显示菜单
import { useAuth } from '../composables/useAuth'
import { useRoute, useRouter } from 'vue-router'
import { computed } from 'vue'

const { isAdmin, role, username, logout } = useAuth()
const roleLabels = { platform_admin: '平台管理员', tenant_admin: '租户管理员', developer: '开发者', viewer: '只读者' }
const route = useRoute()
const router = useRouter()

// 菜单项：管理员看到全部，普通用户只看可访问部分
const allMenus = [
  { name: 'dashboard', label: '总览', path: '/' },
  { name: 'models', label: '模型管理', path: '/models' },
  { name: 'deployments', label: '模型服务', path: '/deployments' },
  { name: 'gpus', label: 'GPU 资源', path: '/gpus' },
  { name: 'notebooks', label: 'Notebook 工作空间', path: '/notebooks' },
  { name: 'quotas', label: '租户与配额', path: '/quotas', adminOnly: true },
  { name: 'audit', label: '告警与审计', path: '/audit', adminOnly: true },
  { name: 'billing', label: '用量与账单', path: '/billing' },
  { name: 'keys', label: 'API Key', path: '/keys', adminOnly: true },
  { name: 'settings', label: '系统设置', path: '/settings', adminOnly: true },
]

const menus = computed(() => allMenus.filter((m) => !m.adminOnly || isAdmin.value))

function isActive(path: string) {
  if (path === '/') return route.path === '/'
  return route.path.startsWith(path)
}

async function doLogout() {
  try { await logout() }
  catch (e) { window.alert(e instanceof Error ? e.message : '服务端退出未确认') }
  finally { await router.push('/login') }
}
</script>

<template>
  <div class="layout">
    <aside class="sidebar">
      <div class="logo">
        <span class="logo-dot"></span>
        Carrot AI Infra
      </div>
      <nav>
        <RouterLink
          v-for="m in menus"
          :key="m.name"
          :to="m.path"
          class="nav-item"
          :class="{ active: isActive(m.path) }"
        >
          {{ m.label }}
        </RouterLink>
      </nav>
    </aside>

    <div class="main">
      <header class="topbar">
        <div class="topbar-title">{{ route.meta.title ?? '' }}</div>
        <div class="flex">
          <span class="role-tag" :class="isAdmin ? 'admin' : 'user'">
            {{ role ? roleLabels[role] : '' }}
          </span>
          <span class="username">{{ username }}</span>
          <button class="ghost" @click="doLogout">退出</button>
        </div>
      </header>
      <main class="content">
        <RouterView />
      </main>
    </div>
  </div>
</template>

<style scoped>
.layout {
  display: flex;
  height: 100%;
}
.sidebar {
  width: var(--sidebar-width);
  background: var(--bg-panel);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
}
.logo {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 20px;
  font-size: 15px;
  font-weight: 700;
  border-bottom: 1px solid var(--border);
}
.logo-dot {
  width: 10px;
  height: 10px;
  border-radius: 50%;
  background: var(--primary);
  box-shadow: 0 0 8px var(--primary);
}
nav {
  padding: 12px 10px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.nav-item {
  padding: 10px 14px;
  border-radius: 8px;
  color: var(--text-dim);
  text-decoration: none;
  font-size: 13px;
  transition: all 0.15s;
}
.nav-item:hover {
  background: var(--bg-hover);
  color: var(--text);
}
.nav-item.active {
  background: rgba(79, 140, 255, 0.15);
  color: var(--primary);
  font-weight: 600;
}
.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
}
.topbar {
  height: 54px;
  border-bottom: 1px solid var(--border);
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  background: var(--bg-panel);
}
.topbar-title {
  font-size: 15px;
  font-weight: 600;
}
.role-tag {
  padding: 3px 10px;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 500;
}
.role-tag.admin {
  background: rgba(245, 158, 11, 0.15);
  color: var(--warning);
}
.role-tag.user {
  background: rgba(56, 189, 248, 0.15);
  color: var(--info);
}
.username {
  color: var(--text-dim);
}
.content {
  flex: 1;
  overflow-y: auto;
}
</style>
