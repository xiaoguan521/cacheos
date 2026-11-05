<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'
import SiteEditor from '../components/SiteEditor.vue'

const session = useSession()
const tab = ref('sites')
const groups = ref<any[]>([])
const gid = ref(0)
const sites = ref<any[]>([])
const ups = ref<any[]>([])

async function load() {
  groups.value = await api.get('/node-groups')
  if (!gid.value && groups.value.length) gid.value = groups.value[0].id
  ;[sites.value, ups.value] = await Promise.all([api.get('/sites', { group_id: gid.value }), api.get('/upstreams', { group_id: gid.value })])
}
onMounted(load)

const editorOpen = ref(false)
const editing = ref<any>(null)
function edit(s: any | null) {
  editing.value = s
  editorOpen.value = true
}
async function removeSite(s: any) {
  try {
    await api.del(`/sites/${s.id}`)
    message.success(`已删除 ${s.name}，发布后从节点移除`)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

const modeText: Record<string, string> = { static: '静态', proxy: '代理', redirect: '跳转', deny: '拒绝' }
const siteCols = [
  { title: '站点', key: 'name' },
  { title: '路由', key: 'routes' },
  { title: 'HTTPS', key: 'https', width: 110 },
  { title: '状态', key: 'enabled', width: 80 },
  { title: '最后修改', key: 'updated', width: 150 },
  { title: '', key: 'ops', width: 120 },
]

// 上游
const upOpen = ref(false)
const upForm = reactive({ id: 0, name: '', algorithm: 'least_conn', servers: [{ addr: '', weight: 0 }] as { addr: string; weight: number }[] })
const upErr = ref('')
function editUp(u: any | null) {
  upErr.value = ''
  if (u) Object.assign(upForm, { id: u.id, name: u.name, algorithm: u.spec.algorithm || 'round_robin', servers: JSON.parse(JSON.stringify(u.spec.servers)) })
  else Object.assign(upForm, { id: 0, name: '', algorithm: 'least_conn', servers: [{ addr: '', weight: 0 }] })
  upOpen.value = true
}
async function saveUp() {
  upErr.value = ''
  const spec = { name: upForm.name, algorithm: upForm.algorithm, servers: upForm.servers.filter((s) => s.addr).map((s) => ({ addr: s.addr.trim(), weight: Number(s.weight) || 0 })) }
  try {
    if (upForm.id) await api.put(`/upstreams/${upForm.id}`, { group_id: gid.value, spec })
    else await api.post('/upstreams', { group_id: gid.value, spec })
    upOpen.value = false
    message.success('已保存')
    load()
  } catch (e) {
    upErr.value = errMsg(e)
  }
}
async function removeUp(u: any) {
  try {
    await api.del(`/upstreams/${u.id}`)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
const algoText: Record<string, string> = { round_robin: '轮询', least_conn: '最少连接', ip_hash: '源 IP 哈希' }
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>站点与上游</h1>
        <p>这里的修改先保存为草稿，到“发布”页确认差异后再下发到节点。</p>
      </div>
      <a-space v-if="session.can('operator')">
        <a-button v-if="tab === 'ups'" type="primary" :disabled="!session.writable" @click="editUp(null)">新建上游</a-button>
        <a-button v-else type="primary" :disabled="!session.writable" @click="edit(null)">新建站点</a-button>
      </a-space>
    </div>

    <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" style="margin-bottom: 12px" @change="load" />
    <a-tabs v-model:active-key="tab" :items="[{ key: 'sites', label: `站点（${sites.length}）` }, { key: 'ups', label: `上游服务（${ups.length}）` }]" />

    <div v-show="tab === 'sites'" class="panel">
      <a-table :columns="siteCols" :data-source="sites" row-key="id" :pagination="false">
        <template #emptyText><a-empty description="还没有站点。新建一个站点，填写域名和路由即可。" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="nm">{{ record.name }}</div>
            <div class="sub">{{ record.spec.domains.join('，') }}</div>
          </template>
          <template v-else-if="column.key === 'routes'">
            <div v-for="r in record.spec.routes" :key="r.match + r.path" class="rt">
              <span class="code">{{ r.match === 'exact' ? '= ' : '' }}{{ r.path }}</span>
              <span class="mode" :class="r.mode">{{ modeText[r.mode] }}</span>
              <span v-if="r.mode === 'proxy'" class="sub">{{ r.proxy.upstream }}</span>
              <span v-if="r.mode === 'redirect'" class="sub">{{ r.redirect.code }} {{ r.redirect.to }}</span>
            </div>
          </template>
          <template v-else-if="column.key === 'https'">
            <span v-if="record.spec.https">{{ record.spec.force_https ? '强制' : '启用' }}{{ record.spec.http2 ? '，HTTP/2' : '' }}</span>
            <span v-else class="sub">未启用</span>
          </template>
          <template v-else-if="column.key === 'enabled'">
            <a-badge :status="record.enabled ? 'success' : 'default'" :text="record.enabled ? '启用' : '停用'" />
          </template>
          <template v-else-if="column.key === 'updated'">
            <div>{{ dayjs(record.updated_at).format('MM-DD HH:mm') }}</div>
            <div class="sub">{{ record.updated_by }}，第 {{ record.version }} 版</div>
          </template>
          <template v-else-if="column.key === 'ops'">
            <a-space v-if="session.can('operator')">
              <a-button type="link" size="small" @click="edit(record)">编辑</a-button>
              <a-popconfirm title="删除站点？发布后从所有节点移除。" ok-text="删除" @confirm="removeSite(record)">
                <a-button type="link" size="small" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </div>

    <div v-show="tab === 'ups'" class="panel">
      <a-table :data-source="ups" row-key="id" :pagination="false" :columns="[{ title: '名称', key: 'name' }, { title: '负载均衡', key: 'algo', width: 140 }, { title: '服务器', key: 'servers' }, { title: '', key: 'ops', width: 120 }]">
        <template #emptyText><a-empty description="还没有上游服务。站点的“反向代理”路由需要先定义上游。" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'"><span class="nm">{{ record.name }}</span></template>
          <template v-else-if="column.key === 'algo'">{{ algoText[record.spec.algorithm] || '轮询' }}</template>
          <template v-else-if="column.key === 'servers'">
            <span v-for="s in record.spec.servers" :key="s.addr" class="srv code">{{ s.addr }}<template v-if="s.weight"> ×{{ s.weight }}</template></span>
          </template>
          <template v-else-if="column.key === 'ops'">
            <a-space v-if="session.can('operator')">
              <a-button type="link" size="small" @click="editUp(record)">编辑</a-button>
              <a-popconfirm title="删除上游？被站点引用时无法删除。" ok-text="删除" @confirm="removeUp(record)">
                <a-button type="link" size="small" danger>删除</a-button>
              </a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </div>

    <SiteEditor :open="editorOpen" :site="editing" :group-id="gid" :upstreams="ups" @close="editorOpen = false" @saved="(editorOpen = false), load()" />

    <a-modal v-model:open="upOpen" :title="upForm.id ? `编辑上游 ${upForm.name}` : '新建上游'" ok-text="保存" @ok="saveUp">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="upForm.name" :disabled="!!upForm.id" placeholder="order-api" /></a-form-item>
        <a-form-item label="负载均衡">
          <a-radio-group v-model:value="upForm.algorithm" :options="[{ label: '最少连接', value: 'least_conn' }, { label: '轮询', value: 'round_robin' }, { label: '源 IP 哈希', value: 'ip_hash' }]" />
        </a-form-item>
        <a-form-item label="服务器" extra="IP:端口，必须在节点组的允许回源网段内">
          <div v-for="(s, i) in upForm.servers" :key="i" class="srvrow">
            <a-input v-model:value="s.addr" class="code" placeholder="10.0.1.11:8080" />
            <a-input-number v-model:value="s.weight" :min="0" :max="100" placeholder="权重" style="width: 96px" />
            <a-button type="text" danger :disabled="upForm.servers.length === 1" @click="upForm.servers.splice(i, 1)">删除</a-button>
          </div>
          <a-button size="small" @click="upForm.servers.push({ addr: '', weight: 0 })">添加服务器</a-button>
        </a-form-item>
        <a-alert v-if="upErr" type="error" :message="upErr" show-icon />
      </a-form>
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
.rt {
  display: flex;
  gap: 8px;
  align-items: center;
  line-height: 1.9;
}
.mode {
  font-size: 12px;
  padding: 0 6px;
  border-radius: 4px;
  background: #eef2f1;
  color: #3b4a4e;
}
.mode.proxy {
  background: #e3f1f2;
  color: #0b5f66;
}
.mode.deny {
  background: #f8e3e1;
  color: #a3332d;
}
.srv {
  display: inline-block;
  margin-right: 10px;
}
.srvrow {
  display: flex;
  gap: 8px;
  margin-bottom: 8px;
}
</style>
