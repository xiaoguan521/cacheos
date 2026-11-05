<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'
import ReleaseLanes from '../components/ReleaseLanes.vue'
import DiffView from '../components/DiffView.vue'
import { deployStatus, kindName } from '../components/status'

const route = useRoute()
const session = useSession()
const groups = ref<any[]>([])
const gid = ref(0)
const group = computed(() => groups.value.find((g) => g.id === gid.value))
const preview = ref<any>(null)
const previewErr = ref('')
const note = ref('')
const publishing = ref(false)
const deployments = ref<any[]>([])
const revisions = ref<any[]>([])
const active = ref<any>(null)
const openFiles = ref<string[]>([])
const approval = ref<{ enabled: boolean; licensed: boolean }>({ enabled: false, licensed: false })
const pendingReq = ref<any>(null) // 当前组待审批的申请（含差异）
const reqFiles = ref<string[]>([])
const reviewComment = ref('')
const reviewing = ref(false)
const recentReqs = ref<any[]>([])
const canaries = ref<any[]>([])
const canaryOpen = ref(false)
const canaryForm = ref({ percents: '10,50,100', observe_minutes: 5, max_5xx_percent: 5, min_requests: 50 })
const batchOpen = ref(false)
const batchGroups = ref<number[]>([])
const hasFeature = (f: string) => !!session.license?.features?.includes(f)
async function loadCanaries() {
  canaries.value = hasFeature('canary') ? await api.get('/canaries', { group_id: gid.value }) : []
}
async function startCanary() {
  try {
    await api.post(`/node-groups/${gid.value}/canary`, {
      note: note.value, percents: canaryForm.value.percents.split(',').map((x) => Number(x.trim())).filter(Boolean),
      observe_minutes: canaryForm.value.observe_minutes, max_5xx_percent: canaryForm.value.max_5xx_percent, min_requests: canaryForm.value.min_requests,
    })
    canaryOpen.value = false
    note.value = ''
    message.success('已开始灰度发布')
    await loadCanaries()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function batchPublish() {
  try {
    const r = await api.post('/releases/batch', { group_ids: batchGroups.value, note: note.value })
    batchOpen.value = false
    const ok = r.results.filter((x: any) => !x.error).length
    message.success(`${r.approval ? '已提交审批' : '已开始发布'}：${ok} / ${r.results.length} 个节点组`)
    r.results.filter((x: any) => x.error).forEach((x: any) => message.warning(`${groups.value.find((g) => g.id === x.group_id)?.name}：${x.error}`))
    await loadAll()
  } catch (e) {
    message.error(errMsg(e))
  }
}
let canaryPoll: number | undefined
const canaryStatus: Record<string, { t: string; c: string }> = {
  running: { t: '进行中', c: 'processing' }, done: { t: '已完成', c: 'green' }, rolled_back: { t: '已自动回滚', c: 'orange' }, failed: { t: '失败', c: 'red' },
}
const batchStatus: Record<string, string> = { pending: '等待', deploying: '发布中', observing: '观察中', passed: '通过', failed: '失败', rolled_back: '已回滚', skipped: '未执行' }

async function loadGroups() {
  groups.value = await api.get('/node-groups')
  if (!gid.value && groups.value.length) gid.value = groups.value[0].id
}
async function loadPreview() {
  previewErr.value = ''
  try {
    preview.value = await api.get(`/node-groups/${gid.value}/preview`)
    openFiles.value = (preview.value.files ?? []).filter((f: any) => f.path.startsWith('conf.d/')).map((f: any) => f.path)
  } catch (e) {
    preview.value = null
    previewErr.value = errMsg(e)
  }
}
async function loadHistory() {
  ;[deployments.value, revisions.value] = await Promise.all([api.get('/deployments', { group_id: gid.value }), api.get('/revisions', { group_id: gid.value })])
  if (!active.value && deployments.value.length) {
    const want = Number(route.query.d)
    active.value = deployments.value.find((d) => d.id === want) || deployments.value[0]
  }
}
async function loadApproval() {
  approval.value = await api.get('/release-approval')
  const reqs = await api.get('/release-requests', { group_id: gid.value })
  recentReqs.value = reqs.slice(0, 10)
  const p = reqs.find((r: any) => r.status === 'pending')
  pendingReq.value = p ? await api.get(`/release-requests/${p.id}`) : null
  reqFiles.value = (pendingReq.value?.files ?? []).filter((f: any) => f.path.startsWith('conf.d/')).map((f: any) => f.path)
}
async function loadAll() {
  await loadGroups()
  await Promise.all([loadPreview(), loadHistory(), loadApproval(), loadCanaries()])
}

let poll: number | undefined
function watchDeployment(id: number) {
  clearInterval(poll)
  const tick = async () => {
    active.value = await api.get(`/deployments/${id}`)
    if (active.value.status !== 'APPLYING') {
      clearInterval(poll)
      await loadGroups()
      await Promise.all([loadPreview(), loadHistory()])
      const s = active.value.status
      if (s === 'APPLIED') message.success(`${active.value.revision_id} 已在全部在线节点生效`)
      else if (s === 'FAILED' || s === 'PARTIAL') message.warning(`${active.value.revision_id} ${deployStatus[s].text}，详见各节点结果`)
    }
  }
  tick()
  poll = window.setInterval(tick, 700)
}

async function publish() {
  publishing.value = true
  try {
    if (approval.value.enabled) {
      await api.post(`/node-groups/${gid.value}/release-requests`, { note: note.value })
      note.value = ''
      message.success('已提交发布申请，等待另一位发布员审批')
      await loadApproval()
      return
    }
    const d = await api.post(`/node-groups/${gid.value}/publish`, { note: note.value })
    note.value = ''
    active.value = d
    watchDeployment(d.id)
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    publishing.value = false
  }
}

async function review(action: 'approve' | 'reject' | 'cancel') {
  reviewing.value = true
  try {
    const r = await api.post(`/release-requests/${pendingReq.value.id}/${action}`, { comment: reviewComment.value })
    reviewComment.value = ''
    if (action === 'approve') {
      active.value = r
      watchDeployment(r.id)
    } else {
      message.success(action === 'reject' ? '已驳回' : '已撤回')
    }
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    reviewing.value = false
    await Promise.all([loadApproval(), loadPreview()])
  }
}
const mine = computed(() => pendingReq.value?.requested_by === session.username)
const reqStatus: Record<string, { text: string; color: string }> = {
  pending: { text: '待审批', color: 'gold' },
  approved: { text: '已批准', color: 'green' },
  rejected: { text: '已驳回', color: 'red' },
  cancelled: { text: '已撤回', color: 'default' },
  stale: { text: '已过期', color: 'default' },
}

async function rollback(rev: string) {
  try {
    const d = await api.post(`/node-groups/${gid.value}/rollback`, { revision_id: rev })
    active.value = d
    watchDeployment(d.id)
  } catch (e) {
    message.error(errMsg(e))
  }
}

function show(d: any) {
  active.value = d
  if (d.status === 'APPLYING') watchDeployment(d.id)
}

onMounted(async () => {
  await loadAll()
  const running = deployments.value.find((d) => d.status === 'APPLYING')
  if (running) watchDeployment(running.id)
})
onUnmounted(() => {
  clearInterval(poll)
  clearInterval(canaryPoll)
})
canaryPoll = window.setInterval(() => {
  if (canaries.value.some((c) => c.status === 'running')) loadCanaries().then(loadHistory)
}, 2000)
watch(gid, () => {
  active.value = null
  loadPreview()
  loadHistory()
  loadApproval()
  loadCanaries()
})

const changeText: Record<string, string> = { added: '新增', removed: '删除', modified: '修改' }
// 只有曾经成功生效过的版本才提供回滚入口
function everApplied(rev: string) {
  return deployments.value.some((d) => d.revision_id === rev && (d.status === 'APPLIED' || d.status === 'PARTIAL'))
}
function lastDeploy(rev: string) {
  return deployments.value.find((d) => d.revision_id === rev)
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>发布</h1>
        <p>每次发布生成一个不可变版本。节点逐台校验、切换、加载并探活，任何一步失败都会自动回滚，流量不中断。</p>
      </div>
      <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" />
    </div>

    <section class="panel req" v-if="pendingReq">
      <div class="phead">
        <h2>待审批的发布申请 <span class="num">#{{ pendingReq.id }} · {{ pendingReq.revision_id }}</span></h2>
        <span class="cur">{{ pendingReq.requested_by }} 提交于 {{ dayjs(pendingReq.requested_at).format('MM-DD HH:mm') }}<template v-if="pendingReq.note">，{{ pendingReq.note }}</template></span>
      </div>
      <a-alert v-if="pendingReq.stale" type="warning" show-icon style="margin-bottom: 12px"
        message="提交后线上版本已变化（例如有人回滚），批准时会被拒绝；请撤回后重新提交。" />
      <p class="hint">批准后下发的就是提交时冻结的版本 {{ pendingReq.revision_id }}；提交之后草稿里的修改不会混进来。</p>
      <a-collapse v-model:active-key="reqFiles" :bordered="false" class="files">
        <a-collapse-panel v-for="f in pendingReq.files" :key="f.path">
          <template #header>
            <span class="fchg" :class="f.change">{{ changeText[f.change] }}</span>
            <span class="code">{{ f.path }}</span>
          </template>
          <DiffView v-if="f.diff && f.diff.startsWith('---')" :diff="f.diff" />
          <div v-else class="nodiff">{{ f.diff || '内容较长，未展开' }}</div>
        </a-collapse-panel>
      </a-collapse>
      <div class="pub" v-if="session.can('releaser')">
        <a-input v-model:value="reviewComment" placeholder="审批意见（可选，写入审计）" :maxlength="200" />
        <template v-if="!mine">
          <a-button danger :loading="reviewing" @click="review('reject')">驳回</a-button>
          <a-button type="primary" :loading="reviewing" :disabled="!session.writable || pendingReq.stale" @click="review('approve')">批准并发布</a-button>
        </template>
        <a-button v-if="mine || session.can('admin')" :loading="reviewing" @click="review('cancel')">撤回</a-button>
      </div>
      <div v-if="mine" class="hint">提交人不能审批自己的申请，请另一位发布员审批。</div>
    </section>

    <section class="panel">
      <div class="phead">
        <h2>待发布变更</h2>
        <span class="cur">线上版本 <b class="num">{{ group?.current_revision_id || '无' }}</b>，{{ group?.online ?? 0 }} / {{ group?.nodes ?? 0 }} 个节点在线</span>
      </div>
      <a-alert v-if="previewErr" type="error" :message="previewErr" show-icon />
      <template v-else-if="preview">
        <a-empty v-if="!preview.changed" :description="`草稿与线上版本一致，没有需要发布的变更`" />
        <template v-else>
          <a-collapse v-model:active-key="openFiles" :bordered="false" class="files">
            <a-collapse-panel v-for="f in preview.files" :key="f.path">
              <template #header>
                <span class="fchg" :class="f.change">{{ changeText[f.change] }}</span>
                <span class="code">{{ f.path }}</span>
              </template>
              <DiffView v-if="f.diff && f.diff.startsWith('---')" :diff="f.diff" />
              <div v-else class="nodiff">{{ f.diff || '内容较长，未展开' }}</div>
            </a-collapse-panel>
          </a-collapse>
          <div class="pub" v-if="session.can('releaser')">
            <a-input v-model:value="note" placeholder="发布说明，例如：首页改版" :maxlength="120" />
            <a-button type="primary" size="large" :loading="publishing" :disabled="!session.writable || (approval.enabled && !!pendingReq)" @click="publish">
              {{ approval.enabled ? '提交发布申请' : `发布到 ${group?.name}` }}
            </a-button>
          </div>
          <div class="more" v-if="session.can('releaser') && !approval.enabled">
            <a v-if="hasFeature('canary')" @click="canaryOpen = true">灰度发布…</a>
            <a v-if="hasFeature('batch_release') && groups.length > 1" @click="batchGroups = [gid]; batchOpen = true">批量发布到多个节点组…</a>
          </div>
          <div class="more" v-else-if="session.can('releaser') && hasFeature('batch_release') && groups.length > 1">
            <a @click="batchGroups = [gid]; batchOpen = true">批量提交多个节点组的发布申请…</a>
          </div>
          <div v-if="approval.enabled" class="hint">已开启发布审批：提交后由另一位发布员审批，批准后才下发。{{ pendingReq ? '当前已有待审批的申请。' : '' }}</div>
          <div v-if="!session.writable" class="ro">授权已失效，暂不能发布新版本；回滚不受影响。</div>
        </template>
      </template>
    </section>

    <section class="panel" v-if="active">
      <div class="phead">
        <h2>
          {{ kindName[active.kind] || active.kind }} <span class="num">{{ active.revision_id }}</span>
        </h2>
        <span class="cur">
          <a-tag :color="deployStatus[active.status]?.color">{{ deployStatus[active.status]?.text || active.status }}</a-tag>
          {{ active.created_by }}，{{ dayjs(active.started_at).format('MM-DD HH:mm:ss') }}<template v-if="active.note">，{{ active.note }}</template>
        </span>
      </div>
      <a-empty v-if="!active.nodes?.length" description="这个节点组还没有节点，版本已生成，节点接入后自动同步" />
      <ReleaseLanes v-else :nodes="active.nodes" :revision="active.revision_id" />
    </section>

    <section class="panel">
      <h2>版本历史</h2>
      <a-table :data-source="revisions" row-key="id" :pagination="false" size="middle" :columns="[{ title: '版本', key: 'id', width: 110 }, { title: '说明', key: 'note' }, { title: '最近一次下发', key: 'dep', width: 260 }, { title: '创建', key: 'created', width: 170 }, { title: '', key: 'ops', width: 130 }]">
        <template #emptyText><a-empty description="还没有版本" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'id'">
            <span class="num rev">{{ record.id }}</span>
            <a-tag v-if="record.id === group?.current_revision_id" color="cyan" style="margin-left: 6px">线上</a-tag>
          </template>
          <template v-else-if="column.key === 'note'">{{ record.note || '—' }}</template>
          <template v-else-if="column.key === 'dep'">
            <template v-if="lastDeploy(record.id)">
              <a @click="show(lastDeploy(record.id))">
                <span class="kd">{{ kindName[lastDeploy(record.id).kind] }}</span><a-tag :color="deployStatus[lastDeploy(record.id).status]?.color">{{ deployStatus[lastDeploy(record.id).status]?.text }}</a-tag>
              </a>
            </template>
          </template>
          <template v-else-if="column.key === 'created'">
            <div>{{ dayjs(record.created_at).format('MM-DD HH:mm') }}</div>
            <div class="sub">{{ record.created_by }}</div>
          </template>
          <template v-else-if="column.key === 'ops'">
            <a-popconfirm v-if="session.can('releaser') && record.id !== group?.current_revision_id && everApplied(record.id)" :title="`把 ${group?.name} 的全部节点回滚到 ${record.id}？`" ok-text="回滚" @confirm="rollback(record.id)">
              <a-button size="small">回滚到此版本</a-button>
            </a-popconfirm>
          </template>
        </template>
      </a-table>
    </section>

    <section class="panel" v-for="cy in canaries.slice(0, 3)" :key="'c' + cy.id">
      <div class="phead">
        <h2>灰度 #{{ cy.id }} <span class="num">{{ cy.revision_id }}</span></h2>
        <span class="cur"><a-tag :color="canaryStatus[cy.status]?.c">{{ canaryStatus[cy.status]?.t }}</a-tag>{{ cy.created_by }}，{{ dayjs(cy.created_at).format('MM-DD HH:mm') }}</span>
      </div>
      <p class="hint">{{ cy.message || `每批观察 ${Math.round(cy.options.observe / 6e10)} 分钟，5xx 占比超过 ${cy.options.max_5xx_percent}% 自动回滚到 ${cy.base_revision_id || '原版本'}` }}</p>
      <a-table :data-source="cy.batches" :row-key="(r: any) => r.nodes.join(',')" :pagination="false" size="small"
        :columns="[{ title: '批次', key: 'i', width: 70 }, { title: '节点数', key: 'n', width: 80 }, { title: '状态', key: 'st', width: 100 }, { title: '观察结果', key: 'r' }]">
        <template #bodyCell="{ column, record, index }">
          <template v-if="column.key === 'i'">{{ index + 1 }}</template>
          <template v-else-if="column.key === 'n'">{{ record.nodes.length }}</template>
          <template v-else-if="column.key === 'st'">{{ batchStatus[record.status] }}</template>
          <template v-else-if="column.key === 'r'">
            <span v-if="record.requests">{{ record.requests }} 请求，5xx {{ record.errors_5xx }}（{{ record.rate.toFixed(1) }}%）</span>
            <span class="sub"> {{ record.message }}</span>
          </template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="canaryOpen" title="灰度发布" ok-text="开始灰度" @ok="startCanary">
      <p class="hint">新版本按批次下发，每批观察后比较这些节点的 5xx 占比；超过阈值自动回滚已灰度的节点并停止，线上版本保持不变。</p>
      <a-form layout="vertical">
        <a-form-item label="批次（累计百分比）"><a-input v-model:value="canaryForm.percents" placeholder="10,50,100" /></a-form-item>
        <a-space>
          <a-form-item label="每批观察（分钟）"><a-input-number v-model:value="canaryForm.observe_minutes" :min="1" :max="120" /></a-form-item>
          <a-form-item label="5xx 阈值（%）"><a-input-number v-model:value="canaryForm.max_5xx_percent" :min="0.1" :max="100" /></a-form-item>
          <a-form-item label="最少请求数"><a-input-number v-model:value="canaryForm.min_requests" :min="1" /></a-form-item>
        </a-space>
        <a-form-item label="发布说明"><a-input v-model:value="note" /></a-form-item>
      </a-form>
    </a-modal>
    <a-modal v-model:open="batchOpen" title="批量发布" :ok-text="approval.enabled ? '逐组提交申请' : '逐组发布'" @ok="batchPublish">
      <a-checkbox-group v-model:value="batchGroups" :options="groups.map((g) => ({ value: g.id, label: g.name }))" />
      <a-input v-model:value="note" placeholder="发布说明" style="margin-top: 12px" />
    </a-modal>

    <section class="panel" v-if="recentReqs.length">
      <h2>发布申请记录</h2>
      <a-table :data-source="recentReqs" row-key="id" :pagination="false" size="middle"
        :columns="[{ title: '申请', key: 'id', width: 150 }, { title: '状态', key: 'st', width: 100 }, { title: '说明', dataIndex: 'note' }, { title: '提交', key: 'req', width: 170 }, { title: '审批', key: 'rev', width: 240 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'id'"><span class="num">#{{ record.id }} · {{ record.revision_id }}</span></template>
          <template v-else-if="column.key === 'st'"><a-tag :color="reqStatus[record.status]?.color">{{ reqStatus[record.status]?.text || record.status }}</a-tag></template>
          <template v-else-if="column.key === 'req'">
            <div>{{ record.requested_by }}</div>
            <div class="sub">{{ dayjs(record.requested_at).format('MM-DD HH:mm') }}</div>
          </template>
          <template v-else-if="column.key === 'rev'">
            <template v-if="record.reviewed_by">
              <div>{{ record.reviewed_by }}<span v-if="record.review_comment" class="sub">：{{ record.review_comment }}</span></div>
              <div class="sub">{{ dayjs(record.reviewed_at).format('MM-DD HH:mm') }}</div>
            </template>
          </template>
        </template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.phead {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 12px;
}
.phead h2 {
  margin: 0;
}
.cur {
  font-size: 13px;
  color: #6b7c80;
}
.cur b {
  color: #16323a;
}
.files :deep(.ant-collapse-item) {
  border-bottom: 1px solid #eef2f1;
}
.fchg {
  display: inline-block;
  width: 40px;
  font-size: 12px;
  font-weight: 600;
}
.fchg.added {
  color: #1f6f43;
}
.fchg.modified {
  color: #8a6510;
}
.fchg.removed {
  color: #a3332d;
}
.nodiff {
  font-size: 12px;
  color: #8a9a9d;
}
.pub {
  display: flex;
  gap: 12px;
  margin-top: 16px;
}
.ro {
  margin-top: 10px;
  font-size: 13px;
  color: #a3332d;
}
.hint {
  margin: 8px 0 0;
  font-size: 13px;
  color: #6b7c80;
}
.more {
  display: flex;
  gap: 16px;
  margin-top: 8px;
  font-size: 13px;
}
.req {
  border-color: #e3c56b;
}
.rev {
  font-weight: 600;
  color: #16323a;
}
.kd {
  margin-right: 8px;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
</style>
