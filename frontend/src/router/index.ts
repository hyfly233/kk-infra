// 路由定义：按角色控制页面访问
import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'
import { useAuth } from '../composables/useAuth'
import type { Role } from '../composables/useAuth'

declare module 'vue-router' {
  interface RouteMeta {
    title?: string
    // 允许访问的角色；缺省表示登录用户均可
    roles?: Role[]
    // 需要管理员
    adminOnly?: boolean
  }
}

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('../views/LoginView.vue'),
    meta: { title: '登录' },
  },
  {
    path: '/',
    component: () => import('../layout/AdminLayout.vue'),
    meta: { title: 'Carrot AI Infra' },
    children: [
      {
        path: '',
        name: 'dashboard',
        component: () => import('../views/DashboardView.vue'),
        meta: { title: '总览' },
      },
      {
        path: 'models',
        name: 'models',
        component: () => import('../views/ModelsView.vue'),
        meta: { title: '模型管理' },
      },
      {
        path: 'models/:id',
        name: 'model-detail',
        component: () => import('../views/ModelDetailView.vue'),
        meta: { title: '模型详情' },
      },
      {
        path: 'deployments',
        name: 'deployments',
        component: () => import('../views/DeploymentsView.vue'),
        meta: { title: '模型服务' },
      },
      {
        path: 'deployments/:id',
        name: 'deployment-detail',
        component: () => import('../views/DeploymentDetailView.vue'),
        meta: { title: '服务详情' },
      },
      {
        path: 'deployments/new',
        name: 'deployment-new',
        component: () => import('../views/DeployWizardView.vue'),
        meta: { title: '部署服务', adminOnly: true },
      },
      {
        path: 'gpus',
        name: 'gpus',
        component: () => import('../views/GPUsView.vue'),
        meta: { title: 'GPU 资源' },
      },
      {
        path: 'notebooks',
        name: 'notebooks',
        component: () => import('../views/NotebookView.vue'),
        meta: { title: 'Notebook 工作空间' },
      },
      {
        path: 'quotas',
        name: 'quotas',
        component: () => import('../views/QuotasView.vue'),
        meta: { title: '租户与配额', adminOnly: true },
      },
      {
        path: 'audit',
        name: 'audit',
        component: () => import('../views/AuditView.vue'),
        meta: { title: '告警与审计', adminOnly: true },
      },
	  {
		path: 'billing',
		name: 'billing',
		component: () => import('../views/BillingView.vue'),
		meta: { title: '用量与账单' },
	  },
      {
        path: 'keys',
        name: 'keys',
        component: () => import('../views/KeysView.vue'),
        meta: { title: 'API Key', adminOnly: true },
      },
      {
        path: 'settings',
        name: 'settings',
        component: () => import('../views/SettingsView.vue'),
        meta: { title: '系统设置', adminOnly: true },
      },
    ],
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: '/',
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 全局守卫：登录检查 + 角色检查
router.beforeEach((to) => {
  const auth = useAuth()
  if (to.name === 'login') {
    // 已登录访问登录页 → 跳首页
    if (auth.isLoggedIn.value) return { name: 'dashboard' }
    return true
  }
  // 未登录 → 登录页
  if (!auth.isLoggedIn.value) {
    return { name: 'login', query: { redirect: to.fullPath } }
  }
  // 管理员专属页面
  if (to.meta.adminOnly && !auth.isAdmin.value) {
    return { name: 'dashboard' }
  }
  // 角色白名单
  if (to.meta.roles && !to.meta.roles.includes(auth.role.value as Role)) {
    return { name: 'dashboard' }
  }
  return true
})

// 动态标题
router.afterEach((to) => {
  document.title = to.meta.title ? `${to.meta.title} · Carrot AI Infra` : 'Carrot AI Infra'
})

export default router
