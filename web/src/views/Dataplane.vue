<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg, http } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const groups = ref<any[]>([])
const gid = ref(0)
const nodes = ref<any[]>([])
const packages = ref<any[]>([])
const upgrades = ref<any[]>([])
const ver = ref<string>()
const variant = ref('standard')
const starting = ref(false)

async function load() {
  if (!groups.value.length) {
    groups.value = await api.get('/node-groups')
    if (!gid.value && groups.value.length) gid.value = groups.value[0].id
  }
  const [n, p, u] = await Promise.all([api.get('/nodes'), session.can('operator') ? api.get('/packages') : Promise.resolve([]), api.get('/upgrades', { group_id: gid.value })])
  nodes.value = n.filter((x: any) => x.group_id === gid.value && x.status !== 'revoked')
  packages.value = p
  upgrades.value = u
}
let timer: number | undefined
onMounted(async () => {
  await load()
  timer = window.setInterval(() => {
    if (upgrades.value.some((u) => u.status === 'running')) load()
  }, 1500)
})
onUnmounted(() => clearInterval(timer))
watch(gid, load)

const agentVer = ref<string>()
const agentVersions = computed(() => [...new Set(packages.value.filter((p) => p.name === 'syapp-agent').map((p) => p.version))])
async function startAgent() {
  try {
    await api.post(`/node-groups/${gid.value}/agent-upgrade`, { version: agentVer.value })
    message.success('已开始分批升级 agent（只重启 agent 进程，不影响流量）')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
const versions = computed(() => [...new Set(packages.value.filter((p) => p.name === 'syapp' && p.variant === variant.value).map((p) => p.version))])
const variantName: Record<string, string> = { standard: '标准版', gmssl: '国密版', lua: 'Lua 附加包', agent: 'agent' }
const nodeStatus: Record<string, { t: string; c: string }> = {
  pending: { t: '等待', c: 'default' },
  upgrading: { t: '升级中', c: 'processing' },
  upgraded: { t: '已升级', c: 'green' },
  rolled_back: { t: '已自动回退', c: 'orange' },
  failed: { t: '失败', c: 'red' },
  skipped: { t: '已停止', c: 'default' },
  offline: { t: '离线', c: 'default' },
}

function pickFiles() {
  const el = document.createElement('input')
  el.type = 'file'
  el.multiple = true
  el.accept = '.gz,.tgz,.sig'
  el.onchange = async () => {
    const fs = Array.from(el.files || [])
    const pkg = fs.find((f) => !f.name.endsWith('.sig'))
    const sig = fs.find((f) => f.name.endsWith('.sig'))
    if (!pkg || !sig) {
      message.warning('请同时选择升级包（.tar.gz）和对应的签名文件（.sig）')
      return
    }
    const fd = new FormData()
    fd.append('package', pkg)
    fd.append('signature', sig)
    try {
      const r = (await http.post('/packages', fd)).data
      message.success(`已导入 ${r.name} ${r.version}（${r.arch}，${variantName[r.variant]}），签名校验通过`)
      await load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  el.click()
}
async function start() {
  starting.value = true
  try {
    await api.post(`/node-groups/${gid.value}/upgrade`, { version: ver.value, variant: variant.value })
    message.success('已开始分批升级：先 1 台，再 10%，再全量')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    starting.value = false
  }
}
const running = computed(() => upgrades.value.some((u) => u.status === 'running'))
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>数据面版本</h1>
        <p>导入厂商签名的 syapp 升级包，按节点组分批热升级（二进制热替换，连接不中断）。任一节点探活失败会自动回到旧版本并停止后续节点；回退即用上一版本再升级一次。</p>
      </div>
      <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" />
    </div>

    <section class="panel">
      <h2>节点版本</h2>
      <a-table :data-source="nodes" row-key="id" :pagination="false" size="small"
        :columns="[{ title: '节点', dataIndex: 'name' }, { title: '架构', dataIndex: 'arch' }, { title: 'syapp', key: 'v' }, { title: 'agent', dataIndex: 'agent_version' }, { title: '状态', key: 'st' }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'v'">{{ (record.syapp_version || '').replace('syapp version: ', '') || '—' }}</template>
          <template v-else-if="column.key === 'st'"><a-badge :status="record.status === 'online' ? 'success' : 'default'" :text="record.status === 'online' ? '在线' : '离线'" /></template>
        </template>
      </a-table>
      <div class="up" v-if="session.can('operator')">
        <a-select v-model:value="variant" style="width: 120px" :options="Object.entries(variantName).map(([value, label]) => ({ value, label }))" />
        <a-select v-model:value="ver" style="width: 200px" placeholder="目标版本" :options="versions.map((v) => ({ value: v, label: v }))" />
        <a-popconfirm :title="`把 ${groups.find((g) => g.id === gid)?.name} 的 ${nodes.length} 台节点升级到 ${ver}？`" @confirm="start">
          <a-button type="primary" :loading="starting" :disabled="!ver || running || !session.writable">开始分批升级</a-button>
        </a-popconfirm>
        <span class="gap" />
        <a-select v-model:value="agentVer" style="width: 200px" placeholder="agent 目标版本" :options="agentVersions.map((v) => ({ value: v, label: 'agent ' + v }))" />
        <a-popconfirm :title="`把 ${nodes.length} 台节点的 agent 升级到 ${agentVer}？syapp 不受影响`" @confirm="startAgent">
          <a-button :disabled="!agentVer || running || !session.writable">升级 agent</a-button>
        </a-popconfirm>
      </div>
    </section>

    <section class="panel" v-for="u in upgrades.slice(0, 5)" :key="u.id">
      <div class="phead">
        <h2>{{ u.variant === 'agent' ? 'agent 升级' : '升级' }} #{{ u.id }} → {{ u.version }} <span class="sub">{{ variantName[u.variant] }}</span></h2>
        <span class="sub">
          <a-tag :color="u.status === 'done' ? 'green' : u.status === 'running' ? 'processing' : 'red'">{{ { done: '完成', running: '进行中', failed: '已停止' }[u.status as string] }}</a-tag>
          {{ u.created_by }} · {{ dayjs(u.created_at).format('MM-DD HH:mm') }}
        </span>
      </div>
      <a-table :data-source="u.nodes" row-key="node_id" :pagination="false" size="small"
        :columns="[{ title: '批次', dataIndex: 'batch', width: 70 }, { title: '节点', dataIndex: 'name' }, { title: '结果', key: 'st', width: 130 }, { title: '说明', dataIndex: 'message' }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'st'"><a-tag :color="nodeStatus[record.status]?.c">{{ nodeStatus[record.status]?.t }}</a-tag></template>
        </template>
      </a-table>
    </section>

    <section class="panel" v-if="session.can('operator')">
      <div class="phead">
        <h2>版本库</h2>
        <a-button v-if="session.can('admin')" size="small" :disabled="!session.writable" @click="pickFiles">导入升级包</a-button>
      </div>
      <a-table :data-source="packages" row-key="id" :pagination="false" size="small"
        :columns="[{ title: '包', key: 'n' }, { title: '架构', dataIndex: 'arch' }, { title: '变体', key: 'var' }, { title: 'SHA-256', key: 'sha' }, { title: '签名密钥', dataIndex: 'kid' }, { title: '导入', key: 'by' }]">
        <template #emptyText><a-empty description="版本库为空：导入厂商发布的 syapp-<版本>-<架构>.tar.gz 与 .sig" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'n'">{{ record.name }} {{ record.version }}</template>
          <template v-else-if="column.key === 'var'">{{ variantName[record.variant] }}</template>
          <template v-else-if="column.key === 'sha'"><span class="code">{{ record.sha256.slice(0, 16) }}…</span></template>
          <template v-else-if="column.key === 'by'">{{ record.uploaded_by }} · {{ dayjs(record.created_at).format('MM-DD HH:mm') }}</template>
        </template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.gap {
  width: 16px;
}
.up {
  display: flex;
  gap: 10px;
  flex-wrap: wrap;
  margin-top: 14px;
}
.phead {
  display: flex;
  justify-content: space-between;
  align-items: baseline;
  flex-wrap: wrap;
  gap: 8px;
  margin-bottom: 8px;
}
.phead h2 {
  margin: 0;
}
.sub {
  font-size: 12px;
  color: #6b7c80;
}
</style>
