import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from '../stores/session'
import Layout from '../layouts/PortalLayout.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/login', name: 'login', component: () => import('../views/Login.vue'), meta: { public: true } },
    {
      path: '/',
      component: Layout,
      children: [
        { path: '', name: 'dashboard', component: () => import('../views/Dashboard.vue'), meta: { title: '总览' } },
        { path: 'customers', name: 'customers', component: () => import('../views/Customers.vue'), meta: { title: '客户与订单' } },
        { path: 'licenses', name: 'licenses', component: () => import('../views/Licenses.vue'), meta: { title: 'License' } },
        { path: 'batches', name: 'batches', component: () => import('../views/Batches.vue'), meta: { title: '签名批次' } },
        { path: 'revocations', name: 'revocations', component: () => import('../views/Revocations.vue'), meta: { title: '吊销' } },
        { path: 'keys', name: 'keys', component: () => import('../views/Keys.vue'), meta: { title: '签名公钥' } },
        { path: 'users', name: 'users', component: () => import('../views/Users.vue'), meta: { title: '用户' } },
        { path: 'audit', name: 'audit', component: () => import('../views/Audit.vue'), meta: { title: '审计' } },
      ],
    },
  ],
})

router.beforeEach(async (to) => {
  if (to.meta.public) return true
  const s = useSession()
  if (!s.me) {
    try {
      await s.load()
    } catch {
      return { name: 'login', query: { next: to.fullPath } }
    }
  }
  return true
})

export default router
