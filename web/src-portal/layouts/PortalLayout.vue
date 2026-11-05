<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  AuditOutlined,
  DashboardOutlined,
  FileProtectOutlined,
  KeyOutlined,
  LogoutOutlined,
  StopOutlined,
  TeamOutlined,
  UserOutlined,
  UsbOutlined,
} from '@antdv-next/icons'
import { roleName, useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()
const collapsed = ref(false)

const items = computed(() => {
  const all = [
    { key: 'dashboard', icon: () => h(DashboardOutlined), label: '总览' },
    { key: 'customers', icon: () => h(TeamOutlined), label: '客户与订单' },
    { key: 'licenses', icon: () => h(FileProtectOutlined), label: 'License' },
    { key: 'batches', icon: () => h(UsbOutlined), label: '签名批次' },
    { key: 'revocations', icon: () => h(StopOutlined), label: '吊销' },
    { key: 'keys', icon: () => h(KeyOutlined), label: '签名公钥' },
    { key: 'users', icon: () => h(UserOutlined), label: '用户', admin: true },
    { key: 'audit', icon: () => h(AuditOutlined), label: '审计' },
  ]
  return all.filter((i) => !i.admin || session.has('admin')).map(({ admin: _a, ...rest }) => rest)
})
const selected = computed(() => [String(route.name || 'dashboard')])
const roles = computed(() => (session.me?.roles || []).map((r) => roleName[r] || r).join('、'))

function go(e: any) {
  router.push({ name: e.key })
  if (window.innerWidth < 768) collapsed.value = true
}

async function logout() {
  await session.logout()
  router.push({ name: 'login' })
}
</script>

<template>
  <a-layout class="shell">
    <a-layout-sider :width="216" class="sider" breakpoint="md" :collapsed-width="0" v-model:collapsed="collapsed">
      <div class="brand">
        <div class="name">EdgeTraffic OS</div>
        <div class="sub">厂商授权后台</div>
      </div>
      <a-menu theme="dark" mode="inline" :selected-keys="selected" :items="items" @click="go" />
      <div class="foot">只保存公钥 · 私钥在离线签名机</div>
    </a-layout-sider>
    <a-layout>
      <a-layout-header class="topbar">
        <div class="title">{{ route.meta.title }}</div>
        <a-dropdown :menu="{ items: [{ key: 'logout', icon: () => h(LogoutOutlined), label: '退出登录' }], onClick: logout }">
          <a-button type="text">{{ session.me?.display_name || session.me?.username }}（{{ roles }}）</a-button>
        </a-dropdown>
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
  padding: 20px 20px 18px;
  color: #fff;
}
.brand .name {
  font-weight: 600;
  font-size: 15px;
}
.brand .sub {
  color: #9fb5b9;
  font-size: 12px;
  margin-top: 2px;
}
.foot {
  margin-top: auto;
  padding: 14px 20px;
  color: #7f9ba0;
  font-size: 12px;
}
.sider :deep(.ant-layout-sider-zero-width-trigger) {
  top: 12px;
}
@media (max-width: 767px) {
  .sider {
    position: fixed;
    z-index: 20;
  }
  .topbar {
    padding-left: 56px !important;
  }
  :deep(.page) {
    padding: 16px 16px 40px;
  }
  :deep(.page-head) {
    flex-wrap: wrap;
    align-items: flex-start;
  }
}
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 24px;
  border-bottom: 1px solid #e1e7e6;
}
.title {
  font-weight: 600;
  color: #16323a;
}
</style>
