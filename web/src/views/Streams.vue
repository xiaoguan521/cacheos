<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const groups = ref<any[]>([])
const gid = ref(0)
const rows = ref<any[]>([])
const licensed = computed(() => !!session.license?.features?.includes('stream'))

async function load() {
  if (!groups.value.length) {
    groups.value = await api.get('/node-groups')
    if (!gid.value && groups.value.length) gid.value = groups.value[0].id
  }
  rows.value = await api.get('/streams', { group_id: gid.value })
}
watch(gid, load)
onMounted(load)

const open = ref(false)
const editId = ref(0)
const form = reactive<any>({ name: '', listen: 3306, protocol: 'tcp', algorithm: '', servers: '', proxy_protocol: false, connect_timeout: 5, idle_timeout: 600, sni: '' })
const toList = (s: string) =>
  s.split(/[\s,]+/).filter(Boolean).map((a) => {
    const [addr, w] = a.split('*')
    return { addr, weight: Number(w || 0) }
  })
const fromList = (l: any[]) => (l || []).map((x) => (x.weight ? `${x.addr}*${x.weight}` : x.addr)).join('\n')
function edit(r?: any) {
  editId.value = r?.id || 0
  const sp = r?.spec || {}
  Object.assign(form, {
    name: sp.name || '', listen: sp.listen || 3306, protocol: sp.protocol || 'tcp', algorithm: sp.algorithm || '', servers: fromList(sp.servers),
    proxy_protocol: !!sp.proxy_protocol, connect_timeout: sp.connect_timeout || 5, idle_timeout: sp.idle_timeout || 600,
    sni: (sp.sni_routes || []).map((x: any) => `${x.domain} = ${fromList(x.servers).replace(/\n/g, ',')}`).join('\n'),
  })
  open.value = true
}
async function save() {
  const spec = {
    name: form.name, listen: form.listen, protocol: form.protocol, algorithm: form.algorithm, servers: toList(form.servers), proxy_protocol: form.proxy_protocol,
    connect_timeout: form.connect_timeout, idle_timeout: form.idle_timeout,
    sni_routes: form.sni.split('\n').filter((l: string) => l.includes('=')).map((l: string) => {
      const [d, s] = l.split('=')
      return { domain: d.trim(), servers: toList(s) }
    }),
  }
  try {
    if (editId.value) await api.put(`/streams/${editId.value}`, { spec })
    else await api.post('/streams', { group_id: gid.value, spec })
    open.value = false
    message.success('已保存，发布后生效')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function toggle(r: any) {
  try {
    await api.put(`/streams/${r.id}`, { spec: r.spec, enabled: !r.enabled })
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function del(r: any) {
  await api.del(`/streams/${r.id}`)
  await load()
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>四层代理</h1>
        <p>TCP / UDP 端口转发，可按 TLS SNI 分流（不解密）。保存后进入待发布变更，发布后生效；节点启用了防火墙时需放行对应端口。</p>
      </div>
      <a-space>
        <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" />
        <a-button v-if="session.can('operator')" type="primary" :disabled="!licensed || !session.writable" @click="edit()">新建</a-button>
      </a-space>
    </div>
    <a-alert v-if="!licensed" type="info" show-icon style="margin-bottom: 16px" message="当前授权不包含四层代理（专业版及以上）。" />
    <section class="panel">
      <a-table :data-source="rows" row-key="id" :pagination="false" size="middle"
        :columns="[{ title: '名称', key: 'name' }, { title: '监听', key: 'listen' }, { title: '后端', key: 'servers' }, { title: '状态', key: 'st', width: 90 }, { title: '', key: 'op', width: 200 }]">
        <template #emptyText><a-empty description="还没有四层代理服务" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">{{ record.name }}</template>
          <template v-else-if="column.key === 'listen'"><span class="code">{{ record.spec.protocol }}/{{ record.spec.listen }}</span><a-tag v-if="record.spec.sni_routes?.length" style="margin-left: 6px">SNI 分流 {{ record.spec.sni_routes.length }}</a-tag></template>
          <template v-else-if="column.key === 'servers'"><span class="code">{{ record.spec.servers.map((s: any) => s.addr).join('、') }}</span></template>
          <template v-else-if="column.key === 'st'"><a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag></template>
          <template v-else-if="column.key === 'op' && session.can('operator')">
            <a-space :size="4">
              <a-button size="small" :disabled="!licensed" @click="edit(record)">编辑</a-button>
              <a-button size="small" :disabled="!licensed && !record.enabled" @click="toggle(record)">{{ record.enabled ? '停用' : '启用' }}</a-button>
              <a-popconfirm title="删除该服务？发布后生效" @confirm="del(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </section>
    <a-modal v-model:open="open" :title="editId ? '编辑四层代理' : '新建四层代理'" :width="600" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="form.name" :disabled="!!editId" placeholder="mysql" /></a-form-item>
        <a-space>
          <a-form-item label="协议"><a-select v-model:value="form.protocol" style="width: 100px" :options="[{ value: 'tcp', label: 'TCP' }, { value: 'udp', label: 'UDP' }]" /></a-form-item>
          <a-form-item label="监听端口"><a-input-number v-model:value="form.listen" :min="1" :max="65535" /></a-form-item>
          <a-form-item label="负载算法"><a-select v-model:value="form.algorithm" style="width: 160px" :options="[{ value: '', label: '轮询' }, { value: 'least_conn', label: '最少连接' }, { value: 'hash', label: '按来源 IP 一致性哈希' }]" /></a-form-item>
        </a-space>
        <a-form-item label="后端（IP:端口，每行一个，*数字 为权重）"><a-textarea v-model:value="form.servers" :rows="3" class="code" placeholder="10.0.1.21:3306&#10;10.0.1.22:3306*2" /></a-form-item>
        <a-form-item v-if="form.protocol === 'tcp'" label="按 SNI 分流（可选，每行：域名 = 后端,后端）"><a-textarea v-model:value="form.sni" :rows="2" class="code" placeholder="a.example.com = 10.0.1.23:443" /></a-form-item>
        <a-space>
          <a-form-item label="连接超时（秒）"><a-input-number v-model:value="form.connect_timeout" :min="1" :max="60" /></a-form-item>
          <a-form-item label="空闲超时（秒）"><a-input-number v-model:value="form.idle_timeout" :min="1" :max="86400" /></a-form-item>
        </a-space>
        <a-checkbox v-model:checked="form.proxy_protocol">向后端发送 PROXY protocol（后端需支持）</a-checkbox>
      </a-form>
    </a-modal>
  </div>
</template>
