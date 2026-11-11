<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const rows = ref<any[]>([])
const detail = ref<any>(null)
const open = ref(false)
const session = useSession()
const statusName: Record<string, string> = { DRAFT: '草稿', PRECHECKED: '已预检', APPROVED: '已批准', APPLYING: '执行中', APPLIED: '已完成', PARTIAL: '部分完成', FAILED: '失败', ROLLED_BACK: '已回滚', PAUSED: '已暂停' }
const statusColor: Record<string, string> = { APPLYING: 'gold', APPLIED: 'green', PARTIAL: 'orange', FAILED: 'red', ROLLED_BACK: 'volcano', PAUSED: 'default' }
const kindName: Record<string, string> = { create: '创建', params: '参数变更', start: '启动', stop: '停止', restart: '重启', failover: '故障切换', reshard: '在线迁槽', rebalance: '再平衡', scale: '扩缩容', upgrade: '引擎升级', delete: '删除' }

async function load() {
  try {
    rows.value = await api.get('/operations')
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function show(row: any) {
  try {
    detail.value = await api.get(`/operations/${row.id}`)
    open.value = true
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function approve(row: any) {
  try {
    await api.post(`/operations/${row.id}/approve`)
    message.success(`操作 #${row.id} 已批准`)
    await load()
  } catch (e) { message.error(errMsg(e)) }
}

async function resume(row: any) {
  try {
    await api.post(`/operations/${row.id}/resume`)
    message.success(`操作 #${row.id} 已续跑`)
    await load()
  } catch (e) { message.error(errMsg(e)) }
}

let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(load, 5000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div><h1>操作记录</h1><p>部署、参数变更、启停和故障切换都以可追踪的后台操作执行。</p></div>
      <a-button @click="load">刷新</a-button>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" :pagination="{ pageSize: 20 }" :columns="[{ title: '编号', dataIndex: 'id', width: 80 }, { title: '时间', key: 'time', width: 170 }, { title: '部署单元', key: 'unit' }, { title: '类型', key: 'kind', width: 130 }, { title: '状态', key: 'status', width: 120 }, { title: '操作人', dataIndex: 'created_by', width: 130 }, { title: '错误', dataIndex: 'error' }, { title: '', key: 'action', width: 70 }]">
        <template #emptyText><a-empty description="还没有操作记录" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'time'"><span class="num">{{ dayjs(record.created_at).format('YYYY-MM-DD HH:mm:ss') }}</span></template>
          <template v-else-if="column.key === 'unit'"><router-link :to="{ name: 'unit-detail', params: { id: record.unit_id } }">{{ record.unit || `#${record.unit_id}` }}</router-link></template>
          <template v-else-if="column.key === 'kind'">{{ kindName[record.kind] || record.kind }}</template>
          <template v-else-if="column.key === 'status'"><a-tag :color="statusColor[record.status] || 'default'">{{ statusName[record.status] || record.status }}</a-tag></template>
          <template v-else-if="column.key === 'action'"><a-space :size="6"><a @click="show(record)">详情</a><a v-if="record.status === 'PRECHECKED' && session.can('releaser') && record.created_by !== session.username" @click="approve(record)">批准</a><a v-if="record.status === 'PAUSED' && session.can('releaser') && record.created_by !== session.username" @click="resume(record)">续跑</a></a-space></template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="open" title="操作详情" :footer="null" width="760px">
      <template v-if="detail">
        <dl class="summary"><dt>编号</dt><dd>{{ detail.id }}</dd><dt>部署单元</dt><dd>{{ detail.unit || `#${detail.unit_id}` }}</dd><dt>状态</dt><dd><a-tag :color="statusColor[detail.status] || 'default'">{{ statusName[detail.status] || detail.status }}</a-tag></dd><dt>创建时间</dt><dd>{{ dayjs(detail.created_at).format('YYYY-MM-DD HH:mm:ss') }}</dd><dt>错误</dt><dd>{{ detail.error || '—' }}</dd></dl>
        <h3>步骤</h3>
        <a-timeline>
          <a-timeline-item v-for="(step, i) in detail.steps || []" :key="i" :color="step.status === 'failed' ? 'red' : step.status === 'ok' ? 'green' : 'blue'">
            <b>{{ step.name }}</b><span class="step-status">{{ step.status }}</span><div class="sub">{{ step.message || '—' }} · {{ step.at ? dayjs(step.at).format('MM-DD HH:mm:ss') : '' }}</div>
          </a-timeline-item>
        </a-timeline>
        <a-empty v-if="!detail.steps?.length" description="暂无步骤记录" />
      </template>
    </a-modal>
  </div>
</template>

<style scoped>
.summary { display: grid; grid-template-columns: 90px 1fr; row-gap: 10px; margin: 0 0 20px; font-size: 13px; }
dt { color: #6b7c80; } dd { margin: 0; color: #1f2a2e; }
h3 { font-size: 14px; color: #16323a; }
.step-status { margin-left: 10px; color: #6b7c80; font-size: 12px; }
.sub { color: #7b8b8e; font-size: 12px; }
</style>
