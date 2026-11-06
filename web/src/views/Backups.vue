<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()
const unit = ref<any>(null)
const rows = ref<any[]>([])
const instanceId = ref('')
const upload = ref(false)
const policy = ref<any>({ enabled: false, cron: '02:00', keep_days: 7, upload: false })
const loading = ref(false)
const error = ref('')
const id = computed(() => String(route.params.id))
const instances = computed(() => (unit.value?.instances || []).filter((x: any) => x.kind === 'server'))

async function load() {
  try {
    const [u, b, p] = await Promise.all([api.get(`/units/${id.value}`), api.get(`/units/${id.value}/backups`), api.get(`/units/${id.value}/backup-policy`)]);
    unit.value = u
    rows.value = b
    policy.value = p
    if (!instanceId.value) instanceId.value = String(instances.value.find((x: any) => x.role === 'master' || x.desired_role === 'master')?.id || instances.value[0]?.id || '')
    error.value = ''
  } catch (e) { error.value = errMsg(e) }
}

async function savePolicy() {
  try {
    policy.value = await api.put(`/units/${id.value}/backup-policy`, policy.value)
    message.success('定时备份策略已保存')
  } catch (e) { message.error(errMsg(e)) }
}

async function create() {
  loading.value = true
  try {
    await api.post(`/units/${id.value}/backups`, { upload: upload.value, instance_id: instanceId.value })
    message.success(upload.value ? '备份已生成并上传到控制台' : '本地备份已生成')
    await load()
  } catch (e) { message.error(errMsg(e)) } finally { loading.value = false }
}

async function restore(row: any) {
  try {
    await api.post(`/units/${id.value}/backups/${encodeURIComponent(row.id)}/restore`, { instance_id: instanceId.value })
    message.success('恢复请求已提交；实例必须处于停止状态')
    await load()
  } catch (e) { message.error(errMsg(e)) }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div><a @click="router.push({ name: 'unit-detail', params: { id } })">← {{ unit?.name || '部署单元' }}</a><h1>备份与恢复</h1><p>RDB 先在实例本机生成并校验 SHA-256；远端备份通过 agent 到控制台的分片通道上传。</p></div>
    </div>
    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />
    <section class="panel controls">
      <a-select v-model:value="instanceId" style="min-width: 260px" placeholder="备份实例" :options="instances.map((x: any) => ({ value: String(x.id), label: `${x.address || x.host}:${x.port} · ${x.role || x.desired_role}` }))" />
      <a-switch v-model:checked="upload" :disabled="!session.license?.features?.includes('remote_backup')" />
      <span>上传到控制台</span>
      <a-button type="primary" :loading="loading" :disabled="!session.can('operator') || !session.writable" @click="create">创建 RDB 备份</a-button>
    </section>
    <section class="panel">
      <div class="phead"><h2>定时备份</h2><a-button size="small" :disabled="!session.can('operator') || !session.writable" @click="savePolicy">保存策略</a-button></div>
      <div class="policy">
        <label>启用 <a-switch v-model:checked="policy.enabled" /></label>
        <label>每日时间 <a-input v-model:value="policy.cron" style="width: 120px" placeholder="02:00" /></label>
        <label>保留天数 <a-input-number v-model:value="policy.keep_days" :min="1" :max="3650" /></label>
        <label>上传到控制台 <a-switch v-model:checked="policy.upload" :disabled="!session.license?.features?.includes('remote_backup')" /></label>
      </div>
      <p class="muted">时间使用控制台时区，格式 HH:MM；调度器每分钟检查一次，备份失败会留下失败记录。</p>
    </section>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" size="small" :pagination="{ pageSize: 20 }" :columns="[{ title: '编号', dataIndex: 'id' }, { title: '位置', dataIndex: 'location' }, { title: '大小', key: 'size' }, { title: 'SHA-256', dataIndex: 'sha256' }, { title: '状态', dataIndex: 'status' }, { title: '时间', key: 'time' }, { title: '', key: 'action', width: 90 }]">
        <template #emptyText><a-empty description="还没有备份" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'size'">{{ record.size ? `${(record.size / 1024 / 1024).toFixed(1)} MiB` : '—' }}</template>
          <template v-else-if="column.key === 'time'">{{ dayjs(record.created_at).format('YYYY-MM-DD HH:mm') }}</template>
          <template v-else-if="column.key === 'action'"><a-popconfirm title="恢复会覆盖目标实例 RDB，且要求实例已停止，确定？" @confirm="restore(record)"><a :disabled="record.status !== 'done' || !session.can('operator')">恢复</a></a-popconfirm></template>
        </template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.phead { display: flex; align-items: center; justify-content: space-between; }
.policy { display: flex; align-items: center; gap: 24px; flex-wrap: wrap; }
.policy label { display: inline-flex; align-items: center; gap: 8px; }
</style>
