import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from '../stores/session'
import { http } from '../api/client'

// CacheOS 控制面模块路由。
const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/setup', name: 'setup', component: () => import('../views/Setup.vue'), meta: { public: true } },
    { path: '/password', name: 'password', component: () => import('../views/ChangePassword.vue'), meta: { public: true } },
    { path: '/login', name: 'login', component: () => import('../views/Login.vue'), meta: { public: true } },
    {
      path: '/',
      component: () => import('../layouts/ConsoleLayout.vue'),
      children: [
        { path: '', redirect: '/dashboard' },
        { path: 'dashboard', name: 'dashboard', component: () => import('../views/Dashboard.vue'), meta: { title: '总览' } },
        { path: 'hosts', name: 'hosts', component: () => import('../views/Hosts.vue'), meta: { title: '主机' } },
        { path: 'units', name: 'units', component: () => import('../views/Units.vue'), meta: { title: '部署单元' } },
        { path: 'units/:id', name: 'unit-detail', component: () => import('../views/UnitDetail.vue'), meta: { title: '部署单元详情' } },
        { path: 'units/:id/diagnose', name: 'diagnose', component: () => import('../views/Diagnose.vue'), meta: { title: '性能诊断' } },
        { path: 'units/:id/backups', name: 'backups', component: () => import('../views/Backups.vue'), meta: { title: '备份与恢复' } },
        { path: 'units/:id/migrations', name: 'migrations', component: () => import('../views/Migrate.vue'), meta: { title: '在线迁移' } },
        { path: 'units/:id/keys', name: 'keys', component: () => import('../views/Keys.vue'), meta: { title: 'Key 浏览器' } },
        { path: 'operations', name: 'operations', component: () => import('../views/Operations.vue'), meta: { title: '操作记录' } },
        { path: 'templates', name: 'templates', component: () => import('../views/Templates.vue'), meta: { title: '参数模板' } },
        { path: 'alerts', name: 'alerts', component: () => import('../views/Alerts.vue'), meta: { title: '告警' } },
        { path: 'audit', name: 'audit', component: () => import('../views/Audit.vue'), meta: { title: '审计' } },
        { path: 'system', name: 'system', component: () => import('../views/System.vue'), meta: { title: '授权与系统' } },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/dashboard' },
  ],
})

let checkedSetup = false

router.beforeEach(async (to) => {
  if (!checkedSetup) {
    const st = await http.get('/setup/status').then((r) => r.data).catch(() => null)
    checkedSetup = true
    if (st && !st.initialized && to.name !== 'setup') return { name: 'setup' }
  }
  if (to.meta.public) return true
  const s = useSession()
  if (!s.username) {
    try {
      await s.loadMe()
      await s.loadLicense()
    } catch {
      return { name: 'login', query: { next: to.fullPath } }
    }
  }
  return true
})

router.afterEach((to) => {
  document.title = (to.meta.title ? `${to.meta.title} · ` : '') + 'CacheOS 控制台'
})

export default router
