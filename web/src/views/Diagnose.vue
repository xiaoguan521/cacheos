<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'antdv-next'
import dayjs from 'dayjs'
import { api, errMsg } from '../api/client'

const route = useRoute()
const router = useRouter()
const unit = ref<any>(null)
const instanceId = ref('')
const kind = ref('memory')
const sample = ref(100)
const report = ref<any>(null)
const reportKind = ref('')
const loading = ref(false)
const error = ref('')

const id = computed(() => String(route.params.id))
const instances = computed(() => (unit.value?.instances || []).filter((x: any) => x.kind === 'server'))
const kindName: Record<string, string> = { memory: '内存', slowlog: '慢日志', latency: '延迟事件', clients: '客户端连接', bigkeys: '大 Key 抽样', rdb_analyze: 'RDB 离线分析' }

// 慢日志聚合列：只有命令名与耗时统计，不含参数值。
const aggColumns = [
  { title: '命令', dataIndex: 'command', key: 'command' },
  { title: '次数', dataIndex: 'count', key: 'count', width: 90 },
  { title: '总耗时 (µs)', dataIndex: 'total_us', key: 'total_us', width: 130 },
  { title: '平均 (µs)', dataIndex: 'avg_us', key: 'avg_us', width: 110 },
  { title: '最大 (µs)', dataIndex: 'max_us', key: 'max_us', width: 110 },
  { title: '最近一次', key: 'last', width: 170 },
]

// 把单实例与多实例两种回传统一成块列表。
const blocks = computed(() => {
  const r = report.value
  if (!r) return []
  if (Array.isArray(r.instances)) {
    return r.instances.map((x: any) => ({
      title: `端口 ${x.port}${x.role ? ' · ' + x.role : ''}`,
      aggregate: x.aggregate,
      advice: x.advice,
      error: x.error,
    }))
  }
  return [{ title: '', aggregate: r.aggregate, advice: r.advice, error: r.error }]
})

const sampleHint = computed(() => (reportKind.value === 'slowlog' ? '最多 128 条' : `最多 ${sample.value} 条`))

async function loadUnit() {
  unit.value = await api.get(`/units/${id.value}`)
}

async function run() {
  loading.value = true
  error.value = ''
  try {
    report.value = await api.get(`/units/${id.value}/diagnose`, { kind: kind.value, sample: Number(sample.value), instance_id: instanceId.value || undefined })
    reportKind.value = kind.value
  } catch (e) {
    error.value = errMsg(e)
    message.error(error.value)
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  try { await loadUnit() } catch (e) { error.value = errMsg(e) }
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div><a @click="router.push({ name: 'unit-detail', params: { id } })">← {{ unit?.name || '部署单元' }}</a><h1>性能诊断</h1><p>默认聚合全部数据实例；只返回指标与元数据。大 Key 通过 SCAN 抽样，RDB 分析只读取本机文件，不执行 KEYS *。</p></div>
    </div>
    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />
    <section class="panel controls">
      <a-select v-model:value="instanceId" style="min-width: 240px" placeholder="实例" :options="[{ value: '', label: '全部实例（聚合）' }, ...instances.map((x: any) => ({ value: String(x.id), label: `${x.address || x.host}:${x.port} · ${x.role || x.desired_role}` }))]" />
      <a-select v-model:value="kind" style="width: 150px" :options="Object.entries(kindName).map(([value, label]) => ({ value, label }))" />
      <a-input-number v-model:value="sample" :min="1" :max="1000" />
      <a-button type="primary" :loading="loading" @click="run">运行诊断</a-button>
    </section>
    <section class="panel" v-if="report !== null">
      <div class="phead"><h2>{{ kindName[reportKind] }}报告</h2><span class="muted">{{ sampleHint }}</span></div>
      <template v-if="reportKind === 'slowlog'">
        <div v-for="(b, i) in blocks" :key="i" class="block">
          <h3 v-if="b.title">{{ b.title }}</h3>
          <a-alert v-if="b.error" type="error" show-icon :message="b.error" style="margin-bottom: 12px" />
          <h3>按命令聚合（按总耗时降序）</h3>
          <a-table :data-source="b.aggregate || []" row-key="command" size="small" :pagination="false" :columns="aggColumns">
            <template #emptyText><a-empty description="没有慢日志记录" /></template>
            <template #bodyCell="{ column, record }">
              <template v-if="column.key === 'last'">{{ record.last_timestamp ? dayjs.unix(record.last_timestamp).format('YYYY-MM-DD HH:mm:ss') : '-' }}</template>
            </template>
          </a-table>
        </div>
      </template>
      <template v-else-if="reportKind === 'memory'">
        <div v-for="(b, i) in blocks" :key="i" class="block">
          <h3 v-if="b.title">{{ b.title }}</h3>
          <a-alert v-if="b.error" type="error" show-icon :message="b.error" style="margin-bottom: 12px" />
          <h3>内存建议</h3>
          <template v-if="(b.advice || []).length">
            <a-alert v-for="a in b.advice" :key="a.code" :type="a.level === 'warning' ? 'warning' : 'info'" show-icon :message="a.message" style="margin-bottom: 8px" />
          </template>
          <p v-else class="muted">未发现需要关注的内存问题。</p>
        </div>
      </template>
      <details v-if="reportKind === 'slowlog' || reportKind === 'memory'" class="raw">
        <summary>原始报告（JSON）</summary>
        <pre class="output">{{ JSON.stringify(report, null, 2) }}</pre>
      </details>
      <pre v-else class="output">{{ JSON.stringify(report, null, 2) }}</pre>
    </section>
    <section class="panel" v-else><a-empty description="选择诊断项后运行" /></section>
  </div>
</template>

<style scoped>
.controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.phead { display: flex; justify-content: space-between; align-items: center; }
.block { margin-bottom: 18px; }
.block h3 { margin: 12px 0 8px; font-size: 14px; }
.raw { margin-top: 14px; }
.raw summary { cursor: pointer; color: #5b6b6b; margin-bottom: 8px; }
.output { margin: 0; padding: 14px; background: #f4f7f7; border-radius: 6px; max-height: 620px; overflow: auto; font-size: 12px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
</style>
