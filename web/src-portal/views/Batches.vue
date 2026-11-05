<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message, Modal } from 'antdv-next'
import dayjs from 'dayjs'
import { api, download, errMsg, http, readFile } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const rows = ref<any[]>([])
const loading = ref(false)
const approved = ref<any[]>([])
const pendingRevocations = ref(0)
const keys = ref<any[]>([])
const exportOpen = ref(false)
const selected = ref<number[]>([])
const kid = ref<string>()
const includeCRL = ref(true)
const importing = ref(false)

const batchStatus: Record<string, { t: string; c: string }> = {
  exported: { t: '等待离线签名', c: 'purple' },
  imported: { t: '已导入', c: 'green' },
  cancelled: { t: '已取消', c: 'default' },
}

async function load() {
  loading.value = true
  try {
    rows.value = await api.get('/batches')
  } finally {
    loading.value = false
  }
}
async function openExport() {
  const [lics, revs, ks] = await Promise.all([api.get('/licenses', { status: 'approved' }), api.get('/revocations'), api.get('/keys')])
  approved.value = lics
  pendingRevocations.value = revs.filter((r: any) => !r.crl_serial).length
  keys.value = ks.filter((k: any) => k.status === 'active')
  selected.value = lics.map((l: any) => l.id)
  kid.value = keys.value[0]?.kid
  includeCRL.value = pendingRevocations.value > 0
  exportOpen.value = true
}
const canExport = computed(() => !!kid.value && (selected.value.length > 0 || includeCRL.value))
async function doExport() {
  try {
    const b = await api.post('/batches', { kid: kid.value, license_ids: selected.value, include_crl: includeCRL.value })
    exportOpen.value = false
    message.success(`批次 ${b.batch_no} 已导出`)
    await load()
    await download(`/batches/${b.id}/request`, `sign-request-${b.batch_no}.json`)
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function doImport() {
  let body: string
  try {
    body = await readFile()
  } catch {
    return
  }
  importing.value = true
  try {
    const r = (await http.post('/batches/import', body, { headers: { 'Content-Type': 'application/json' } })).data
    const parts = [`批次 ${r.batch.batch_no} 已导入`]
    if (r.issued?.length) parts.push(`签发 ${r.issued.length} 张 License`)
    if (r.crl_serial) parts.push(`发布吊销列表 #${r.crl_serial}`)
    message.success(parts.join('，'))
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    importing.value = false
  }
}
function cancel(b: any) {
  Modal.confirm({
    title: `取消批次 ${b.batch_no}？`,
    content: '批次中的 License 回到“已审批”，吊销回到“待发布”。已经离线签好的结果将无法再导入。',
    okButtonProps: { danger: true },
    onOk: async () => {
      try {
        await api.post(`/batches/${b.id}/cancel`)
        message.success('已取消')
        await load()
      } catch (e) {
        message.error(errMsg(e))
      }
    },
  })
}
onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>签名批次</h1>
        <p>导出签名请求 → U 盘拷到离线签名机，运行离线签名工具 → 拷回 signed-*.json 并导入。</p>
      </div>
      <a-space v-if="session.has('signer')">
        <a-button :loading="importing" @click="doImport">导入签名结果</a-button>
        <a-button type="primary" @click="openExport">导出签名批次</a-button>
      </a-space>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" :loading="loading" size="middle" :scroll="{ x: 900 }"
        :columns="[
          { title: '批次', dataIndex: 'batch_no', width: 90 },
          { title: '状态', key: 'status', width: 130 },
          { title: '签名密钥', dataIndex: 'kid' },
          { title: 'License', dataIndex: 'licenses', width: 80 },
          { title: '吊销列表', key: 'crl', width: 90 },
          { title: '请求 SHA-256', key: 'sha', width: 150 },
          { title: '导出 / 导入', key: 'who', width: 220 },
          { title: '', key: 'op', width: 150 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'status'"><a-tag :color="batchStatus[record.status]?.c">{{ batchStatus[record.status]?.t || record.status }}</a-tag></template>
          <template v-else-if="column.key === 'crl'">{{ record.crl_serial ? '#' + record.crl_serial : '—' }}</template>
          <template v-else-if="column.key === 'sha'"><a-tooltip :title="record.request_sha256"><span class="num">{{ record.request_sha256.slice(0, 16) }}…</span></a-tooltip></template>
          <template v-else-if="column.key === 'who'">
            <div>{{ record.exported_by }} · {{ dayjs(record.exported_at).format('MM-DD HH:mm') }}</div>
            <div v-if="record.imported_at" class="muted">{{ record.imported_by }} · {{ dayjs(record.imported_at).format('MM-DD HH:mm') }}</div>
          </template>
          <template v-else-if="column.key === 'op' && session.has('signer')">
            <a-space :size="4">
              <a-button size="small" @click="download(`/batches/${record.id}/request`, `sign-request-${record.batch_no}.json`)">下载请求</a-button>
              <a-button v-if="record.status === 'exported'" size="small" danger @click="cancel(record)">取消</a-button>
            </a-space>
          </template>
        </template>
        <template #emptyText>还没有签名批次</template>
      </a-table>
    </section>

    <a-modal v-model:open="exportOpen" title="导出签名批次" :width="620" ok-text="导出并下载" :ok-button-props="{ disabled: !canExport }" @ok="doExport">
      <a-alert v-if="!keys.length" type="warning" show-icon style="margin-bottom: 12px" message="没有启用的签名公钥，请管理员先在“签名公钥”中登记。" />
      <a-form layout="vertical">
        <a-form-item label="签名密钥（离线签名机将使用对应的私钥文件）">
          <a-select v-model:value="kid" :options="keys.map((k) => ({ value: k.kid, label: `${k.kid} · ${k.fingerprint}` }))" />
        </a-form-item>
        <a-form-item :label="`已审批的 License（${approved.length}）`">
          <a-checkbox-group v-model:value="selected" style="display: grid; gap: 6px">
            <a-checkbox v-for="l in approved" :key="l.id" :value="l.id"><span class="num">{{ l.license_id }}</span> · {{ l.customer_name }}</a-checkbox>
          </a-checkbox-group>
          <div v-if="!approved.length" class="muted">没有等待签名的 License</div>
        </a-form-item>
        <a-form-item>
          <a-checkbox v-model:checked="includeCRL" :disabled="!pendingRevocations">同时签发新的吊销列表（待发布吊销 {{ pendingRevocations }} 条）</a-checkbox>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.muted {
  color: #6b7c80;
  font-size: 12px;
}
</style>
