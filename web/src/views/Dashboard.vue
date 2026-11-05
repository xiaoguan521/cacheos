<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import dayjs from 'dayjs'
import { api, errMsg } from '../api/client'
import { deployStatus } from '../components/status'

const data = ref<any>(null)
const error = ref('')

async function load() {
  try {
    data.value = await api.get('/dashboard')
    error.value = ''
  } catch (e) {
    error.value = errMsg(e)
  }
}

let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(load, 10000)
})
onUnmounted(() => clearInterval(timer))

const health = computed(() => data.value?.unit_health || { ok: 0, warning: 0, critical: 0 })
const problems = computed(() => data.value?.problems || [])
const operations = computed(() => data.value?.operations || [])

function bytes(v: number) {
  if (!v) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let n = v
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  return `${n >= 10 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`
}

function ago(value?: string) {
  if (!value) return '—'
  const seconds = dayjs().diff(dayjs(value), 'second')
  if (seconds < 60) return `${Math.max(seconds, 0)} 秒前`
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟前`
  return dayjs(value).format('MM-DD HH:mm')
}

function opText(op: any) {
  const names: Record<string, string> = { create: '创建', params: '参数变更', start: '启动', stop: '停止', restart: '重启', failover: '故障切换', delete: '删除' }
  return names[op.kind] || op.kind
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>CacheOS 总览</h1>
        <p>主机、部署单元与最近变更的运行状态。</p>
      </div>
      <a-button @click="load">刷新</a-button>
    </div>

    <a-alert v-if="error" type="error" show-icon :message="error" style="margin-bottom: 16px" />

    <div v-if="data" class="stats">
      <div class="stat"><span>在线主机</span><strong>{{ data.hosts_online }}<small> / {{ data.hosts }}</small></strong><em>{{ data.hosts_tuning_warnings }} 台有调优提醒</em></div>
      <div class="stat"><span>部署单元</span><strong>{{ data.units }}</strong><em>{{ data.instances }} 个实例</em></div>
      <div class="stat"><span>使用内存</span><strong>{{ bytes(data.memory_used) }}</strong><em>规格 {{ bytes(data.memory_total) }}</em></div>
      <div class="stat"><span>吞吐</span><strong>{{ data.ops_per_sec }}</strong><em>ops / sec</em></div>
    </div>

    <div v-if="data" class="health-row">
      <div class="health ok"><span />健康 {{ health.ok }}</div>
      <div class="health warning"><span />需关注 {{ health.warning }}</div>
      <div class="health critical"><span />异常 {{ health.critical }}</div>
      <router-link to="/units">查看全部部署单元 →</router-link>
    </div>

    <section v-if="data" class="panel">
      <div class="phead"><h2>需要关注</h2><span class="muted">{{ problems.length }} 条</span></div>
      <a-empty v-if="!problems.length" description="当前没有异常" />
      <div v-else class="problem-list">
        <router-link v-for="(p, i) in problems" :key="i" :to="{ name: 'unit-detail', params: { id: p.unit_id } }">
          <a-tag color="warning">{{ p.unit }}</a-tag><span>{{ p.problem }}</span>
        </router-link>
      </div>
    </section>

    <section v-if="data" class="panel">
      <div class="phead"><h2>最近操作</h2><router-link to="/operations">查看全部 →</router-link></div>
      <a-table :data-source="operations" row-key="id" size="small" :pagination="false"
        :columns="[{ title: '时间', key: 'time', width: 150 }, { title: '部署单元', dataIndex: 'unit' }, { title: '操作', key: 'kind' }, { title: '状态', key: 'status' }, { title: '操作人', dataIndex: 'created_by' }]">
        <template #emptyText><a-empty description="还没有操作记录" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'time'"><span class="num">{{ ago(record.created_at) }}</span></template>
          <template v-else-if="column.key === 'kind'">{{ opText(record) }}</template>
          <template v-else-if="column.key === 'status'"><a-tag :color="deployStatus[record.status]?.color || 'default'">{{ deployStatus[record.status]?.text || record.status }}</a-tag></template>
        </template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.stats { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin-bottom: 16px; }
.stat { background: #fff; border: 1px solid #e1e7e6; border-radius: 8px; padding: 16px 18px; display: flex; flex-direction: column; gap: 6px; }
.stat span, .stat em, .muted { color: #6b7c80; font-size: 12px; font-style: normal; }
.stat strong { color: #16323a; font-size: 26px; font-weight: 650; line-height: 1.1; }
.stat small { color: #8a9a9d; font-size: 14px; font-weight: 400; }
.health-row { display: flex; align-items: center; gap: 20px; margin-bottom: 16px; font-size: 13px; }
.health { display: inline-flex; gap: 7px; align-items: center; }
.health span { width: 8px; height: 8px; border-radius: 50%; background: currentColor; }
.health.ok { color: #1f6f43; }.health.warning { color: #8a6510; }.health.critical { color: #a3332d; }
.health-row a { margin-left: auto; }
.phead { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
.phead h2 { margin: 0; }
.problem-list { display: flex; flex-direction: column; gap: 9px; }
.problem-list a { display: flex; align-items: center; gap: 10px; color: #334b51; text-decoration: none; }
.problem-list a:hover { color: #0e7c86; }
@media (max-width: 900px) { .stats { grid-template-columns: repeat(2, 1fr); } }
@media (max-width: 560px) { .stats { grid-template-columns: 1fr; } .health-row { flex-wrap: wrap; } }
</style>
