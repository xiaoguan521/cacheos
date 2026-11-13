<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { use } from 'echarts/core'
import { HeatmapChart } from 'echarts/charts'
import { GridComponent, TooltipComponent, VisualMapComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import VChart from 'vue-echarts'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'
import DiffView from '../components/DiffView.vue'

use([HeatmapChart, GridComponent, TooltipComponent, VisualMapComponent, CanvasRenderer])

const route = useRoute()
const router = useRouter()
const session = useSession()
const unit = ref<any>(null)
const metrics = ref<any>({ instances: {} })
const revisions = ref<any[]>([])
const error = ref('')
const paramsText = ref('{}')
const paramNote = ref('')
const applyRestart = ref(false)
const accountOpen = ref(false)
const accountForm = reactive({ name: '', password: '', key_patterns: '*', categories: '+@read,+@write,-@dangerous', enabled: true })
const generatedPassword = ref('')
const upgradeOpen = ref(false)
const upgradeVersion = ref('')
const upgradeSwitchover = ref(true)
const scaleOpen = ref(false)
const scaleForm = reactive({ replicas: 1, shards: 3 })
const sentinelOpen = ref(false)
const sentinelForm = reactive({ operation: 'set', down_after_ms: '30000', failover_timeout: '60000', parallel_syncs: '1' })
const reshardForm = reactive({ source_instance_id: '', target_instance_id: '', slots: '0-100', batch: 100 })
const rollbackOpen = ref(false)
const rollbackTarget = ref<any>(null)
const rollbackPreview = ref<any>(null)
const rollbackDiff = ref('')
const rollbackNote = ref('')
const rollbackRestart = ref(false)
const rebalanceOpen = ref(false)
const rebalanceLoading = ref(false)
const rebalancePlan = ref<any>(null)
const canRebalance = computed(() => session.can('releaser') && session.writable && !!session.license?.features?.includes('online_reshard'))
const rebalanceActionable = computed(() => !!rebalancePlan.value && !rebalancePlan.value.balanced && (rebalancePlan.value.steps || []).length > 0)

const id = computed(() => String(route.params.id))
const metricRows = computed(() => Object.entries(metrics.value?.instances || {}).flatMap(([instanceID, points]: [string, any]) => (points || []).slice(-20).map((p: any) => ({ instanceID, ...p }))))
const slotRows = computed(() => (unit.value?.instances || [])
  .filter((x: any) => x.kind === 'server' && x.cluster_slots)
  .map((x: any) => ({
    label: `${x.host || 'host'}:${x.port} · ${x.role || x.desired_role || '—'}`,
    ranges: String(x.cluster_slots).split(/[\s,]+/).flatMap((raw: string) => {
      const [startRaw, endRaw = startRaw] = raw.split('-')
      const start = Number(startRaw)
      const end = Number(endRaw)
      return Number.isInteger(start) && Number.isInteger(end) && start >= 0 && end <= 16383 && start <= end ? [{ start, end }] : []
    }),
  }))
  .filter((x: any) => x.ranges.length > 0))
const slotChartOption = computed(() => {
  const bucketSize = 64
  const bucketCount = 16384 / bucketSize
  const data: number[][] = []
  slotRows.value.forEach((row: any, rowIndex: number) => row.ranges.forEach((range: any) => {
    for (let bucket = Math.floor(range.start / bucketSize); bucket <= Math.floor(range.end / bucketSize); bucket++) data.push([bucket, rowIndex, 1])
  }))
  return {
    animation: false,
    grid: { left: 190, right: 24, top: 18, bottom: 38 },
    tooltip: {
      formatter: (params: any) => {
        const bucket = Number(params.value?.[0] || 0)
        const start = bucket * bucketSize
        const end = Math.min(16383, start + bucketSize - 1)
        return `${slotRows.value[Number(params.value?.[1] || 0)]?.label || '实例'}<br/>槽 ${start}-${end}`
      },
    },
    visualMap: { show: false, min: 0, max: 1, inRange: { color: ['#eef5f4', '#0e7c86'] } },
    xAxis: { type: 'category', data: Array.from({ length: bucketCount }, (_, i) => String(i * bucketSize)), axisLabel: { interval: 63 } },
    yAxis: { type: 'category', data: slotRows.value.map((x: any) => x.label), inverse: true },
    series: [{ type: 'heatmap', data }],
  }
})
const statusColor: Record<string, string> = { running: 'green', creating: 'gold', changing: 'gold', degraded: 'orange', stopped: 'default', failed: 'red', deleting: 'volcano' }

async function load() {
  try {
    const [u, m, r] = await Promise.all([api.get(`/units/${id.value}`), api.get(`/units/${id.value}/metrics`, { minutes: 60 }), api.get(`/units/${id.value}/revisions`)]);
    unit.value = u
    metrics.value = m
    revisions.value = r
    paramsText.value = JSON.stringify(u.params || {}, null, 2)
    const masters = (u.instances || []).filter((x: any) => x.kind === 'server' && (x.role === 'master' || x.desired_role === 'master'))
    if (!reshardForm.source_instance_id && masters[0]) reshardForm.source_instance_id = String(masters[0].id)
    if (!reshardForm.target_instance_id && masters[1]) reshardForm.target_instance_id = String(masters[1].id)
    error.value = ''
  } catch (e) {
    error.value = errMsg(e)
  }
}

async function action(name: string) {
  try {
    const r = await api.post(`/units/${id.value}/actions/${name}`, { switchover: name === 'restart' })
    message.success(`操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function failover() {
  try {
    const r = await api.post(`/units/${id.value}/failover`)
    message.success(`故障切换操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function upgradeUnit() {
  const version = upgradeVersion.value.trim()
  if (!/^\d+\.\d+\.\d+(-\d+)?$/.test(version)) {
    message.error('版本号格式应为 x.y.z 或 x.y.z-n')
    return
  }
  if (!window.confirm(`将把 ${unit.value.name} 升级到 ${version}，并按从节点到主节点滚动重启，确定继续？`)) return
  try {
    const r = await api.post(`/units/${id.value}/upgrade`, { version, switchover: upgradeSwitchover.value })
    message.success(`升级操作 #${r.operation?.id || '—'} 已提交`)
    upgradeOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function openScale() {
  scaleForm.replicas = Number(unit.value?.replicas || 1)
  scaleForm.shards = Number(unit.value?.shards || 3)
  scaleOpen.value = true
}

async function scaleUnit() {
  const payload: Record<string, number> = { replicas: Number(scaleForm.replicas) }
  if (unit.value?.mode === 'cluster') payload.shards = Number(scaleForm.shards)
  if (payload.replicas === Number(unit.value?.replicas || 0) && (!payload.shards || payload.shards === Number(unit.value?.shards || 0))) {
    message.info('目标规格没有变化')
    return
  }
  if (!window.confirm(`将把 ${unit.value.name} 调整为 ${payload.shards ? `${payload.shards} 个分片、` : ''}${payload.replicas} 个从节点/分片，确定继续？`)) return
  try {
    const r = await api.post(`/units/${id.value}/scale`, payload)
    message.success(`扩缩容操作 #${r.operation?.id || '—'} 已提交`)
    scaleOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function openSentinel() {
  sentinelOpen.value = true
}

async function sentinelOperation() {
  const options: Record<string, string> = {}
  if (sentinelForm.operation === 'set') {
    for (const [key, value] of Object.entries({
      'down-after-milliseconds': sentinelForm.down_after_ms,
      'failover-timeout': sentinelForm.failover_timeout,
      'parallel-syncs': sentinelForm.parallel_syncs,
    })) {
      if (String(value).trim()) options[key] = String(value).trim()
    }
  }
  try {
    const r = await api.post(`/units/${id.value}/sentinel`, { operation: sentinelForm.operation, options })
    message.success(`哨兵操作 #${r.operation?.id || '—'} 已提交`)
    sentinelOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function applyParams() {
  let next: Record<string, string>
  try {
    next = JSON.parse(paramsText.value)
    if (!next || Array.isArray(next) || typeof next !== 'object') throw new Error('参数必须是 JSON 对象')
  } catch (e) {
    message.error(`参数 JSON 无效：${e instanceof Error ? e.message : String(e)}`)
    return
  }
  if (!paramNote.value.trim()) {
    message.error('请填写变更说明')
    return
  }
  const old = unit.value?.params || {}
  const reset = Object.keys(old).filter((key) => !(key in next))
  try {
    const r = await api.post(`/units/${id.value}/params`, { set: next, reset, note: paramNote.value, restart: applyRestart.value, switchover: applyRestart.value })
    message.success(`参数变更操作 #${r.operation?.id || '—'} 已提交`)
    paramNote.value = ''
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

// 回滚前先用参数预览接口计算"当前版本 → 目标历史版本"的差异（删除的键按默认值计入差异，与服务端一致）
async function openRollback(revision: any) {
  const current = unit.value?.params || {}
  const target = revision.params || {}
  const reset = Object.keys(current).filter((key) => !(key in target))
  try {
    const pv = await api.post(`/units/${id.value}/params/preview`, { set: target, reset })
    rollbackPreview.value = pv
    rollbackDiff.value = Object.keys(pv.changed || {})
      .sort()
      .flatMap((key) => [`-${key}=${pv.old?.[key] ?? ''}`, `+${key}=${pv.changed[key]}`])
      .join('\n')
    rollbackTarget.value = revision
    rollbackNote.value = `回滚到 ${revision.id}`
    rollbackRestart.value = false
    rollbackOpen.value = true
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function submitRollback() {
  if (!rollbackTarget.value || !rollbackPreview.value) return
  if (!rollbackNote.value.trim()) {
    message.error('请填写变更说明')
    return
  }
  try {
    // 父版本取预览时的当前版本：确认的差异与提交时的基线一致，否则服务端返回 REVISION_CONFLICT
    const r = await api.post(`/units/${id.value}/revisions/${rollbackTarget.value.id}/rollback`, {
      parent_revision_id: rollbackPreview.value.base_revision,
      note: rollbackNote.value,
      restart: rollbackRestart.value,
      switchover: rollbackRestart.value,
    })
    message.success(`回滚操作 #${r.operation?.id || '—'} 已提交`)
    rollbackOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function openAccount() {
  Object.assign(accountForm, { name: '', password: '', key_patterns: '*', categories: '+@read,+@write,-@dangerous', enabled: true })
  generatedPassword.value = ''
  accountOpen.value = true
}

async function saveAccount() {
  try {
    const r = await api.post(`/units/${id.value}/accounts`, accountForm)
    generatedPassword.value = r.generated_password || ''
    message.success('业务账号已保存')
    await load()
    if (!generatedPassword.value) accountOpen.value = false
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function deleteAccount(account: any) {
  try {
    await api.del(`/units/${id.value}/accounts/${encodeURIComponent(account.name)}`)
    message.success('业务账号已删除')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function parseSlotRanges(raw: string) {
  return raw.split(',').map((part) => {
    if (!part.trim()) throw new Error('槽范围不能为空')
    const [a, b = a] = part.trim().split('-').map(Number)
    if (!Number.isInteger(a) || !Number.isInteger(b) || a < 0 || b > 16383 || a > b) throw new Error('槽范围必须是 0-16383 内的 start-end，可用逗号分隔')
    return { start: a, end: b }
  })
}

async function reshard() {
  try {
    const slots = parseSlotRanges(reshardForm.slots)
    const r = await api.post(`/units/${id.value}/reshard`, { source_instance_id: Number(reshardForm.source_instance_id), target_instance_id: Number(reshardForm.target_instance_id), slots, batch: Number(reshardForm.batch) })
    message.success(`迁槽操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(e instanceof Error ? e.message : errMsg(e))
  }
}

async function openRebalance() {
  rebalanceOpen.value = true
  await loadRebalancePlan()
}

// dry_run 只返回基于已保存状态的计划，服务端不会下发任何指令。
async function loadRebalancePlan() {
  rebalanceLoading.value = true
  rebalancePlan.value = null
  try {
    const r = await api.post(`/units/${id.value}/rebalance`, { dry_run: true })
    rebalancePlan.value = r.plan
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    rebalanceLoading.value = false
  }
}

async function rebalance() {
  try {
    const r = await api.post(`/units/${id.value}/rebalance`, { dry_run: false })
    rebalanceOpen.value = false
    message.success(`再平衡操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function onRebalanceOk() {
  if (rebalanceActionable.value) return rebalance()
  rebalanceOpen.value = false
}

function slotText(slots: { start: number; end: number }[]) {
  return slots.map((x) => (x.start === x.end ? `${x.start}` : `${x.start}-${x.end}`)).join(', ')
}

function bytes(v: number) {
  if (!v) return '0 B'
  if (v >= 1024 ** 3) return `${(v / 1024 ** 3).toFixed(1)} GiB`
  if (v >= 1024 ** 2) return `${Math.round(v / 1024 ** 2)} MiB`
  return `${Math.round(v / 1024)} KiB`
}

onMounted(() => {
  load()
})
let timer: number | undefined
onMounted(() => { timer = window.setInterval(load, 10000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="page" v-if="unit">
    <div class="page-head">
      <div><a @click="router.push({ name: 'units' })">← 部署单元</a><h1>{{ unit.name }}</h1><p>{{ unit.description || '—' }} · {{ unit.mode }} · revision {{ unit.revision_id || '—' }}</p></div>
      <a-space>
        <a-button @click="load">刷新</a-button>
        <a-button @click="router.push({ name: 'diagnose', params: { id } })">诊断</a-button>
        <a-button @click="router.push({ name: 'backups', params: { id } })">备份</a-button>
        <a-button v-if="unit.mode !== 'standalone' || session.can('operator')" @click="router.push({ name: 'migrations', params: { id } })">迁移</a-button>
        <a-button @click="router.push({ name: 'keys', params: { id } })">Key 浏览器</a-button>
        <a-button v-if="session.cliExec" @click="router.push({ name: 'cli', params: { id } })">Web 命令行</a-button>
        <a-button :disabled="!session.can('operator')" @click="action(unit.status === 'stopped' ? 'start' : 'stop')">{{ unit.status === 'stopped' ? '启动' : '停止' }}</a-button>
        <a-button :disabled="!session.can('operator')" @click="action('restart')">重启</a-button>
        <a-button v-if="unit.mode !== 'standalone'" :disabled="!session.can('operator')" @click="failover">故障切换</a-button>
        <a-button v-if="unit.mode !== 'standalone'" :disabled="!session.can('releaser') || !session.writable" @click="openScale">扩缩容</a-button>
        <a-button v-if="unit.mode === 'sentinel'" :disabled="!session.can('releaser') || !session.writable" @click="openSentinel">哨兵管理</a-button>
        <a-button type="primary" :disabled="!session.can('releaser') || !session.writable" @click="upgradeVersion = ''; upgradeOpen = true">升级引擎</a-button>
      </a-space>
    </div>

    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />
    <div class="summary">
      <div><span>状态</span><strong><a-tag :color="statusColor[unit.status] || 'default'">{{ unit.status }}</a-tag></strong></div>
      <div><span>健康度</span><strong>{{ unit.health }}</strong></div>
      <div><span>端点</span><strong class="code">{{ unit.endpoint || '未就绪' }}</strong></div>
      <div><span>数据面版本</span><strong class="code">{{ unit.engine_version || '—' }}</strong></div>
      <div><span>扩展数据类型</span><strong>{{ unit.modules ? unit.modules.split(',').join(' · ') : '无' }}</strong></div>
      <div><span>规格</span><strong>{{ unit.memory_mb }} MiB · {{ unit.instances?.length || 0 }} 个实例</strong></div>
      <div><span>当前用量</span><strong>{{ bytes(unit.usage?.used_memory || 0) }} · {{ unit.usage?.ops_per_sec || 0 }} ops/s</strong></div>
    </div>

    <section class="panel">
      <h2>实例</h2>
      <a-table :data-source="unit.instances" row-key="id" size="small" :pagination="false" :columns="[{ title: '地址', key: 'address' }, { title: '分片', dataIndex: 'shard', width: 80 }, { title: '期望角色', dataIndex: 'desired_role', width: 110 }, { title: '实际角色', dataIndex: 'role', width: 100 }, { title: '状态', dataIndex: 'state', width: 120 }, { title: '资源', key: 'resource', width: 220 }, { title: '参数版本', dataIndex: 'applied_revision', width: 120 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'address'"><span class="code">{{ record.address || record.host }}:{{ record.port }}</span><div class="sub">{{ record.host }}</div></template>
          <template v-else-if="column.key === 'resource'">{{ bytes(record.used_memory) }} · {{ record.ops_per_sec || 0 }} ops/s · {{ record.connected_clients || 0 }} clients</template>
        </template>
      </a-table>
    </section>

    <section v-if="unit.mode === 'cluster'" class="panel">
      <div class="phead"><h2>槽位分布</h2><span class="muted">16384 槽 · 每格 64 槽</span></div>
      <v-chart v-if="slotRows.length" class="slot-chart" :option="slotChartOption" autoresize />
      <a-empty v-else description="等待集群槽位上报" />
    </section>

    <section v-if="unit.mode === 'cluster'" class="panel">
      <div class="phead"><h2>在线迁槽</h2><span><a-button size="small" :disabled="!canRebalance" @click="openRebalance">再平衡</a-button> <a-button type="primary" size="small" :disabled="!session.can('releaser') || !session.writable" @click="reshard">提交迁槽计划</a-button></span></div>
      <div class="param-foot">
        <a-select v-model:value="reshardForm.source_instance_id" placeholder="源主节点" style="min-width: 190px" :options="unit.instances.filter((x: any) => x.kind === 'server' && (x.role === 'master' || x.desired_role === 'master')).map((x: any) => ({ value: String(x.id), label: `${x.host}:${x.port} · ${x.shard}` }))" />
        <a-select v-model:value="reshardForm.target_instance_id" placeholder="目标主节点" style="min-width: 190px" :options="unit.instances.filter((x: any) => x.kind === 'server' && (x.role === 'master' || x.desired_role === 'master')).map((x: any) => ({ value: String(x.id), label: `${x.host}:${x.port} · ${x.shard}` }))" />
        <a-input v-model:value="reshardForm.slots" style="width: 220px" placeholder="槽范围，如 0-100,200-250" />
        <a-input-number v-model:value="reshardForm.batch" :min="1" :max="1000" />
      </div>
      <p class="sub">控制台按槽、按批执行 IMPORTING → MIGRATING → MIGRATE → NODE；操作超时会暂停，避免自动继续。</p>
    </section>

    <div class="two-cols">
      <section class="panel">
        <div class="phead"><h2>参数</h2><a-button type="primary" size="small" :disabled="!session.can('releaser') || !session.writable" @click="applyParams">提交参数变更</a-button></div>
        <a-textarea v-model:value="paramsText" :rows="14" class="code" />
        <div class="param-foot"><a-input v-model:value="paramNote" placeholder="变更说明（必填）" /><a-checkbox v-model:checked="applyRestart">需要时滚动重启</a-checkbox></div>
        <p class="sub">服务端会校验参数；删除 JSON 中的键会恢复为默认值。变更提交后可在操作记录中查看步骤。</p>
      </section>
      <section class="panel">
        <h2>参数版本</h2>
        <a-timeline>
          <a-timeline-item v-for="revision in revisions" :key="revision.id" color="blue"><b>{{ revision.id }}</b> · {{ revision.note || '—' }}<div class="sub">{{ revision.created_by }} · {{ dayjs(revision.created_at).format('YYYY-MM-DD HH:mm') }}</div><a-button v-if="revision.id !== unit.revision_id" type="link" size="small" :disabled="!session.can('releaser') || !session.writable" @click="openRollback(revision)">回滚到此版本</a-button></a-timeline-item>
        </a-timeline>
        <a-empty v-if="!revisions.length" description="还没有版本记录" />
      </section>
    </div>

    <section class="panel">
      <h2>最近监控</h2>
      <a-table :data-source="metricRows" row-key="minute" size="small" :pagination="{ pageSize: 10 }" :columns="[{ title: '实例', dataIndex: 'instanceID', width: 100 }, { title: '时间', key: 'time', width: 160 }, { title: '吞吐', key: 'ops', width: 120 }, { title: '内存', key: 'memory', width: 120 }, { title: '连接数', dataIndex: 'clients', width: 100 }, { title: '命中率', key: 'hit' }]">
        <template #emptyText><a-empty description="暂无监控采样" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'time'">{{ dayjs.unix(record.minute).format('MM-DD HH:mm') }}</template>
          <template v-else-if="column.key === 'ops'">{{ record.ops_per_sec }} ops/s</template>
          <template v-else-if="column.key === 'memory'">{{ bytes(record.used_memory) }}</template>
          <template v-else-if="column.key === 'hit'">{{ record.hit_rate ? `${(record.hit_rate * 100).toFixed(1)}%` : '—' }}</template>
        </template>
      </a-table>
    </section>

    <section class="panel">
      <div class="phead"><h2>业务账号</h2><a-button size="small" :disabled="!session.can('operator') || !session.writable" @click="openAccount">新建 / 轮换</a-button></div>
      <a-table :data-source="unit.accounts || []" row-key="name" size="small" :pagination="false" :columns="[{ title: '名称', dataIndex: 'name' }, { title: 'Key 范围', dataIndex: 'key_patterns' }, { title: '命令类别', dataIndex: 'categories' }, { title: '状态', key: 'status' }, { title: '', key: 'action', width: 80 }]">
        <template #emptyText><a-empty description="还没有业务账号" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'"><a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag></template>
          <template v-else-if="column.key === 'action' && session.can('operator')"><a-popconfirm title="删除该业务账号？" @confirm="deleteAccount(record)"><a class="danger">删除</a></a-popconfirm></template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="rebalanceOpen" title="槽位再平衡" width="760px" :ok-text="rebalanceActionable ? '确认执行' : '关闭'" :ok-button-props="{ disabled: rebalanceActionable && (!canRebalance || rebalanceLoading) }" @ok="onRebalanceOk">
      <a-spin :spinning="rebalanceLoading">
        <template v-if="rebalancePlan">
          <a-alert v-if="rebalancePlan.balanced" type="success" show-icon message="各主节点槽数已在目标范围内，无需操作。" />
          <template v-else>
            <p class="sub">每个主节点目标为 ⌊16384/N⌋ 或 ⌈16384/N⌉ 个槽，本次共迁移 {{ rebalancePlan.moved }} 个槽，分 {{ rebalancePlan.steps.length }} 步执行。</p>
            <a-table :data-source="rebalancePlan.steps" row-key="index" size="small" :pagination="false" :columns="[{ title: '步骤', dataIndex: 'index', width: 70 }, { title: '源 → 目标', key: 'route' }, { title: '槽范围', key: 'ranges' }, { title: '槽数', dataIndex: 'count', width: 90 }]">
              <template #bodyCell="{ column, record }">
                <template v-if="column.key === 'route'"><span class="code">{{ record.source }}</span> → <span class="code">{{ record.target }}</span></template>
                <template v-else-if="column.key === 'ranges'"><span class="code">{{ slotText(record.slots) }}</span></template>
              </template>
            </a-table>
          </template>
          <a-table :data-source="rebalancePlan.nodes" row-key="instance_id" size="small" :pagination="false" :columns="[{ title: '主节点', dataIndex: 'name' }, { title: '再平衡前', dataIndex: 'before', width: 110 }, { title: '目标', dataIndex: 'target', width: 90 }, { title: '再平衡后', dataIndex: 'after', width: 110 }]" />
          <p class="sub">执行时会重新读取实时槽位并重新计算计划，结果可能与预览略有不同。任一步失败会停止后续步骤，已完成的步骤不回滚，可在操作记录中查看停止的位置。</p>
        </template>
      </a-spin>
    </a-modal>

    <a-modal v-model:open="accountOpen" title="保存业务账号" ok-text="保存" @ok="saveAccount">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="accountForm.name" placeholder="例如 app" /></a-form-item>
        <a-form-item label="口令" extra="留空由服务端生成；生成的口令只显示一次。"><a-input-password v-model:value="accountForm.password" autocomplete="new-password" /></a-form-item>
        <a-form-item label="Key 范围"><a-input v-model:value="accountForm.key_patterns" /></a-form-item>
        <a-form-item label="命令类别"><a-input v-model:value="accountForm.categories" /></a-form-item>
        <a-checkbox v-model:checked="accountForm.enabled">启用</a-checkbox>
      </a-form>
      <a-alert v-if="generatedPassword" type="warning" show-icon style="margin-top: 14px" message="请立即保存以下口令；关闭窗口后不会再次返回。" :description="generatedPassword" />
    </a-modal>

    <a-modal v-model:open="rollbackOpen" :title="`回滚参数到 ${rollbackTarget?.id || ''}`" ok-text="确认回滚" :ok-button-props="{ disabled: !session.can('releaser') || !session.writable || !rollbackDiff }" @ok="submitRollback">
      <p class="sub">当前版本 <b>{{ rollbackPreview?.base_revision }}</b> → 目标版本 <b>{{ rollbackTarget?.id }}</b>。回滚会生成一个新版本，历史版本保持不变。</p>
      <DiffView v-if="rollbackDiff" :diff="rollbackDiff" />
      <a-alert v-else type="info" show-icon message="目标版本与当前参数一致，无需回滚" />
      <a-input v-model:value="rollbackNote" placeholder="变更说明（必填）" style="margin-top: 14px" />
      <a-checkbox v-model:checked="rollbackRestart" style="margin-top: 8px">需要时滚动重启</a-checkbox>
      <p v-if="rollbackPreview?.restart?.length" class="sub">需重启的参数：{{ rollbackPreview.restart.join('、') }}</p>
    </a-modal>
    <a-modal v-model:open="upgradeOpen" title="滚动升级数据面" ok-text="提交升级" :ok-button-props="{ disabled: !session.can('releaser') || !session.writable }" @ok="upgradeUnit">
      <a-form layout="vertical">
        <a-form-item label="目标版本" extra="版本必须已导入版本库，且覆盖该单元所有主机的架构。"><a-input v-model:value="upgradeVersion" placeholder="例如 1.30.6-1" /></a-form-item>
        <a-checkbox v-model:checked="upgradeSwitchover">重启主节点前先协调切换</a-checkbox>
      </a-form>
      <a-alert type="info" show-icon style="margin-top: 14px" message="升级失败时会在操作记录中暂停；可由另一名发布员在操作中心续跑。" />
    </a-modal>

    <a-modal v-model:open="scaleOpen" title="调整部署规格" ok-text="提交扩缩容" :ok-button-props="{ disabled: !session.can('releaser') || !session.writable }" @ok="scaleUnit">
      <a-form layout="vertical">
        <a-form-item label="每个分片的从节点数" extra="范围 1-5；缩容会先停止并回收多余实例，数据目录进入主机回收区。"><a-input-number v-model:value="scaleForm.replicas" :min="1" :max="5" style="width: 100%" /></a-form-item>
        <a-form-item v-if="unit.mode === 'cluster'" label="分片数" extra="范围 3-1024；扩容会在线迁槽，缩容会先把槽迁回保留分片。"><a-input-number v-model:value="scaleForm.shards" :min="3" :max="1024" style="width: 100%" /></a-form-item>
      </a-form>
      <a-alert type="warning" show-icon message="扩缩容是后台操作；执行期间该部署单元会被加锁，失败时会暂停并允许续跑。" />
    </a-modal>

    <a-modal v-model:open="sentinelOpen" title="哨兵管理" ok-text="提交" :ok-button-props="{ disabled: !session.can('releaser') || !session.writable }" @ok="sentinelOperation">
      <a-form layout="vertical">
        <a-form-item label="操作"><a-select v-model:value="sentinelForm.operation" :options="[{ value: 'set', label: '调整故障检测参数' }, { value: 'monitor', label: '重新监控当前主节点' }, { value: 'reset', label: '重置监控状态' }, { value: 'remove', label: '移除监控目标' }]" /></a-form-item>
        <template v-if="sentinelForm.operation === 'set'">
          <a-form-item label="down-after-milliseconds"><a-input v-model:value="sentinelForm.down_after_ms" /></a-form-item>
          <a-form-item label="failover-timeout"><a-input v-model:value="sentinelForm.failover_timeout" /></a-form-item>
          <a-form-item label="parallel-syncs"><a-input v-model:value="sentinelForm.parallel_syncs" /></a-form-item>
        </template>
      </a-form>
      <a-alert v-if="sentinelForm.operation === 'remove'" type="warning" show-icon message="移除监控目标后不会再自动故障切换；如需恢复，请重新执行监控当前主节点。" />
    </a-modal>
  </div>
</template>

<style scoped>
.page-head { align-items: flex-start; }
.page-head h1 { margin-top: 6px; }
.summary { display: grid; grid-template-columns: repeat(6, 1fr); gap: 12px; margin-bottom: 16px; }
.summary > div { background: #fff; border: 1px solid #e1e7e6; border-radius: 8px; padding: 13px 15px; display: flex; flex-direction: column; gap: 7px; min-width: 0; }
.summary span, .sub { color: #7b8b8e; font-size: 12px; }
.summary strong { color: #16323a; font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.two-cols { display: grid; grid-template-columns: 1.4fr 1fr; gap: 16px; margin-top: 16px; }
.two-cols .panel { margin-top: 0; }
.phead { display: flex; align-items: center; justify-content: space-between; }
.phead h2 { margin-bottom: 10px; }
.slot-chart { width: 100%; height: 260px; }
.param-foot { display: flex; gap: 12px; align-items: center; margin-top: 10px; }
.param-foot .ant-input { flex: 1; }
.danger { color: #a3332d; }
@media (max-width: 1100px) { .summary { grid-template-columns: repeat(3, 1fr); } }
@media (max-width: 800px) { .two-cols { grid-template-columns: 1fr; } .summary { grid-template-columns: repeat(2, 1fr); } }
</style>
