<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import dayjs from 'dayjs'
import { api } from '../api/client'
import TrafficChart from '../components/TrafficChart.vue'
import { deployStatus, kindName } from '../components/status'

const data = ref<any>(null)
const nodes = ref<any[]>([])
const groups = ref<any[]>([])

async function load() {
  const [d, n, g] = await Promise.all([api.get('/dashboard'), api.get('/nodes'), api.get('/node-groups')])
  data.value = d
  nodes.value = n.filter((x: any) => x.status !== 'revoked')
  groups.value = g
}

let t: number | undefined
onMounted(() => {
  load()
  t = window.setInterval(load, 5000)
})
onUnmounted(() => clearInterval(t))

function groupName(id: number) {
  return groups.value.find((g) => g.id === id)?.name || id
}
function currentRev(groupId: number) {
  return groups.value.find((g) => g.id === groupId)?.current_revision_id || ''
}
function ago(t?: string) {
  if (!t) return '从未'
  const s = dayjs().diff(dayjs(t), 'second')
  return s < 60 ? `${s} 秒前` : s < 3600 ? `${Math.floor(s / 60)} 分钟前` : dayjs(t).format('MM-DD HH:mm')
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>边缘节点</h1>
        <p v-if="data">
          <span class="num">{{ data.online }}</span> / <span class="num">{{ data.nodes }}</span> 在线，
          <span class="num">{{ data.sites }}</span> 个站点，最近 15 分钟
          <span class="num">{{ data.requests_15m }}</span> 次请求，5xx 占比
          <span class="num">{{ data.error_rate_15m.toFixed(2) }}%</span>
        </p>
      </div>
      <router-link to="/nodes"><a-button>添加节点</a-button></router-link>
    </div>

    <a-alert v-if="data && (data.certs_expired || data.certs_expiring)" :type="data.certs_expired ? 'error' : 'warning'" show-icon style="margin-bottom: 16px">
      <template #message>
        <span v-if="data.certs_expired"><b class="num">{{ data.certs_expired }}</b> 张证书已过期</span>
        <span v-if="data.certs_expired && data.certs_expiring">，</span>
        <span v-if="data.certs_expiring"><b class="num">{{ data.certs_expiring }}</b> 张证书 30 天内到期</span>
        <router-link to="/certificates" style="margin-left: 12px">去续期</router-link>
      </template>
    </a-alert>

    <section class="fleet" v-if="data">
      <a-empty v-if="!nodes.length" description="还没有节点。在“节点”页生成注册命令，在边缘服务器上执行即可接入。" />
      <article v-for="n in nodes" :key="n.id" class="node" :class="[n.status, { drift: n.drifted, behind: n.status === 'online' && currentRev(n.group_id) && n.applied_revision !== currentRev(n.group_id) }]">
        <header>
          <span class="light" />
          <strong>{{ n.name }}</strong>
          <span class="grp">{{ groupName(n.group_id) }}</span>
        </header>
        <div class="rev num">{{ n.applied_revision || '未发布' }}</div>
        <div class="meta">
          <template v-if="n.status === 'online'">
            <span class="num">{{ n.active_connections }}</span> 个连接，CPU <span class="num">{{ Math.round(n.cpu_percent) }}%</span>
          </template>
          <template v-else>离线，最后心跳 {{ ago(n.last_seen_at) }}</template>
        </div>
        <div v-if="n.drifted" class="flag">配置被手动改动</div>
        <div v-else-if="n.status === 'online' && currentRev(n.group_id) && n.applied_revision !== currentRev(n.group_id)" class="flag">落后于组版本 {{ currentRev(n.group_id) }}</div>
      </article>
    </section>

    <div class="grid" v-if="data">
      <section class="panel">
        <h2>全网流量（最近 30 分钟）</h2>
        <TrafficChart :series="data.series" />
      </section>
      <section class="panel">
        <h2>最近发布</h2>
        <a-empty v-if="!data.deployments.length" description="还没有发布记录" />
        <ul class="deps">
          <li v-for="d in data.deployments" :key="d.id">
            <router-link :to="{ name: 'releases', query: { d: d.id } }" class="num">{{ d.revision_id }}</router-link>
            <span class="kind">{{ kindName[d.kind] || d.kind }}</span>
            <a-tag :color="deployStatus[d.status]?.color">{{ deployStatus[d.status]?.text || d.status }}</a-tag>
            <span class="note">{{ d.note }}</span>
            <time>{{ ago(d.started_at) }}</time>
          </li>
        </ul>
      </section>
    </div>
  </div>
</template>

<style scoped>
.fleet {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.node {
  position: relative;
  background: #fff;
  border: 1px solid #e1e7e6;
  border-left: 4px solid #2f9e5b;
  border-radius: 6px;
  padding: 12px 14px;
}
.node.offline {
  border-left-color: #b8c3c2;
  background: #fafbfb;
}
.node.behind {
  border-left-color: #d9a21b;
}
.node.drift {
  border-left-color: #c8423b;
}
.node header {
  display: flex;
  align-items: center;
  gap: 8px;
}
.light {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #2f9e5b;
  box-shadow: 0 0 0 3px #e6f3ec;
}
.offline .light {
  background: #b8c3c2;
  box-shadow: none;
}
.grp {
  margin-left: auto;
  font-size: 12px;
  color: #6b7c80;
}
.rev {
  margin: 10px 0 4px;
  font-size: 22px;
  font-weight: 600;
  color: #16323a;
}
.offline .rev {
  color: #8a9a9d;
}
.meta {
  font-size: 12px;
  color: #6b7c80;
}
.flag {
  margin-top: 8px;
  font-size: 12px;
  color: #a3332d;
}
.behind .flag {
  color: #8a6510;
}
.grid {
  display: grid;
  grid-template-columns: 1.6fr 1fr;
  gap: 16px;
  align-items: start;
}
.grid .panel + .panel {
  margin-top: 0;
}
.deps {
  list-style: none;
  margin: 0;
  padding: 0;
}
.deps li {
  display: grid;
  grid-template-columns: 76px 64px auto 1fr auto;
  align-items: center;
  gap: 8px;
  padding: 9px 0;
  border-bottom: 1px solid #eef2f1;
  font-size: 13px;
}
.deps li:last-child {
  border-bottom: 0;
}
.kind {
  color: #6b7c80;
}
.note {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #3b4a4e;
}
time {
  color: #8a9a9d;
  font-size: 12px;
}
@media (max-width: 980px) {
  .grid {
    grid-template-columns: 1fr;
  }
}
</style>
