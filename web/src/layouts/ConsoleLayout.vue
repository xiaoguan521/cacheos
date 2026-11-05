<script setup lang="ts">
import { computed, h, onMounted, onUnmounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  AlertOutlined,
  ApiOutlined,
  AuditOutlined,
  ClusterOutlined,
  CloudUploadOutlined,
  CodeOutlined,
  DashboardOutlined,
  EnvironmentOutlined,
  GlobalOutlined,
  ImportOutlined,
  KeyOutlined,
  LockOutlined,
  LogoutOutlined,
  RocketOutlined,
  SafetyCertificateOutlined,
} from '@antdv-next/icons'
import { editionName, licenseStateName, useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()

const items = [
  { key: 'dashboard', icon: () => h(DashboardOutlined), label: '总览' },
  { key: 'nodes', icon: () => h(ClusterOutlined), label: '节点' },
  { key: 'sites', icon: () => h(GlobalOutlined), label: '站点与上游' },
  { key: 'ip-sets', icon: () => h(EnvironmentOutlined), label: 'IP 集与地域库' },
  { key: 'streams', icon: () => h(ApiOutlined), label: '四层代理' },
  { key: 'certificates', icon: () => h(LockOutlined), label: '证书' },
  { key: 'imports', icon: () => h(ImportOutlined), label: '配置导入' },
  { key: 'snippets', icon: () => h(CodeOutlined), label: '自定义片段' },
  { key: 'releases', icon: () => h(RocketOutlined), label: '发布' },
  { key: 'dataplane', icon: () => h(CloudUploadOutlined), label: '数据面版本' },
  { key: 'alerts', icon: () => h(AlertOutlined), label: '告警' },
  { key: 'audit', icon: () => h(AuditOutlined), label: '审计' },
  { key: 'system', icon: () => h(SafetyCertificateOutlined), label: '授权与系统' },
]

const roleName: Record<string, string> = { admin: '管理员', operator: '运维', releaser: '发布员', viewer: '只读' }

const selected = computed(() => [String(route.name || 'dashboard')])

const lic = computed(() => session.license)
const licTone = computed(() => {
  switch (lic.value?.state) {
    case 'VALID':
      return 'ok'
    case 'EXPIRING':
    case 'GRACE':
      return 'warn'
    default:
      return 'bad'
  }
})
const licText = computed(() => {
  const l = lic.value
  if (!l) return ''
  const name = licenseStateName[l.state] || l.state
  if (l.state === 'VALID' || l.state === 'EXPIRING' || l.state === 'GRACE') {
    const ed = editionName[l.edition || ''] || l.edition
    return l.state === 'VALID' ? `${ed}，剩余 ${Math.max(l.days_left, 0)} 天` : `${ed}${name}，剩余 ${Math.max(l.days_left, 0)} 天`
  }
  return `授权${name}，控制台只读`
})

let timer: number | undefined
onMounted(() => {
  timer = window.setInterval(() => session.loadLicense().catch(() => {}), 60000)
})
onUnmounted(() => clearInterval(timer))

async function logout() {
  await session.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <a-layout class="shell">
    <a-layout-sider :width="216" class="sider">
      <div class="brand">
        <div class="mark" aria-hidden="true"><span /><span /><span /></div>
        <div>
          <div class="name">EdgeTraffic OS</div>
          <div class="sub">边缘流量操作系统</div>
        </div>
      </div>
      <a-menu theme="dark" mode="inline" :selected-keys="selected" :items="items" @click="(e: any) => router.push({ name: e.key })" />
      <div class="runtime">数据面 syapp</div>
    </a-layout-sider>
    <a-layout>
      <a-layout-header class="topbar">
        <div class="title">{{ route.meta.title }}</div>
        <div class="right">
          <router-link v-if="lic" :to="{ name: 'system' }" class="lic" :class="licTone">
            <i class="dot" />{{ licText }}
          </router-link>
          <a-dropdown :menu="{ items: [{ key: 'password', icon: () => h(KeyOutlined), label: '修改口令' }, { key: 'logout', icon: () => h(LogoutOutlined), label: '退出登录' }], onClick: (e: any) => (e.key === 'password' ? router.push({ name: 'password' }) : logout()) }">
            <a-button type="text">{{ session.username }}（{{ roleName[session.role] || session.role }}）</a-button>
          </a-dropdown>
        </div>
      </a-layout-header>
      <a-layout-content>
        <router-view />
      </a-layout-content>
    </a-layout>
  </a-layout>
</template>

<style scoped>
.shell {
  min-height: 100vh;
}
.sider {
  position: sticky;
  top: 0;
  height: 100vh;
}
.sider :deep(.ant-layout-sider-children) {
  display: flex;
  flex-direction: column;
}
.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 20px 18px 18px;
  color: #fff;
}
/* 三条车道组成的标识：对应“流量”与“发布车道” */
.mark {
  display: grid;
  gap: 3px;
  width: 26px;
}
.mark span {
  display: block;
  height: 4px;
  border-radius: 2px;
  background: #0e7c86;
}
.mark span:nth-child(2) {
  width: 70%;
  background: #2f9e5b;
}
.mark span:nth-child(3) {
  width: 40%;
  background: #d9a21b;
}
.name {
  font-weight: 650;
  font-size: 15px;
  letter-spacing: 0.02em;
}
.sub {
  font-size: 12px;
  color: #8fa7ab;
  margin-top: 2px;
}
.runtime {
  margin-top: auto;
  padding: 16px 20px;
  font-size: 12px;
  color: #7f979b;
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px 0 28px;
  border-bottom: 1px solid #e1e7e6;
  height: 56px;
  line-height: 56px;
}
.title {
  font-size: 15px;
  color: #16323a;
  font-weight: 600;
}
.right {
  display: flex;
  align-items: center;
  gap: 16px;
}
.lic {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  height: 28px;
  line-height: 28px;
  padding: 0 12px;
  border-radius: 14px;
  font-size: 13px;
  text-decoration: none;
}
.lic .dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: currentColor;
}
.lic.ok {
  color: #1f6f43;
  background: #e6f3ec;
}
.lic.warn {
  color: #8a6510;
  background: #fbf1d8;
}
.lic.bad {
  color: #a3332d;
  background: #f8e3e1;
}
</style>
