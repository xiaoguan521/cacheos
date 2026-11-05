import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from '../stores/session'
import { http } from '../api/client'

// 路由与规格书 7.1 的 MVP 模块对应
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
        { path: 'nodes', name: 'nodes', component: () => import('../views/Nodes.vue'), meta: { title: '节点' } },
        { path: 'sites', name: 'sites', component: () => import('../views/Sites.vue'), meta: { title: '站点与上游' } },
        { path: 'ip-sets', name: 'ip-sets', component: () => import('../views/IpSets.vue'), meta: { title: 'IP 集与地域库' } },
        { path: 'streams', name: 'streams', component: () => import('../views/Streams.vue'), meta: { title: '四层代理' } },
        { path: 'certificates', name: 'certificates', component: () => import('../views/Certificates.vue'), meta: { title: '证书' } },
        { path: 'imports', name: 'imports', component: () => import('../views/Imports.vue'), meta: { title: '配置导入' } },
        { path: 'snippets', name: 'snippets', component: () => import('../views/Snippets.vue'), meta: { title: '自定义片段' } },
        { path: 'releases', name: 'releases', component: () => import('../views/Releases.vue'), meta: { title: '发布' } },
        { path: 'dataplane', name: 'dataplane', component: () => import('../views/Dataplane.vue'), meta: { title: '数据面版本' } },
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
  document.title = (to.meta.title ? `${to.meta.title} · ` : '') + 'EdgeTraffic OS'
})

export default router
