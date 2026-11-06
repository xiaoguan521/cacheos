<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()
const unit = ref<any>(null)
const jobs = ref<any[]>([])
const error = ref('')
const loading = ref(false)
const cutoverLoading = ref<number | null>(null)
const form = reactive({ instance_id: '', source_address: '', source_user: '', source_password: '', pattern: '*', mode: 'full', batch: 200, max_passes: 2, source_quiesced: false })

const id = computed(() => String(route.params.id))
const instances = computed(() => (unit.value?.instances || []).filter((x: any) => x.kind === 'server'))

watch(() => form.mode, mode => { if (mode === 'full_incremental') form.pattern = '*' })

async function load() {
  try {
    const [u, m] = await Promise.all([api.get(`/units/${id.value}`), api.get(`/units/${id.value}/migrations`)]);
    unit.value = u
    jobs.value = m
    if (!form.instance_id) form.instance_id = String(instances.value.find((x: any) => x.role === 'master' || x.desired_role === 'master')?.id || instances.value[0]?.id || '')
    error.value = ''
  } catch (e) {
    error.value = errMsg(e)
  }
}

async function cutover(job: any) {
  cutoverLoading.value = Number(job.id)
  try {
    await api.post(`/units/${id.value}/migrations/${job.id}/cutover`, { source_quiesced: true })
    message.success('迁移已完成切换')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    cutoverLoading.value = null
  }
}

async function start() {
  loading.value = true
  try {
    await api.post(`/units/${id.value}/migrations`, { ...form, instance_id: Number(form.instance_id), batch: Number(form.batch), max_passes: Number(form.max_passes) })
    message.success('迁移任务已提交')
    form.source_password = ''
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    loading.value = false
  }
}

function progress(job: any) {
  try { return JSON.parse(job.progress || '{}') } catch { return {} }
}

onMounted(load)
let timer: number | undefined
onMounted(() => { timer = window.setInterval(load, 10000) })
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div><a @click="router.push({ name: 'unit-detail', params: { id } })">← {{ unit?.name || '部署单元' }}</a><h1>在线数据迁移</h1><p>全量迁移由 agent 直接在数据面执行；增量迁移使用目标实例原生复制，源端账号需允许 PSYNC/SYNC/REPLCONF/INFO。</p></div>
    </div>
    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />
    <a-alert type="warning" show-icon message="全量 + 复制流增量会让目标先保持源端副本；源端停写后再执行最终切换，避免把未确认的复制偏移当成一致性结果。" style="margin-bottom: 16px" />
    <section class="panel form">
      <a-select v-model:value="form.instance_id" placeholder="目标实例" style="min-width: 250px" :options="instances.map((x: any) => ({ value: String(x.id), label: `${x.address || x.host}:${x.port} · ${x.role || x.desired_role}` }))" />
      <a-input v-model:value="form.source_address" placeholder="源端地址，例如 10.0.0.10:6379" style="min-width: 280px" />
      <a-input v-model:value="form.source_user" placeholder="源端 ACL 用户（可选）" style="width: 190px" />
      <a-input-password v-model:value="form.source_password" placeholder="源端口令（可选）" style="width: 190px" />
      <a-input v-model:value="form.pattern" :disabled="form.mode === 'full_incremental'" placeholder="Key 前缀，例如 app:*" style="width: 190px" />
      <a-select v-model:value="form.mode" style="width: 190px" :options="[{ value: 'full', label: '全量' }, { value: 'full_incremental', label: '全量 + 复制流增量' }]" />
      <a-input-number v-model:value="form.batch" :min="1" :max="1000" addon-before="批量" />
      <a-input-number v-if="form.mode === 'full_incremental'" v-model:value="form.max_passes" :min="2" :max="5" addon-before="轮数" />
      <a-checkbox v-if="form.mode === 'full_incremental'" v-model:checked="form.source_quiesced">我确认源端已停写，初次同步完成后自动切换</a-checkbox>
      <a-button type="primary" :loading="loading" :disabled="!session.can('operator') || !session.writable" @click="start">开始迁移</a-button>
    </section>
    <section class="panel">
      <h2>任务记录</h2>
      <a-table :data-source="jobs" row-key="id" size="small" :pagination="{ pageSize: 10 }" :columns="[{ title: 'ID', dataIndex: 'id', width: 80 }, { title: '源端', dataIndex: 'source_addr' }, { title: '模式', dataIndex: 'mode', width: 170 }, { title: '状态', dataIndex: 'status', width: 130 }, { title: '进度', key: 'progress', width: 300 }, { title: '创建时间', key: 'created' }, { title: '', key: 'action', width: 110 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'progress'"><span class="code">{{ JSON.stringify(progress(record)) }}</span></template>
          <template v-else-if="column.key === 'created'">{{ dayjs(record.created_at).format('YYYY-MM-DD HH:mm:ss') }}</template>
          <template v-else-if="column.key === 'action' && record.status === 'awaiting_cutover'"><a-button size="small" type="primary" :loading="cutoverLoading === Number(record.id)" :disabled="!session.can('operator') || !session.writable" @click="cutover(record)">确认切换</a-button></template>
        </template>
      </a-table>
      <a-empty v-if="!jobs.length" description="还没有迁移任务" />
    </section>
  </div>
</template>

<style scoped>
.form { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
.code { font-size: 12px; white-space: pre-wrap; word-break: break-all; }
</style>
