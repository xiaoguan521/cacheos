<script setup lang="ts">
import { nodeResult } from './status'

// 每个节点一条“车道”，对应 agent 本地发布状态机（规格书 5.4）的阶段
const props = defineProps<{ nodes: any[]; revision: string }>()

const stages = ['下发', '校验', '切换', '加载', '探活']

function failedAt(n: any): number {
  const m = (n.message || '').toLowerCase()
  switch (n.status) {
    case 'INVALID':
      return 1
    case 'ROLLED_BACK':
    case 'DEGRADED':
      if (m.includes('post check')) return 4
      return 3
    case 'REJECTED':
    case 'CONFLICT':
    case 'TIMEOUT':
      return 0
    default:
      return -1
  }
}

function stageState(n: any, i: number): string {
  if (n.status === 'APPLIED') return 'done'
  if (n.status === 'APPLYING') return 'run'
  if (n.status === 'PENDING' || n.status === 'OFFLINE') return 'idle'
  const f = failedAt(n)
  if (i < f) return 'done'
  if (i === f) return 'fail'
  return 'idle'
}

function explain(n: any): string {
  const m: string = n.message || ''
  if (n.status === 'APPLIED') return `已切换到 ${props.revision}，流量正常`
  if (n.status === 'INVALID') return '配置未通过 syapp -t 校验，线上配置未改动'
  if (n.status === 'ROLLED_BACK') {
    const to = m.match(/rolled back to (r\d+)/)?.[1]
    const why = m.includes('post check') ? '发布后本机探活失败' : m.includes('bind') ? '端口占用导致加载失败' : '新配置加载失败'
    return `${why}，已自动回滚到 ${to || '上一版本'}，流量未中断`
  }
  if (n.status === 'OFFLINE') return '节点离线，上线后自动同步到组版本'
  if (n.status === 'APPLYING') return '执行中…'
  if (n.status === 'PENDING') return '排队中'
  return m
}
</script>

<template>
  <div class="lanes">
    <div v-for="n in nodes" :key="n.node_id" class="lane" :class="n.status.toLowerCase()">
      <div class="who">
        <strong>{{ n.node_name }}</strong>
        <a-tag :color="nodeResult[n.status]?.color" class="tag">{{ nodeResult[n.status]?.text || n.status }}</a-tag>
      </div>
      <ol class="track">
        <li v-for="(s, i) in stages" :key="s" :class="stageState(n, i)">
          <span class="seg" />
          <span class="lbl">{{ s }}</span>
        </li>
      </ol>
      <div class="why">{{ explain(n) }}</div>
      <details v-if="n.stderr" class="err">
        <summary>查看节点输出</summary>
        <pre class="code">{{ n.stderr }}</pre>
      </details>
    </div>
  </div>
</template>

<style scoped>
.lanes {
  display: grid;
  gap: 10px;
}
.lane {
  display: grid;
  grid-template-columns: 180px 1fr;
  grid-template-areas:
    'who track'
    'who why'
    'err err';
  column-gap: 20px;
  row-gap: 4px;
  padding: 12px 16px;
  border: 1px solid #e1e7e6;
  border-radius: 6px;
  background: #fff;
}
.who {
  grid-area: who;
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: flex-start;
}
.who strong {
  color: #16323a;
}
.tag {
  margin: 0;
}
.track {
  grid-area: track;
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: 4px;
  list-style: none;
  margin: 2px 0 0;
  padding: 0;
}
.track li {
  display: grid;
  gap: 4px;
}
.seg {
  height: 8px;
  border-radius: 4px;
  background: #e6eceb;
  position: relative;
  overflow: hidden;
}
.lbl {
  font-size: 12px;
  color: #8a9a9d;
}
.done .seg {
  background: #2f9e5b;
}
.done .lbl {
  color: #3b4a4e;
}
.fail .seg {
  background: #c8423b;
}
.fail .lbl {
  color: #a3332d;
  font-weight: 600;
}
/* 执行中：流动的车道，是界面里唯一的持续动效 */
.run .seg {
  background: #f3e5bd;
}
.run .seg::after {
  content: '';
  position: absolute;
  inset: 0;
  width: 40%;
  background: #d9a21b;
  border-radius: 4px;
  animation: drive 1.1s linear infinite;
}
.run:nth-child(2) .seg::after {
  animation-delay: 0.22s;
}
.run:nth-child(3) .seg::after {
  animation-delay: 0.44s;
}
.run:nth-child(4) .seg::after {
  animation-delay: 0.66s;
}
.run:nth-child(5) .seg::after {
  animation-delay: 0.88s;
}
@keyframes drive {
  from {
    transform: translateX(-100%);
  }
  to {
    transform: translateX(260%);
  }
}
.why {
  grid-area: why;
  font-size: 13px;
  color: #3b4a4e;
}
.rolled_back .why {
  color: #8a4b12;
}
.invalid .why,
.degraded .why,
.rejected .why {
  color: #a3332d;
}
.offline {
  background: #fafbfb;
}
.err {
  grid-area: err;
  margin-top: 6px;
}
.err summary {
  cursor: pointer;
  font-size: 12px;
  color: #6b7c80;
}
.err pre {
  white-space: pre-wrap;
  background: #16323a;
  color: #d9ecee;
  padding: 10px 12px;
  border-radius: 6px;
  margin: 6px 0 0;
}
</style>
