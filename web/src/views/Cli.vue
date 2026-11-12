<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'
import { parseCommandLine } from '../utils/cliArgv'
import { formatReply, type ReplyLine, type ReplyNode } from '../utils/cliReply'

interface Entry {
  id: number
  command: string
  lines: ReplyLine[]
  truncated: boolean
}

const route = useRoute()
const router = useRouter()
const session = useSession()
const unit = ref<any>(null)
const instanceId = ref<number | undefined>(undefined)
const input = ref('')
const running = ref(false)
const entries = ref<Entry[]>([])
const outEl = ref<HTMLElement | null>(null)
const id = computed(() => String(route.params.id))
const servers = computed(() => (unit.value?.instances || []).filter((x: any) => x.kind === 'server'))

// 命令历史只保存在页面内存中（不写 localStorage）。
const history: string[] = []
let historyPos = -1
let nextId = 1

async function load() {
  try {
    unit.value = await api.get(`/units/${id.value}`)
    if (instanceId.value === undefined && servers.value.length) instanceId.value = servers.value[0].id
  } catch (e) {
    message.error(errMsg(e))
  }
}

function scrollToEnd() {
  nextTick(() => {
    if (outEl.value) outEl.value.scrollTop = outEl.value.scrollHeight
  })
}

async function submit() {
  const text = input.value.trim()
  if (!text || running.value) return
  if (instanceId.value === undefined) {
    message.warning('请先选择实例')
    return
  }
  let argv: string[]
  try {
    argv = parseCommandLine(text)
  } catch (e) {
    message.error(errMsg(e))
    return
  }
  history.push(text)
  historyPos = -1
  input.value = ''
  running.value = true
  const entry: Entry = { id: nextId++, command: text, lines: [], truncated: false }
  entries.value.push(entry)
  scrollToEnd()
  try {
    const r = await api.post(`/units/${id.value}/exec?instance_id=${encodeURIComponent(String(instanceId.value))}`, { argv })
    entry.lines = formatReply(r.reply as ReplyNode)
    entry.truncated = !!r.truncated
  } catch (e: any) {
    const code = e?.response?.data?.code
    entry.lines = [{ text: `(error) ${code ? `${code}: ` : ''}${errMsg(e)}`, tone: 'error' }]
  } finally {
    running.value = false
    scrollToEnd()
  }
}

// 上下键翻阅本次页面内的命令历史。
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (history.length === 0) return
    historyPos = historyPos < 0 ? history.length - 1 : Math.max(0, historyPos - 1)
    input.value = history[historyPos]
  } else if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (historyPos < 0) return
    if (historyPos < history.length - 1) {
      historyPos++
      input.value = history[historyPos]
    } else {
      historyPos = -1
      input.value = ''
    }
  }
}

function clearScreen() {
  entries.value = []
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <a @click="router.push({ name: 'unit-detail', params: { id } })">← {{ unit?.name || '部署单元' }}</a>
        <h1>Web 命令行</h1>
        <p>只允许数据命令：管理、配置、脚本、阻塞、订阅、事务与连接类命令一律拒绝。每条命令需要二次验证并记审计，单条超时 5 秒，返回超过 256 KiB 将截断。</p>
      </div>
    </div>

    <section v-if="!session.cliExec" class="panel">
      <a-result status="403" title="未获得命令行授权" sub-title="未获得命令行授权，请联系管理员" />
    </section>

    <template v-else>
      <section class="panel controls">
        <a-select v-model:value="instanceId" style="min-width: 220px" placeholder="选择实例" :disabled="servers.length === 0"
          :options="servers.map((s: any) => ({ value: s.id, label: `端口 ${s.port} · ${s.role || '—'} · ${s.state || '—'}` }))" />
        <a-button @click="clearScreen">清屏</a-button>
        <span class="muted">↑ ↓ 翻阅本页命令历史</span>
      </section>

      <section class="panel">
        <div class="out" ref="outEl">
          <div v-if="entries.length === 0" class="muted">输入数据命令后回车执行，例如：GET app:user:1</div>
          <div v-for="e in entries" :key="e.id" class="entry">
            <div class="cmd">$ {{ e.command }}</div>
            <div v-for="(l, i) in e.lines" :key="i" :class="['line', l.tone]">{{ l.text }}</div>
            <div v-if="e.truncated" class="note">输出已截断：超过 256 KiB 的部分未显示</div>
          </div>
          <div v-if="running" class="muted">执行中…</div>
        </div>
        <div class="prompt" @keydown="onKeydown">
          <a-input v-model:value="input" :disabled="running || servers.length === 0" placeholder='例如：SET app:k "hello world"' @press-enter="submit" />
          <a-button type="primary" :loading="running" :disabled="!input.trim() || servers.length === 0" @click="submit">执行</a-button>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.muted { color: #7b8b8e; font-size: 12px; }
.out { height: 480px; overflow: auto; background: #f4f7f7; padding: 12px; border-radius: 6px; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 13px; margin-bottom: 12px; }
.entry { margin-bottom: 10px; }
.cmd { font-weight: 600; word-break: break-all; }
.line { white-space: pre-wrap; word-break: break-word; }
.line.error { color: #c0392b; }
.line.muted { color: #7b8b8e; }
.note { color: #b7791f; font-size: 12px; margin-top: 4px; }
.prompt { display: flex; gap: 12px; align-items: center; }
.prompt :deep(.ant-input), .prompt > :first-child { flex: 1; }
</style>
