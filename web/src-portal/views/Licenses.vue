<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message, Modal } from 'antdv-next'
import dayjs from 'dayjs'
import { api, download, errMsg, readFile } from '../api/client'
import { editionName, statusColor, statusName, useSession } from '../stores/session'

const route = useRoute()
const router = useRouter()
const session = useSession()
const status = ref(String(route.query.status || ''))
const rows = ref<any[]>([])
const loading = ref(false)
const detail = ref<any>(null)

const customers = ref<any[]>([])
const orders = ref<any[]>([])
const draftOpen = ref(false)
const draft = reactive({ customer_id: undefined as number | undefined, order_id: undefined as number | undefined, activation_request: '', installation_id: '', binding: 'installation', expires_at: '' })

const renewOpen = ref(false)
const renewOf = ref<any>(null)
const renewOrder = ref<number>()

async function load() {
  loading.value = true
  try {
    rows.value = await api.get('/licenses', status.value ? { status: status.value } : undefined)
  } finally {
    loading.value = false
  }
}
watch(status, (s) => {
  router.replace({ query: s ? { status: s } : {} })
  load()
})

async function loadOrders(cid?: number) {
  orders.value = cid ? await api.get('/orders', { customer_id: cid }) : []
}
async function openDraft() {
  customers.value = await api.get('/customers')
  Object.assign(draft, { customer_id: undefined, order_id: undefined, activation_request: '', installation_id: '', binding: 'installation', expires_at: '' })
  orders.value = []
  draftOpen.value = true
}
async function pasteRequest() {
  try {
    draft.activation_request = await readFile()
  } catch {
    /* 取消选择 */
  }
}
async function saveDraft() {
  try {
    const { customer_id: _c, ...body } = draft
    await api.post('/licenses', body)
    message.success('草稿已创建')
    draftOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function act(r: any, action: string, okText: string, body?: any) {
  try {
    await api.post(`/licenses/${r.id}/${action}`, body)
    message.success(okText)
    await load()
    if (detail.value?.id === r.id) detail.value = await api.get(`/licenses/${r.id}`)
  } catch (e) {
    message.error(errMsg(e))
  }
}
const reasonOpen = ref(false)
const reasonFor = ref<{ r: any; action: 'reject' | 'revoke' } | null>(null)
const reasonText = ref('')
function withReason(r: any, action: 'reject' | 'revoke') {
  reasonFor.value = { r, action }
  reasonText.value = ''
  reasonOpen.value = true
}
async function doReason() {
  if (!reasonFor.value) return
  if (!reasonText.value.trim()) {
    message.warning('请填写原因')
    return
  }
  const { r, action } = reasonFor.value
  await act(r, action, action === 'reject' ? '已驳回' : '已申请吊销', { reason: reasonText.value.trim() })
  reasonOpen.value = false
}
async function del(r: any) {
  Modal.confirm({
    title: `删除草稿 ${r.license_id}？`,
    okButtonProps: { danger: true },
    onOk: async () => {
      try {
        await api.del(`/licenses/${r.id}`)
        message.success('已删除')
        await load()
      } catch (e) {
        message.error(errMsg(e))
      }
    },
  })
}
async function openRenew(r: any) {
  renewOf.value = r
  renewOrder.value = undefined
  await loadOrders(r.customer_id)
  renewOpen.value = true
}
async function doRenew() {
  try {
    await api.post(`/licenses/${renewOf.value.id}/renew`, { order_id: renewOrder.value })
    message.success('续期草稿已创建')
    renewOpen.value = false
    status.value = 'draft'
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function show(r: any) {
  detail.value = await api.get(`/licenses/${r.id}`)
}
async function dl(r: any) {
  try {
    await download(`/licenses/${r.id}/download`, `${r.license_id}.lic`)
  } catch (e) {
    message.error(errMsg(e))
  }
}
const statusOptions = computed(() => [{ value: '', label: '全部' }, ...Object.entries(statusName).map(([value, label]) => ({ value, label }))])
const mine = (r: any) => r.created_by === session.me?.username
onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>License</h1>
        <p>审批人不能是起草人或提交人；审批后由签名员导出批次、在离线签名机签名。</p>
      </div>
      <a-space>
        <a-select v-model:value="status" :options="statusOptions" style="width: 140px" />
        <a-button v-if="session.has('sales')" type="primary" @click="openDraft">起草 License</a-button>
      </a-space>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" :loading="loading" size="middle" :scroll="{ x: 960 }"
        :columns="[
          { title: 'License', key: 'id', width: 190 },
          { title: '客户', dataIndex: 'customer_name' },
          { title: '版本', key: 'edition', width: 90 },
          { title: '状态', key: 'status', width: 120 },
          { title: '到期', key: 'exp', width: 110 },
          { title: '起草 / 审批', key: 'who', width: 150 },
          { title: '操作', key: 'op', width: 230 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'id'">
            <a @click="show(record)" class="num">{{ record.license_id }}</a>
            <div v-if="record.parent_license_id" class="muted">续期自 {{ record.parent_license_id }}</div>
          </template>
          <template v-else-if="column.key === 'edition'">{{ editionName[record.payload?.edition] || record.payload?.edition }}</template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="statusColor[record.status]">{{ statusName[record.status] }}</a-tag>
            <a-tag v-if="record.revoking" color="orange">吊销待发布</a-tag>
          </template>
          <template v-else-if="column.key === 'exp'">{{ dayjs(record.expires_at).format('YYYY-MM-DD') }}</template>
          <template v-else-if="column.key === 'who'">{{ record.created_by }}<span v-if="record.approved_by"> / {{ record.approved_by }}</span></template>
          <template v-else-if="column.key === 'op'">
            <a-space wrap :size="4">
              <template v-if="record.status === 'draft' && session.has('sales')">
                <a-button size="small" type="primary" @click="act(record, 'submit', '已提交审批')">提交</a-button>
                <a-button size="small" danger @click="del(record)">删除</a-button>
              </template>
              <template v-if="record.status === 'rejected' && session.has('sales')">
                <a-button size="small" @click="act(record, 'submit', '已重新提交')">重新提交</a-button>
              </template>
              <template v-if="record.status === 'submitted' && session.has('business')">
                <a-tooltip :title="mine(record) ? '审批人不能是起草人' : ''">
                  <a-button size="small" type="primary" :disabled="mine(record)" @click="act(record, 'approve', '已审批')">审批</a-button>
                </a-tooltip>
                <a-button size="small" danger @click="withReason(record, 'reject')">驳回</a-button>
              </template>
              <template v-if="record.status === 'issued'">
                <a-button size="small" @click="dl(record)">下载</a-button>
                <a-button v-if="session.has('sales')" size="small" @click="openRenew(record)">续期</a-button>
                <a-button v-if="session.has('business') && !record.revoking" size="small" danger @click="withReason(record, 'revoke')">吊销</a-button>
              </template>
            </a-space>
          </template>
        </template>
        <template #emptyText>没有符合条件的 License</template>
      </a-table>
    </section>

    <a-drawer :open="!!detail" :width="520" :title="detail?.license_id" @close="detail = null">
      <template v-if="detail">
        <a-descriptions :column="1" size="small" bordered>
          <a-descriptions-item label="状态"><a-tag :color="statusColor[detail.status]">{{ statusName[detail.status] }}</a-tag></a-descriptions-item>
          <a-descriptions-item label="客户">{{ detail.customer_name }}（{{ detail.customer_code }}）</a-descriptions-item>
          <a-descriptions-item label="订单">{{ detail.order_no }}</a-descriptions-item>
          <a-descriptions-item label="版本">{{ editionName[detail.payload?.edition] }}</a-descriptions-item>
          <a-descriptions-item label="节点 / 站点">{{ detail.payload?.limits?.nodes || '不限' }} / {{ detail.payload?.limits?.sites || '不限' }}</a-descriptions-item>
          <a-descriptions-item label="生效 → 到期">{{ dayjs(detail.payload?.not_before).format('YYYY-MM-DD') }} → {{ dayjs(detail.expires_at).format('YYYY-MM-DD') }}（宽限 {{ detail.payload?.grace_days }} 天）</a-descriptions-item>
          <a-descriptions-item label="安装 ID"><span class="num">{{ detail.installation_id }}</span></a-descriptions-item>
          <a-descriptions-item label="绑定方式">{{ detail.binding === 'installation_only' ? '仅安装 ID' : '安装 ID + 机器指纹' }}</a-descriptions-item>
          <a-descriptions-item label="功能">{{ (detail.payload?.features || []).join('、') }}</a-descriptions-item>
          <a-descriptions-item label="签名密钥">{{ detail.payload?.kid || '导出批次时确定' }}</a-descriptions-item>
          <a-descriptions-item label="起草 / 提交 / 审批">{{ detail.created_by }} / {{ detail.submitted_by || '—' }} / {{ detail.approved_by || '—' }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.reject_reason" label="驳回原因">{{ detail.reject_reason }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.issued_at" label="签发时间">{{ dayjs(detail.issued_at).format('YYYY-MM-DD HH:mm') }}</a-descriptions-item>
          <a-descriptions-item v-if="detail.revoked_at" label="吊销">{{ dayjs(detail.revoked_at).format('YYYY-MM-DD HH:mm') }} · {{ detail.revoke_reason }}</a-descriptions-item>
        </a-descriptions>
      </template>
    </a-drawer>

    <a-modal v-model:open="draftOpen" title="起草 License" :width="560" @ok="saveDraft">
      <a-form layout="vertical">
        <a-form-item label="客户">
          <a-select v-model:value="draft.customer_id" show-search option-filter-prop="label"
            :options="customers.map((c) => ({ value: c.id, label: `${c.code} ${c.name}` }))"
            @change="(v: any) => { draft.order_id = undefined; loadOrders(v) }" />
        </a-form-item>
        <a-form-item label="订单">
          <a-select v-model:value="draft.order_id" :disabled="!draft.customer_id"
            :options="orders.map((o) => ({ value: o.id, label: `${o.order_no} · ${editionName[o.edition]} · ${o.days} 天` }))" />
        </a-form-item>
        <a-form-item label="离线激活申请（客户控制台导出的 activation-request.json）">
          <a-textarea v-model:value="draft.activation_request" :rows="4" placeholder="粘贴内容，或点击下方按钮选择文件" />
          <a-button size="small" style="margin-top: 6px" @click="pasteRequest">选择文件</a-button>
        </a-form-item>
        <a-form-item label="或只填安装 ID（无激活申请时，绑定方式为“仅安装 ID”）">
          <a-input v-model:value="draft.installation_id" :disabled="!!draft.activation_request" />
        </a-form-item>
        <a-form-item label="绑定方式">
          <a-radio-group v-model:value="draft.binding">
            <a-radio value="installation">安装 ID + 机器指纹</a-radio>
            <a-radio value="installation_only">仅安装 ID</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item label="指定到期日（可选，默认按订单天数）">
          <a-input v-model:value="draft.expires_at" placeholder="YYYY-MM-DD" />
        </a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="reasonOpen" :title="reasonFor?.action === 'revoke' ? `吊销 ${reasonFor?.r.license_id}` : `驳回 ${reasonFor?.r.license_id}`"
      :ok-text="reasonFor?.action === 'revoke' ? '申请吊销' : '驳回'" :ok-button-props="{ danger: true }" @ok="doReason">
      <p v-if="reasonFor?.action === 'revoke'" class="muted">吊销会写入下一个签名批次的吊销列表；客户控制台导入新的吊销列表后生效。</p>
      <a-textarea v-model:value="reasonText" :rows="3" placeholder="原因（必填，写入审计）" />
    </a-modal>

    <a-modal v-model:open="renewOpen" :title="`续期 ${renewOf?.license_id || ''}`" @ok="doRenew">
      <p class="muted">续期沿用原 License 的安装 ID 与绑定方式，新到期日从原到期日（或今天，取较晚者）起算；生成草稿后照常审批、签名。</p>
      <a-select v-model:value="renewOrder" style="width: 100%" placeholder="选择续期订单"
        :options="orders.map((o) => ({ value: o.id, label: `${o.order_no} · ${editionName[o.edition]} · ${o.days} 天` }))" />
    </a-modal>
  </div>
</template>

<style scoped>
.muted {
  color: #6b7c80;
  font-size: 12px;
}
</style>
