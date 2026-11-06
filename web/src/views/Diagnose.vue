<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'

const route = useRoute()
const router = useRouter()
const unit = ref<any>(null)
const instanceId = ref('')
const kind = ref('memory')
const sample = ref(100)
const report = ref<any>(null)
const loading = ref(false)
const error = ref('')

const id = computed(() => String(route.params.id))
const instances = computed(() => (unit.value?.instances || []).filter((x: any) => x.kind === 'server'))
const kindName: Record<string, string> = { memory: '内存', slowlog: '慢日志', latency: '延迟事件', clients: '客户端连接', bigkeys: '大 Key 抽样', rdb_analyze: 'RDB 离线分析' }

async function loadUnit() {
  unit.value = await api.get(`/units/${id.value}`)
}

async function run() {
  loading.value = true
  error.value = ''
  try {
    report.value = await api.get(`/units/${id.value}/diagnose`, { kind: kind.value, sample: Number(sample.value), instance_id: instanceId.value || undefined })
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
      <div class="phead"><h2>{{ kindName[kind] }}报告</h2><span class="muted">最多 {{ sample }} 条</span></div>
      <pre class="output">{{ JSON.stringify(report, null, 2) }}</pre>
    </section>
    <section class="panel" v-else><a-empty description="选择诊断项后运行" /></section>
  </div>
</template>

<style scoped>
.controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.phead { display: flex; justify-content: space-between; align-items: center; }
.output { margin: 0; padding: 14px; background: #f4f7f7; border-radius: 6px; max-height: 620px; overflow: auto; font-size: 12px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
</style>
