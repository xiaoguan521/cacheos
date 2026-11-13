<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message } from 'antdv-next'
import { api, errMsg, http } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const hosts = ref<any[]>([])
const loading = ref(false)
const enrollOpen = ref(false)
const commandOpen = ref(false)
const command = ref<any>(null)
const form = reactive({ zone: '' })
const agentPackages = ref<any[]>([])
const upgradeOpen = ref(false)
const upgradeHost = ref<any>(null)
const upgradeTarget = ref<string | undefined>(undefined)
const upgrading = ref(false)

async function load() {
  loading.value = true
  try {
    hosts.value = await api.get('/hosts')
    // 版本库列表仅管理员可读；其他角色无法选择 agent 版本，升级入口不显示。
    agentPackages.value = session.can('admin') ? await api.get('/engine-packages').then((ps: any[]) => ps.filter((p) => p.name === 'sycache-agent')).catch(() => []) : []
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

async function enroll() {
  try {
    command.value = await api.post('/hosts/enroll', form)
    enrollOpen.value = false
    commandOpen.value = true
    form.zone = ''
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function copyCommand() {
  if (!command.value?.command) return
  await navigator.clipboard.writeText(command.value.command)
  message.success('命令已复制')
}

async function toggleMaintenance(host: any) {
  try {
    await api.put(`/hosts/${host.id}`, { status: host.status === 'maintenance' ? 'online' : 'maintenance' })
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function installedVersions(host: any): string[] {
  return String(host.engine_versions || '').split(',').map((v) => v.trim()).filter(Boolean)
}

async function switchVersion(host: any, version: string) {
  if (!version || version === host.engine_current) return
  if (!window.confirm(`将 ${host.name} 的 current 切换到 ${version}；实例会在后续滚动重启时使用新版本，继续？`)) return
  try {
    await api.post(`/hosts/${host.id}/switch-version`, { version })
    message.success(`主机 ${host.name} 已切换到 ${version}`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

// 与控制台 / agent 的版本规则一致：N.N.N，再比较 "-" 后的数字。
function agentVersionParts(v: string): number[] {
  const [main, rel] = String(v).split('-', 2)
  const nums = main.split('.').map((x) => Number.parseInt(x, 10) || 0)
  return [nums[0] || 0, nums[1] || 0, nums[2] || 0, Number.parseInt(rel ?? '', 10) || 0]
}

function agentVersionNewer(target: string, current: string): boolean {
  const a = agentVersionParts(target)
  const b = agentVersionParts(current)
  for (let i = 0; i < a.length; i++) {
    if (a[i] !== b[i]) return a[i] > b[i]
  }
  return false
}

function agentUpgradeCandidates(host: any): string[] {
  if (!host.agent_version) return []
  return agentPackages.value
    .filter((p) => p.arch === host.arch && agentVersionNewer(p.version, host.agent_version))
    .map((p) => p.version)
    .sort((a, b) => (agentVersionNewer(a, b) ? -1 : agentVersionNewer(b, a) ? 1 : 0))
}

function openUpgrade(host: any) {
  upgradeHost.value = host
  upgradeTarget.value = agentUpgradeCandidates(host)[0]
  upgradeOpen.value = true
}

async function upgradeAgent() {
  const host = upgradeHost.value
  const version = upgradeTarget.value
  if (!host || !version) {
    message.error('请选择目标版本')
    return
  }
  if (!window.confirm(`将 ${host.name} 的 agent 从 ${host.agent_version} 升级到 ${version}；agent 验签后自行重启，实例不受影响，继续？`)) return
  upgrading.value = true
  try {
    // 升级包由 agent 从控制台分片下载，耗时可能超过默认 30 秒的请求超时。
    await http.post(`/hosts/${host.id}/upgrade-agent`, { version }, { timeout: 180000 })
    message.success(`主机 ${host.name} 的 agent 已升级到 ${version}，重启后生效`)
    upgradeOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    upgrading.value = false
  }
}

async function revoke(host: any) {
  try {
    await api.post(`/hosts/${host.id}/revoke`)
    message.success('主机已吊销')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function bytes(v: number) {
  if (!v) return '—'
  if (v >= 1024 ** 3) return `${(v / 1024 ** 3).toFixed(1)} GiB`
  return `${Math.round(v / 1024 ** 2)} MiB`
}

function seenAt(value?: string) {
  return value ? new Date(value).toLocaleString() : '从未'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>主机</h1>
        <p>管理 agent 接入的主机。实例只会分配到在线或维护状态的主机。</p>
      </div>
      <a-space>
        <a-button @click="load">刷新</a-button>
        <a-button type="primary" :disabled="!session.can('operator') || !session.writable" @click="enrollOpen = true">生成接入命令</a-button>
      </a-space>
    </div>

    <section class="panel">
      <a-table :data-source="hosts" row-key="id" :loading="loading" :pagination="{ pageSize: 20 }"
        :columns="[{ title: '主机', key: 'host' }, { title: '状态', key: 'status', width: 110 }, { title: '区域', dataIndex: 'zone', width: 120 }, { title: '系统', key: 'system' }, { title: '版本', key: 'version', width: 190 }, { title: '资源', key: 'resource', width: 190 }, { title: '实例', key: 'instances', width: 100 }, { title: '最近心跳', key: 'seen', width: 170 }, { title: '', key: 'actions', width: 210 }]">
        <template #emptyText><a-empty description="还没有接入主机" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'host'">
            <div class="host-name">{{ record.name }}</div>
            <div class="sub code">{{ record.hostname || record.address || '—' }}</div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="record.online ? 'green' : record.status === 'maintenance' ? 'gold' : 'default'">{{ record.online ? '在线' : record.status === 'maintenance' ? '维护' : '离线' }}</a-tag>
            <a-tag v-if="record.warnings?.length" color="orange">调优提醒</a-tag>
          </template>
          <template v-else-if="column.key === 'system'">
            <div>{{ record.os_release || '—' }}</div><div class="sub">{{ record.arch }}</div>
          </template>
          <template v-else-if="column.key === 'version'">
            <a-select size="small" :value="record.engine_current || undefined" :disabled="!session.can('operator') || !session.writable || !record.online || !installedVersions(record).length"
              placeholder="未上报" style="width: 170px" :options="installedVersions(record).map((v) => ({ value: v, label: v }))"
              @change="(v: string) => switchVersion(record, v)" />
            <div class="sub">已安装 {{ installedVersions(record).length }} 个</div>
            <div class="sub">扩展模块 {{ record.modules ? record.modules.split(',').join(' · ') : '未安装' }}</div>
            <div class="sub">agent {{ record.agent_version || '未上报' }}</div>
            <a-button v-if="session.can('operator') && session.writable && record.online && agentUpgradeCandidates(record).length" size="small" type="link" style="padding: 0" @click="openUpgrade(record)">升级 agent</a-button>
          </template>
          <template v-else-if="column.key === 'resource'">
            <div>CPU {{ Math.round(record.cpu_percent || 0) }}% · {{ record.cpu_cores || 0 }} 核</div>
            <div class="sub">内存 {{ bytes(record.mem_used_bytes) }} / {{ bytes(record.mem_total_bytes) }}</div>
          </template>
          <template v-else-if="column.key === 'instances'">{{ record.instances?.length ?? '—' }}</template>
          <template v-else-if="column.key === 'seen'"><span class="num">{{ seenAt(record.last_seen_at) }}</span></template>
          <template v-else-if="column.key === 'actions' && session.can('operator')">
            <a-space :size="4">
              <a-button size="small" @click="toggleMaintenance(record)">{{ record.status === 'maintenance' ? '恢复分配' : '设为维护' }}</a-button>
              <a-popconfirm v-if="session.can('admin')" title="吊销后控制台不再管理此主机，确定？" @confirm="revoke(record)"><a-button size="small" danger>吊销</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="enrollOpen" title="生成主机接入命令" ok-text="生成" @ok="enroll">
      <a-form layout="vertical">
        <a-form-item label="区域 / 可用区" extra="用于实例分配时的反亲和；可留空。">
          <a-input v-model:value="form.zone" placeholder="例如 cn-hz-a" />
        </a-form-item>
      </a-form>
      <a-alert type="info" show-icon message="接入令牌只在命令中返回，请通过安全渠道传给目标主机。" />
    </a-modal>

    <a-modal v-model:open="upgradeOpen" title="升级主机 agent" ok-text="升级" :confirm-loading="upgrading" :ok-button-props="{ disabled: !upgradeTarget }" @ok="upgradeAgent">
      <a-form layout="vertical">
        <a-form-item label="主机">{{ upgradeHost?.name }}（当前 agent {{ upgradeHost?.agent_version }}）</a-form-item>
        <a-form-item label="目标版本" extra="只列出版本库中架构相同且高于当前版本的 agent 包；不支持降级。">
          <a-select v-model:value="upgradeTarget" :options="agentUpgradeCandidates(upgradeHost || {}).map((v) => ({ value: v, label: v }))" />
        </a-form-item>
      </a-form>
      <a-alert type="info" show-icon message="agent 升级只重启 agent 进程，数据面实例不受影响。升级包由 agent 再次验签后才会安装。" />
    </a-modal>

    <a-modal v-model:open="commandOpen" title="在目标主机执行以下命令" :footer="null" width="720px">
      <a-alert type="warning" show-icon message="命令包含一次性令牌；执行后令牌立即失效。" style="margin-bottom: 12px" />
      <pre class="command">{{ command?.command }}</pre>
      <a-space>
        <a-button type="primary" @click="copyCommand">复制命令</a-button>
        <span class="sub">令牌有效期至 {{ command?.expires_at ? new Date(command.expires_at).toLocaleString() : '—' }}</span>
      </a-space>
    </a-modal>
  </div>
</template>

<style scoped>
.host-name { font-weight: 600; color: #16323a; }
.sub { color: #7b8b8e; font-size: 12px; }
.command { padding: 14px; background: #172326; color: #d8e8e7; border-radius: 6px; white-space: pre-wrap; word-break: break-all; font-size: 12px; line-height: 1.6; }
</style>
