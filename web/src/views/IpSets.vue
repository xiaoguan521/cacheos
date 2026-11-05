<script setup lang="ts">
// IP 集与地域库（P10）：节点组的命名网段集合，被站点访问规则的“IP 集 / 地域”条件引用。
import { onMounted, reactive, ref, watch } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg, http } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const groups = ref<any[]>([])
const gid = ref(0)
const rows = ref<any[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    if (!groups.value.length) {
      groups.value = await api.get('/node-groups')
      if (!gid.value && groups.value.length) gid.value = groups.value[0].id
    }
    rows.value = await api.get('/ip-sets', { group_id: gid.value })
  } finally {
    loading.value = false
  }
}
watch(gid, load)
onMounted(load)

// 手工维护
const open = ref(false)
const editing = ref<any>(null)
const form = reactive({ name: '', label: '', text: '' })
const saving = ref(false)
const error = ref('')
async function openEdit(r?: any) {
  error.value = ''
  editing.value = r || null
  Object.assign(form, { name: r?.name || '', label: r?.label || '', text: '' })
  if (r) {
    const full = await api.get(`/ip-sets/${r.id}`)
    form.text = (full.cidrs || []).join('\n')
  }
  open.value = true
}
async function save() {
  error.value = ''
  saving.value = true
  const cidrs = form.text.split(/[\s,，]+/).map((s) => s.trim()).filter(Boolean)
  try {
    if (editing.value) await api.put(`/ip-sets/${editing.value.id}`, { label: form.label, cidrs })
    else await api.post('/ip-sets', { group_id: gid.value, name: form.name, label: form.label, cidrs })
    message.success('已保存。被规则引用时，下次发布生效。')
    open.value = false
    load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    saving.value = false
  }
}

// 从 RIR 分配统计文件导入地域库
const impOpen = ref(false)
const imp = reactive({ name: 'cn', label: '', country: 'CN', file: null as File | null })
const importing = ref(false)
function pick() {
  const el = document.createElement('input')
  el.type = 'file'
  el.onchange = () => (imp.file = el.files?.[0] || null)
  el.click()
}
async function doImport() {
  if (!imp.file) return
  error.value = ''
  importing.value = true
  const fd = new FormData()
  fd.append('group_id', String(gid.value))
  fd.append('name', imp.name)
  fd.append('label', imp.label)
  fd.append('country', imp.country)
  fd.append('file', imp.file)
  try {
    const r = (await http.post('/ip-sets/import', fd)).data
    message.success(`已导入 ${r.count} 条网段`)
    impOpen.value = false
    load()
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    importing.value = false
  }
}

async function remove(r: any) {
  try {
    await api.del(`/ip-sets/${r.id}`)
    message.success(`已删除 ${r.name}`)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

const columns = [
  { title: 'IP 集', key: 'name' },
  { title: '网段', key: 'count', width: 110 },
  { title: '来源', key: 'source', width: 150 },
  { title: '被规则引用', key: 'users' },
  { title: '', key: 'ops', width: 150 },
]
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>IP 集与地域库</h1>
        <p>在站点的访问规则中按“IP 集 / 地域”放行或拒绝。地域库从 RIR 公开的分配统计文件（delegated-*-extended-latest）按国家 / 地区代码导入，定期重新导入即可更新。</p>
      </div>
      <a-space>
        <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" />
        <template v-if="session.can('operator')">
          <a-button :disabled="!session.writable" @click="openEdit()">新建 IP 集</a-button>
          <a-button type="primary" :disabled="!session.writable" @click="(error = ''), (impOpen = true)">导入地域库</a-button>
        </template>
      </a-space>
    </div>

    <div class="panel">
      <a-table :columns="columns" :data-source="rows" row-key="id" :pagination="false" :loading="loading">
        <template #emptyText><a-empty description="还没有 IP 集" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="nm code">{{ record.name }}</div>
            <div class="sub">{{ record.label }} · {{ record.updated_by }} {{ dayjs(record.updated_at).format('MM-DD HH:mm') }}</div>
          </template>
          <template v-else-if="column.key === 'count'"><span class="num">{{ record.count.toLocaleString() }}</span></template>
          <template v-else-if="column.key === 'source'">
            <a-tag v-if="record.source.startsWith('rir:')" color="blue">地域 {{ record.source.slice(4) }}</a-tag>
            <span v-else class="sub">手工维护</span>
          </template>
          <template v-else-if="column.key === 'users'">
            <span v-if="!record.users.length" class="sub">未使用</span>
            <span v-else class="code users">{{ record.users.join('、') }}</span>
          </template>
          <template v-else-if="column.key === 'ops'">
            <a-space v-if="session.can('operator')" :size="0">
              <a-button type="link" size="small" :disabled="!session.writable" @click="openEdit(record)">编辑</a-button>
              <a-popconfirm :title="`删除 ${record.name}？`" ok-text="删除" @confirm="remove(record)">
                <a-button type="link" danger size="small" :disabled="!session.writable || record.users.length > 0">删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </div>

    <a-modal v-model:open="open" :title="editing ? `编辑 ${editing.name}` : '新建 IP 集'" ok-text="保存" :confirm-loading="saving" width="600px" @ok="save">
      <a-form layout="vertical">
        <a-form-item v-if="!editing" label="名称" extra="小写字母、数字、连字符；规则中按名称引用"><a-input v-model:value="form.name" class="code" placeholder="office" /></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.label" placeholder="办公网出口" /></a-form-item>
        <a-form-item label="网段" extra="每行一个，支持 IPv4 / IPv6 地址或 CIDR"><a-textarea v-model:value="form.text" :rows="8" class="code" placeholder="10.20.0.0/16&#10;203.0.113.7" /></a-form-item>
      </a-form>
      <a-alert v-if="error" type="error" :message="error" show-icon />
    </a-modal>

    <a-modal v-model:open="impOpen" title="导入地域库" ok-text="导入" :confirm-loading="importing" :ok-button-props="{ disabled: !imp.file }" width="560px" @ok="doImport">
      <a-alert type="info" show-icon style="margin-bottom: 16px"
        message="从 APNIC / ARIN / RIPE NCC / LACNIC / AFRINIC 下载 delegated-*-extended-latest 文件（中国大陆、港澳台在 APNIC），按国家 / 地区代码挑出已分配的 IPv4 / IPv6 网段。同名 IP 集会被整体替换。" />
      <a-form layout="vertical">
        <a-form-item label="国家 / 地区代码" extra="ISO 3166 两位代码，例如 CN、HK、SG"><a-input v-model:value="imp.country" class="code" style="width: 120px" /></a-form-item>
        <a-form-item label="保存为 IP 集"><a-input v-model:value="imp.name" class="code" placeholder="cn" /></a-form-item>
        <a-form-item label="说明（可选）"><a-input v-model:value="imp.label" placeholder="中国大陆" /></a-form-item>
        <a-form-item label="分配统计文件"><a-button @click="pick">{{ imp.file ? imp.file.name : '选择文件' }}</a-button></a-form-item>
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
.users {
  font-size: 12.5px;
}
</style>
