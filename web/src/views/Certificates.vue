<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const certs = ref<any[]>([])
const loading = ref(false)

const acme = ref<any>({ settings: {}, orders: [] })
const pending = computed(() => acme.value.orders.some((o: any) => o.status === 'pending'))

async function load() {
  loading.value = true
  try {
    ;[certs.value, acme.value] = await Promise.all([api.get('/certificates'), api.get('/acme')])
  } finally {
    loading.value = false
  }
}
// 有签发进行中时每 3 秒刷新进度
let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(() => pending.value && load(), 3000)
})
onUnmounted(() => clearInterval(timer))

// ACME 签发
const issueOpen = ref(false)
const issueForm = reactive({ name: '', domains: [] as string[] })
const issuing = ref(false)
const issueError = ref('')
function openIssue() {
  Object.assign(issueForm, { name: '', domains: [] })
  issueError.value = ''
  issueOpen.value = true
}
async function issue() {
  issuing.value = true
  issueError.value = ''
  try {
    await api.post('/certificates/acme', issueForm)
    message.success('已开始签发，下方“ACME 签发记录”可查看进度')
    issueOpen.value = false
    load()
  } catch (e) {
    issueError.value = errMsg(e)
  } finally {
    issuing.value = false
  }
}
async function acmeRenew(c: any) {
  try {
    await api.post(`/certificates/${c.id}/acme-renew`)
    message.success(`已开始为 ${c.name} 续期`)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

// ACME 设置
const settingsOpen = ref(false)
const settingsForm = reactive({ directory: '', email: '' })
function openSettings() {
  Object.assign(settingsForm, { directory: acme.value.settings.directory, email: acme.value.settings.email })
  settingsOpen.value = true
}
async function saveSettings() {
  try {
    await api.put('/acme', settingsForm)
    message.success('已保存')
    settingsOpen.value = false
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
const orderStatus: Record<string, { text: string; color: string }> = {
  pending: { text: '进行中', color: 'gold' },
  valid: { text: '成功', color: 'green' },
  failed: { text: '失败', color: 'red' },
}

// 上传 / 续期共用一个表单：renewing 为空表示上传新证书
const open = ref(false)
const renewing = ref<any>(null)
const saving = ref(false)
const error = ref('')
const form = reactive({ name: '', cert_pem: '', key_pem: '' })

function openUpload() {
  renewing.value = null
  Object.assign(form, { name: '', cert_pem: '', key_pem: '' })
  error.value = ''
  open.value = true
}
function openRenew(c: any) {
  renewing.value = c
  Object.assign(form, { name: c.name, cert_pem: '', key_pem: '' })
  error.value = ''
  open.value = true
}

// 从本地文件读取 PEM，避免手工复制粘贴出错
function readFile(field: 'cert_pem' | 'key_pem') {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.pem,.crt,.cer,.key,.txt'
  input.onchange = async () => {
    const file = input.files?.[0]
    if (file) form[field] = await file.text()
  }
  input.click()
}

function describeDeploys(list: any[]) {
  if (!list?.length) return '没有站点在使用，无需下发。'
  const started = list.filter((d) => d.status === 'started').length
  const busy = list.filter((d) => d.status === 'busy').length
  const failed = list.filter((d) => d.status === 'failed')
  const parts = []
  if (started) parts.push(`已向 ${started} 个节点组下发新证书`)
  if (busy) parts.push(`${busy} 个节点组正在发布，请稍后点“重新下发”`)
  if (failed.length) parts.push(`${failed.length} 个节点组下发失败：${failed[0].message}`)
  if (!parts.length) parts.push('线上版本中没有需要替换的证书')
  return parts.join('；')
}

async function submit() {
  error.value = ''
  saving.value = true
  try {
    if (renewing.value) {
      const r = await api.put(`/certificates/${renewing.value.id}`, { cert_pem: form.cert_pem, key_pem: form.key_pem })
      message.success(`证书已更新。${describeDeploys(r.deployments)}`, 6)
    } else {
      await api.post('/certificates', form)
      message.success('证书已上传，可在站点的 HTTPS 设置中选择')
    }
    open.value = false
    load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

// 国密 TLCP 双证书：签名证书 + 加密证书各带私钥（需要企业版 gm_tls 授权；续期在任何授权状态下可用）
const gmLicensed = computed(() => !!session.license?.features?.includes('gm_tls'))
const gmOpen = ref(false)
const gmRenewing = ref<any>(null)
const gmForm = reactive({ name: '', sign_cert_pem: '', sign_key_pem: '', enc_cert_pem: '', enc_key_pem: '' })
const gmFields = [
  { key: 'sign_cert_pem', label: '签名证书（PEM，可包含中间证书）', ph: '-----BEGIN CERTIFICATE-----' },
  { key: 'sign_key_pem', label: '签名私钥（PEM，不能带密码）', ph: '-----BEGIN PRIVATE KEY-----' },
  { key: 'enc_cert_pem', label: '加密证书（PEM，可包含中间证书）', ph: '-----BEGIN CERTIFICATE-----' },
  { key: 'enc_key_pem', label: '加密私钥（PEM，不能带密码）', ph: '-----BEGIN PRIVATE KEY-----' },
] as const
type GMField = (typeof gmFields)[number]['key']
function openGM(c?: any) {
  gmRenewing.value = c || null
  Object.assign(gmForm, { name: c?.name || '', sign_cert_pem: '', sign_key_pem: '', enc_cert_pem: '', enc_key_pem: '' })
  error.value = ''
  gmOpen.value = true
}
function readGMFile(field: GMField) {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = '.pem,.crt,.cer,.key,.txt'
  input.onchange = async () => {
    const file = input.files?.[0]
    if (file) gmForm[field] = await file.text()
  }
  input.click()
}
async function submitGM() {
  error.value = ''
  saving.value = true
  try {
    const { name, ...pair } = gmForm
    if (gmRenewing.value) {
      const r = await api.put(`/certificates/${gmRenewing.value.id}/gm`, pair)
      message.success(`国密证书已更新。${describeDeploys(r.deployments)}`, 6)
    } else {
      await api.post('/certificates/gm', { name, ...pair })
      message.success('国密双证书已上传，可在站点的 HTTPS 设置中作为“国密证书”选择')
    }
    gmOpen.value = false
    load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

async function redeploy(c: any) {
  try {
    const r = await api.post(`/certificates/${c.id}/deploy`)
    message.success(describeDeploys(r.deployments), 6)
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function remove(c: any) {
  try {
    await api.del(`/certificates/${c.id}`)
    message.success(`已删除证书 ${c.name}`)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

const statusTag: Record<string, { text: string; color: string }> = {
  valid: { text: '有效', color: 'green' },
  expiring: { text: '即将到期', color: 'gold' },
  expired: { text: '已过期', color: 'red' },
}

const columns = [
  { title: '证书', key: 'name' },
  { title: '域名', key: 'domains' },
  { title: '到期', key: 'expiry', width: 170 },
  { title: '使用站点', key: 'sites', width: 160 },
  { title: '', key: 'ops', width: 200 },
]
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>证书</h1>
        <p>站点从这里选择证书。私钥加密保存，任何页面都不会回显。证书续期在授权过期后仍可使用，续期后自动下发到引用它的节点组。</p>
      </div>
      <a-space v-if="session.can('operator')">
        <a-button :disabled="!session.writable" @click="openUpload">上传证书</a-button>
        <a-tooltip :title="gmLicensed ? '' : '国密 TLCP 需要企业版授权'">
          <a-button :disabled="!session.writable || !gmLicensed" @click="openGM()">上传国密证书</a-button>
        </a-tooltip>
        <a-button type="primary" :disabled="!session.writable || pending" @click="openIssue">自动申请证书</a-button>
      </a-space>
    </div>

    <div class="panel">
      <a-table :columns="columns" :data-source="certs" row-key="id" :pagination="false" :loading="loading">
        <template #emptyText>
          <a-empty description="还没有证书。上传证书与私钥（PEM 格式）后，就能在站点中启用 HTTPS。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="nm">{{ record.name }}</div>
            <div class="sub">签发者 {{ record.issuer || '—' }}<span v-if="record.source === 'migrated'"> · 由旧版本站点迁移</span></div>
            <a-tag v-if="record.source === 'acme'" color="blue" class="src">ACME{{ record.auto_renew ? ' · 自动续期' : '' }}</a-tag>
            <a-tag v-if="record.kind === 'sm2'" color="red" class="src">国密 SM2 · 签名 + 加密</a-tag>
          </template>
          <template v-else-if="column.key === 'domains'">
            <span v-for="d in record.domains" :key="d" class="dm code">{{ d }}</span>
          </template>
          <template v-else-if="column.key === 'expiry'">
            <a-tag :color="statusTag[record.status].color">{{ statusTag[record.status].text }}</a-tag>
            <div class="sub num">{{ dayjs(record.not_after).format('YYYY-MM-DD') }}<template v-if="record.status !== 'expired'">，剩 {{ record.days_left }} 天</template></div>
          </template>
          <template v-else-if="column.key === 'sites'">
            <span v-if="!record.sites" class="sub">未使用</span>
            <span v-else>{{ record.site_list.join('、') }}</span>
          </template>
          <template v-else-if="column.key === 'ops'">
            <a-space v-if="session.can('operator')" :size="0">
              <a-button v-if="record.source === 'acme'" type="link" size="small" :disabled="pending" @click="acmeRenew(record)">立即续期</a-button>
              <a-button v-else-if="record.kind === 'sm2'" type="link" size="small" @click="openGM(record)">续期</a-button>
              <a-button v-else type="link" size="small" @click="openRenew(record)">续期</a-button>
              <a-button v-if="record.sites" type="link" size="small" @click="redeploy(record)">重新下发</a-button>
              <a-popconfirm v-if="!record.sites" :title="`删除证书 ${record.name}？`" ok-text="删除" @confirm="remove(record)">
                <a-button type="link" danger size="small" :disabled="!session.writable">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </div>

    <div class="panel">
      <div class="ahead">
        <h2>ACME 签发记录</h2>
        <span class="sub">CA：<span class="code">{{ acme.settings.directory }}</span><template v-if="acme.settings.email"> · {{ acme.settings.email }}</template></span>
        <a-button v-if="session.can('admin')" size="small" @click="openSettings">设置</a-button>
      </div>
      <a-empty v-if="!acme.orders.length" description="还没有签发记录。自动申请需要控制台能访问 CA，并且域名已有发布到节点的站点（CA 会通过 80 端口访问节点验证）。到期前 30 天自动续期。" />
      <a-table v-else :data-source="acme.orders" row-key="id" :pagination="false" size="small"
        :columns="[{ title: '证书', key: 'name' }, { title: '域名', key: 'domains' }, { title: '状态', key: 'status', width: 300 }, { title: '时间', key: 'time', width: 150 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">{{ record.name }}<span class="sub"> · {{ record.cert_id && record.created_by.startsWith('system') ? '自动续期' : record.created_by }}</span></template>
          <template v-else-if="column.key === 'domains'"><span v-for="d in record.domains" :key="d" class="dm code">{{ d }}</span></template>
          <template v-else-if="column.key === 'status'">
            <a-tag :color="orderStatus[record.status]?.color">{{ orderStatus[record.status]?.text }}</a-tag>
            <span v-if="record.status === 'pending'" class="sub">{{ record.step }}…</span>
            <div v-if="record.error" class="err">{{ record.error }}</div>
          </template>
          <template v-else-if="column.key === 'time'"><span class="sub num">{{ dayjs(record.created_at).format('MM-DD HH:mm:ss') }}</span></template>
        </template>
      </a-table>
    </div>

    <a-modal v-model:open="issueOpen" title="自动申请证书（ACME）" ok-text="开始申请" :confirm-loading="issuing" width="600px" @ok="issue">
      <a-alert type="info" show-icon style="margin-bottom: 16px" message="控制台向 CA 申请证书，CA 通过 80 端口访问节点上的验证文件。每个域名都要有已发布到节点的站点；证书签发后自动下发，并在到期前 30 天自动续期。" />
      <a-form layout="vertical">
        <a-form-item label="名称" extra="小写字母、数字、连字符"><a-input v-model:value="issueForm.name" placeholder="shop-example-com" /></a-form-item>
        <a-form-item label="域名" extra="回车添加；HTTP-01 验证不支持通配符"><a-select v-model:value="issueForm.domains" mode="tags" :open="false" placeholder="shop.example.com" /></a-form-item>
      </a-form>
      <a-alert v-if="issueError" type="error" :message="issueError" show-icon />
    </a-modal>

    <a-modal v-model:open="settingsOpen" title="ACME 设置" ok-text="保存" @ok="saveSettings">
      <a-form layout="vertical">
        <a-form-item label="ACME 服务地址" extra="默认 Let's Encrypt。内网自建的 ACME 服务（如 step-ca）填写其 directory 地址，CA 证书通过控制台的 ETOS_ACME_CA_FILE 配置。">
          <a-input v-model:value="settingsForm.directory" class="code" />
        </a-form-item>
        <a-form-item label="联系邮箱" extra="CA 用于发送到期等通知，可不填"><a-input v-model:value="settingsForm.email" /></a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="open" :title="renewing ? `续期证书 ${renewing.name}` : '上传证书'" :ok-text="renewing ? '续期并下发' : '上传'" :confirm-loading="saving" width="680px" @ok="submit">
      <a-alert v-if="renewing" type="info" show-icon style="margin-bottom: 16px" :message="`新证书必须仍包含 ${renewing.site_list.length ? '站点使用的全部域名' : '原有域名'}。续期后会以线上版本为基础只替换证书文件，生成新版本下发，不会带上未发布的草稿改动。`" />
      <a-form layout="vertical">
        <a-form-item v-if="!renewing" label="名称" extra="小写字母、数字、连字符，例如 shop-example-com">
          <a-input v-model:value="form.name" placeholder="shop-example-com" />
        </a-form-item>
        <a-form-item label="证书（PEM，可包含中间证书）">
          <template #extra><a @click="readFile('cert_pem')">从文件读取</a></template>
          <a-textarea v-model:value="form.cert_pem" :rows="5" class="code" placeholder="-----BEGIN CERTIFICATE-----" />
        </a-form-item>
        <a-form-item label="私钥（PEM，不能带密码）">
          <template #extra><a @click="readFile('key_pem')">从文件读取</a></template>
          <a-textarea v-model:value="form.key_pem" :rows="5" class="code" placeholder="-----BEGIN PRIVATE KEY-----" />
        </a-form-item>
      </a-form>
      <a-alert v-if="error" type="error" :message="error" show-icon />
    </a-modal>

    <a-modal v-model:open="gmOpen" :title="gmRenewing ? `续期国密证书 ${gmRenewing.name}` : '上传国密双证书'" :ok-text="gmRenewing ? '续期并下发' : '上传'" :confirm-loading="saving" width="720px" @ok="submitGM">
      <a-alert type="info" show-icon style="margin-bottom: 16px"
        :message="gmRenewing ? '签名证书必须仍包含站点使用的全部域名。续期后只替换国密证书文件，生成新版本下发。' : '国密 TLCP 使用两张 SM2 证书：签名证书（digitalSignature）与加密证书（keyEncipherment）。站点仍需一张标准证书服务不支持国密的客户端；国密证书只能下发到国密版 syapp 节点。'" />
      <a-form layout="vertical">
        <a-form-item v-if="!gmRenewing" label="名称" extra="小写字母、数字、连字符，例如 gov-example-sm2">
          <a-input v-model:value="gmForm.name" placeholder="gov-example-sm2" />
        </a-form-item>
        <a-row :gutter="16">
          <a-col v-for="f in gmFields" :key="f.key" :xs="24" :md="12">
            <a-form-item :label="f.label">
              <template #extra><a @click="readGMFile(f.key)">从文件读取</a></template>
              <a-textarea v-model:value="gmForm[f.key]" :rows="4" class="code" :placeholder="f.ph" />
            </a-form-item>
          </a-col>
        </a-row>
      </a-form>
      <a-alert v-if="error" type="error" :message="error" show-icon />
    </a-modal>
  </div>
</template>

<style scoped>
.nm {
  font-weight: 600;
  color: #16323a;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
.src {
  margin-top: 4px;
}
.ahead {
  display: flex;
  align-items: center;
  gap: 16px;
  margin-bottom: 12px;
}
.ahead h2 {
  margin: 0;
}
.ahead .sub {
  flex: 1;
}
.err {
  margin-top: 4px;
  font-size: 12px;
  color: #a3332d;
}
.dm {
  display: inline-block;
  margin: 0 8px 4px 0;
  font-size: 12.5px;
}
</style>
