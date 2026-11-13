<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const units = ref<any[]>([])
const templates = ref<any[]>([])
const loading = ref(false)
const modalOpen = ref(false)
const busy = ref<number | null>(null)
const form = reactive({ name: '', mode: 'standalone', description: '', memory_mb: 512, shards: 3, replicas: 1, tls: false, modules: [] as string[], sentinel_name: '', quorum: 2, template_id: 0 })

// 扩展数据类型模块（ADR 0002）：只能在创建时选择；search 目前不支持集群。
const moduleOptions = computed(() => [
  { value: 'json', label: 'JSON 文档（json）' },
  { value: 'search', label: '搜索与向量（search）', disabled: form.mode === 'cluster' },
  { value: 'bloom', label: '布隆过滤器（bloom）' },
])
watch(() => form.mode, (m) => {
  if (m === 'cluster') form.modules = form.modules.filter((x) => x !== 'search')
})

const modeName: Record<string, string> = { standalone: '单机', replication: '主从', sentinel: '哨兵', cluster: '集群' }
const statusColor: Record<string, string> = { running: 'green', creating: 'gold', changing: 'gold', degraded: 'orange', stopped: 'default', failed: 'red', deleting: 'volcano' }

async function load() {
  loading.value = true
  try {
    const [u, t] = await Promise.all([api.get('/units'), api.get('/param-templates')])
    units.value = u
    templates.value = t
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function openCreate() {
  Object.assign(form, { name: '', mode: 'standalone', description: '', memory_mb: 512, shards: 3, replicas: 1, tls: false, modules: [], sentinel_name: '', quorum: 2, template_id: templates.value.find((x) => x.builtin)?.id || 0 })
  modalOpen.value = true
}

async function create() {
  try {
    const r = await api.post('/units', { ...form, memory_mb: Number(form.memory_mb), shards: form.mode === 'cluster' ? Number(form.shards) : 1, replicas: ['replication', 'sentinel', 'cluster'].includes(form.mode) ? Number(form.replicas) : 0, quorum: form.mode === 'sentinel' ? Number(form.quorum) : 0, template_id: Number(form.template_id), params: {}, host_ids: [] })
    modalOpen.value = false
    message.success(`已创建操作 #${r.operation?.id || '—'}，正在部署`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function action(unit: any, name: string) {
  busy.value = unit.id
  try {
    const r = await api.post(`/units/${unit.id}/actions/${name}`, { switchover: name === 'restart' })
    message.success(`操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    busy.value = null
  }
}

async function failover(unit: any) {
  busy.value = unit.id
  try {
    const r = await api.post(`/units/${unit.id}/failover`)
    message.success(`故障切换操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    busy.value = null
  }
}

async function remove(unit: any) {
  try {
    const r = await api.del(`/units/${unit.id}?confirm=${encodeURIComponent(unit.name)}`)
    message.success(`删除操作 #${r.operation?.id || '—'} 已提交`)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

function bytes(v: number) {
  if (!v) return '—'
  return v >= 1024 ? `${(v / 1024).toFixed(1)} GiB` : `${v} MiB`
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>部署单元</h1>
        <p>以单机、主从、哨兵或集群形态编排缓存实例。</p>
      </div>
      <a-space><a-button @click="load">刷新</a-button><a-button type="primary" :disabled="!session.can('operator') || !session.writable" @click="openCreate">创建部署单元</a-button></a-space>
    </div>

    <section class="panel">
      <a-table :data-source="units" row-key="id" :loading="loading" :pagination="{ pageSize: 20 }"
        :columns="[{ title: '名称', key: 'name' }, { title: '形态', key: 'mode', width: 100 }, { title: '状态', key: 'status', width: 110 }, { title: '实例', key: 'instances', width: 110 }, { title: '资源', key: 'usage', width: 190 }, { title: '端点', key: 'endpoint', width: 180 }, { title: '操作', key: 'actions', width: 250 }]">
        <template #emptyText><a-empty description="还没有部署单元" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'"><router-link class="unit-name" :to="{ name: 'unit-detail', params: { id: record.id } }">{{ record.name }}</router-link><div class="sub">{{ record.description || '—' }}</div></template>
          <template v-else-if="column.key === 'mode'">{{ modeName[record.mode] || record.mode }}</template>
          <template v-else-if="column.key === 'status'"><a-tag :color="statusColor[record.status] || 'default'">{{ record.status }}</a-tag><a-tag v-if="record.health !== 'ok'" color="orange">{{ record.health }}</a-tag></template>
          <template v-else-if="column.key === 'instances'">{{ record.instances?.length || 0 }}<span class="sub"> / {{ record.shards * (1 + record.replicas) }}</span></template>
          <template v-else-if="column.key === 'usage'"><div>{{ bytes(record.memory_mb) }} · {{ record.usage?.ops_per_sec || 0 }} ops/s</div><div class="sub">已用 {{ bytes(Math.round((record.usage?.used_memory || 0) / 1024 / 1024)) }}</div></template>
          <template v-else-if="column.key === 'endpoint'"><span class="code">{{ record.endpoint || '未就绪' }}</span></template>
          <template v-else-if="column.key === 'actions' && session.can('operator')">
            <a-space :size="4" wrap>
              <a-button size="small" :loading="busy === record.id" @click="action(record, record.status === 'stopped' ? 'start' : 'stop')">{{ record.status === 'stopped' ? '启动' : '停止' }}</a-button>
              <a-button size="small" :loading="busy === record.id" @click="action(record, 'restart')">重启</a-button>
              <a-button v-if="record.mode !== 'standalone'" size="small" :loading="busy === record.id" @click="failover(record)">切换</a-button>
              <a-popconfirm title="删除部署单元及其编排记录？" @confirm="remove(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="modalOpen" title="创建部署单元" ok-text="创建" @ok="create">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="form.name" placeholder="例如 session-cache" /></a-form-item>
        <a-form-item label="形态"><a-select v-model:value="form.mode" :options="Object.entries(modeName).map(([value, label]) => ({ value, label }))" /></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.description" /></a-form-item>
        <a-form-item label="每个数据实例内存（MiB）"><a-input-number v-model:value="form.memory_mb" :min="64" :max="1048576" style="width: 100%" /></a-form-item>
        <a-form-item v-if="form.mode === 'replication'" label="每个分片的从节点数"><a-input-number v-model:value="form.replicas" :min="1" :max="5" style="width: 100%" /></a-form-item>
        <a-form-item v-if="form.mode === 'sentinel'" label="哨兵监控名"><a-input v-model:value="form.sentinel_name" placeholder="例如 cache-main" /></a-form-item>
        <a-form-item v-if="form.mode === 'sentinel'" label="哨兵 quorum"><a-input-number v-model:value="form.quorum" :min="1" :max="5" style="width: 100%" /></a-form-item>
        <a-form-item v-if="form.mode === 'cluster'" label="分片数"><a-input-number v-model:value="form.shards" :min="3" :max="1024" style="width: 100%" /></a-form-item>
        <a-form-item v-if="form.mode === 'cluster'" label="每个分片的从节点数"><a-input-number v-model:value="form.replicas" :min="1" :max="5" style="width: 100%" /></a-form-item>
        <a-form-item label="数据面 TLS"><a-switch v-model:checked="form.tls" /></a-form-item>
        <a-form-item label="扩展数据类型" extra="只能在创建时选择，创建后不能增减；搜索与向量暂不支持集群。">
          <a-checkbox-group v-model:value="form.modules" :options="moduleOptions" />
        </a-form-item>
        <a-form-item label="参数模板"><a-select v-model:value="form.template_id" :options="templates.map((t: any) => ({ value: t.id, label: `${t.name}${t.builtin ? '（内置）' : ''}` }))" /></a-form-item>
      </a-form>
      <a-alert type="info" show-icon message="创建后会生成后台操作；主机由编排器从在线主机中自动选择。" />
    </a-modal>
  </div>
</template>

<style scoped>
.unit-name { color: #0e7c86; font-weight: 600; text-decoration: none; }
.sub { color: #7b8b8e; font-size: 12px; }
</style>
