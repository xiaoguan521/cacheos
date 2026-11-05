<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

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
const accountForm = reactive({ name: '', password: '', key_patterns: '*', categories: '+@all', enabled: true })
const generatedPassword = ref('')

const id = computed(() => String(route.params.id))
const metricRows = computed(() => Object.entries(metrics.value?.instances || {}).flatMap(([instanceID, points]: [string, any]) => (points || []).slice(-20).map((p: any) => ({ instanceID, ...p }))))
const statusColor: Record<string, string> = { running: 'green', creating: 'gold', changing: 'gold', degraded: 'orange', stopped: 'default', failed: 'red', deleting: 'volcano' }

async function load() {
  try {
    const [u, m, r] = await Promise.all([api.get(`/units/${id.value}`), api.get(`/units/${id.value}/metrics`, { minutes: 60 }), api.get(`/units/${id.value}/revisions`)]);
    unit.value = u
    metrics.value = m
    revisions.value = r
    paramsText.value = JSON.stringify(u.params || {}, null, 2)
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

function openAccount() {
  Object.assign(accountForm, { name: '', password: '', key_patterns: '*', categories: '+@all', enabled: true })
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
        <a-button :disabled="!session.can('operator')" @click="action(unit.status === 'stopped' ? 'start' : 'stop')">{{ unit.status === 'stopped' ? '启动' : '停止' }}</a-button>
        <a-button :disabled="!session.can('operator')" @click="action('restart')">重启</a-button>
        <a-button v-if="unit.mode !== 'standalone'" :disabled="!session.can('operator')" @click="failover">故障切换</a-button>
      </a-space>
    </div>

    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />
    <div class="summary">
      <div><span>状态</span><strong><a-tag :color="statusColor[unit.status] || 'default'">{{ unit.status }}</a-tag></strong></div>
      <div><span>健康度</span><strong>{{ unit.health }}</strong></div>
      <div><span>端点</span><strong class="code">{{ unit.endpoint || '未就绪' }}</strong></div>
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
          <a-timeline-item v-for="revision in revisions" :key="revision.id" color="blue"><b>{{ revision.id }}</b> · {{ revision.note || '—' }}<div class="sub">{{ revision.created_by }} · {{ dayjs(revision.created_at).format('YYYY-MM-DD HH:mm') }}</div></a-timeline-item>
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
  </div>
</template>

<style scoped>
.page-head { align-items: flex-start; }
.page-head h1 { margin-top: 6px; }
.summary { display: grid; grid-template-columns: repeat(5, 1fr); gap: 12px; margin-bottom: 16px; }
.summary > div { background: #fff; border: 1px solid #e1e7e6; border-radius: 8px; padding: 13px 15px; display: flex; flex-direction: column; gap: 7px; min-width: 0; }
.summary span, .sub { color: #7b8b8e; font-size: 12px; }
.summary strong { color: #16323a; font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.two-cols { display: grid; grid-template-columns: 1.4fr 1fr; gap: 16px; margin-top: 16px; }
.two-cols .panel { margin-top: 0; }
.phead { display: flex; align-items: center; justify-content: space-between; }
.phead h2 { margin-bottom: 10px; }
.param-foot { display: flex; gap: 12px; align-items: center; margin-top: 10px; }
.param-foot .ant-input { flex: 1; }
.danger { color: #a3332d; }
@media (max-width: 1100px) { .summary { grid-template-columns: repeat(3, 1fr); } }
@media (max-width: 800px) { .two-cols { grid-template-columns: 1fr; } .summary { grid-template-columns: repeat(2, 1fr); } }
</style>
