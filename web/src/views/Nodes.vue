<script setup lang="ts">
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'
import DiffView from '../components/DiffView.vue'

const session = useSession()
const groups = ref<any[]>([])
const nodes = ref<any[]>([])
const gid = ref<number>(0)
const group = computed(() => groups.value.find((g) => g.id === gid.value))

async function load() {
  groups.value = await api.get('/node-groups')
  if (!gid.value && groups.value.length) gid.value = groups.value[0].id
  nodes.value = (await api.get('/nodes', { group_id: gid.value })).filter((n: any) => n.status !== 'revoked')
}
let t: number | undefined
onMounted(() => {
  load()
  t = window.setInterval(load, 5000)
})
onUnmounted(() => clearInterval(t))

// 添加节点
const enroll = ref<any>(null)
const enrolling = ref(false)
async function addNode() {
  enrolling.value = true
  try {
    enroll.value = await api.post(`/node-groups/${gid.value}/enroll`)
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    enrolling.value = false
  }
}
function copy(text: string) {
  navigator.clipboard?.writeText(text).then(() => message.success('已复制'))
}

// 节点组设置
const editing = ref(false)
const gform = reactive({ description: '', allowed_cidrs: '', real_ip_from: '' })
function openGroup() {
  Object.assign(gform, { description: group.value.description, allowed_cidrs: group.value.allowed_cidrs, real_ip_from: group.value.real_ip_from })
  editing.value = true
}
async function saveGroup() {
  try {
    await api.put(`/node-groups/${gid.value}`, gform)
    message.success('已保存，发布后生效')
    editing.value = false
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function restore(n: any) {
  try {
    const d = await api.post(`/nodes/${n.id}/restore`)
    message.success(`正在把 ${n.name} 恢复为 ${d.revision_id}，可在“发布”页查看进度`, 5)
    load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

// 漂移详情与采纳（规格书 12.7 路径 B）
const drift = ref<any>(null)
const driftNode = ref<any>(null)
const adoptRes = ref<any>(null)
async function showDrift(n: any) {
  driftNode.value = n
  adoptRes.value = null
  try {
    drift.value = await api.get(`/nodes/${n.id}/drift`)
  } catch (e) {
    drift.value = { files: [], error: errMsg(e) }
  }
}
async function adopt() {
  try {
    adoptRes.value = await api.post(`/nodes/${driftNode.value.id}/drift/adopt`, {})
    message.success(`已提交 ${adoptRes.value.snippets.length} 个片段，等待另一人审批后随下一次发布生效`, 5)
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function revoke(n: any) {
  await api.post(`/nodes/${n.id}/revoke`)
  message.success(`已吊销 ${n.name}，其证书立即失效。如需重新接入：点“添加节点”，在该机器上执行新生成的命令`, 6)
  load()
}

const columns = [
  { title: '节点', key: 'name' },
  { title: '状态', key: 'status', width: 110 },
  { title: '当前版本', key: 'rev', width: 130 },
  { title: '数据面', key: 'syapp' },
  { title: '负载', key: 'load', width: 210 },
  { title: '最后心跳', key: 'seen', width: 120 },
  { title: '', key: 'ops', width: 80 },
]
function ago(t?: string) {
  if (!t) return '—'
  const s = dayjs().diff(dayjs(t), 'second')
  return s < 60 ? `${s} 秒前` : s < 3600 ? `${Math.floor(s / 60)} 分钟前` : dayjs(t).format('MM-DD HH:mm')
}
function mb(b: number) {
  return (b / 1024 / 1024 / 1024).toFixed(1) + ' GB'
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>节点</h1>
        <p>节点上的 syapp-agent 只向控制台发起连接，节点不需要开放任何管理端口。</p>
      </div>
      <a-space>
        <a-button v-if="session.can('operator')" @click="openGroup">节点组设置</a-button>
        <a-button v-if="session.can('operator')" type="primary" :loading="enrolling" :disabled="!session.writable" @click="addNode">添加节点</a-button>
      </a-space>
    </div>

    <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" style="margin-bottom: 16px" @change="load" />

    <div class="panel">
      <div class="gsum" v-if="group">
        <span>节点组 <b>{{ group.name }}</b></span>
        <span>组版本 <b class="num">{{ group.current_revision_id || '未发布' }}</b></span>
        <span>允许回源网段 <b class="code">{{ group.allowed_cidrs || '未设置' }}</b></span>
      </div>
      <a-table :columns="columns" :data-source="nodes" row-key="id" :pagination="false">
        <template #emptyText>
          <a-empty description="这个节点组还没有节点。点击“添加节点”生成一次性注册命令。" />
        </template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">
            <div class="nm">{{ record.name }}</div>
            <div class="sub code">{{ record.remote_addr }}</div>
          </template>
          <template v-else-if="column.key === 'status'">
            <a-badge :status="record.status === 'online' ? 'success' : 'default'" :text="record.status === 'online' ? '在线' : '离线'" />
            <div v-if="record.drifted" class="warn">
              配置被手动改动
              <a v-if="session.can('operator')" class="restore" @click="showDrift(record)">查看差异</a>
              <a-popconfirm v-if="session.can('releaser')" :title="`把 ${record.name} 恢复为控制台下发的 ${record.applied_revision}？节点上的改动会备份在版本目录旁，不会丢失。`" ok-text="恢复" @confirm="restore(record)">
                <a class="restore">恢复</a>
              </a-popconfirm>
            </div>
          </template>
          <template v-else-if="column.key === 'rev'">
            <span class="num">{{ record.applied_revision || '—' }}</span>
            <a-tag v-if="group?.current_revision_id && record.applied_revision !== group.current_revision_id && record.status === 'online'" color="gold" style="margin-left: 6px">落后</a-tag>
          </template>
          <template v-else-if="column.key === 'syapp'">
            <div>{{ (record.syapp_version || '').replace('syapp version: ', '') || '—' }}<a-tag v-if="record.tls_lib === 'tongsuo'" color="red" class="gm">国密</a-tag><a-tooltip v-if="record.lua_addon" :title="`syapp-lua ${record.lua_addon}`"><a-tag color="purple" class="gm">Lua</a-tag></a-tooltip></div>
            <div class="sub">agent {{ record.agent_version }}</div>
          </template>
          <template v-else-if="column.key === 'load'">
            <template v-if="record.status === 'online'">
              <div class="num">CPU {{ Math.round(record.cpu_percent) }}%，内存 {{ mb(record.mem_used_bytes) }}</div>
              <div class="sub num">{{ record.active_connections }} 个连接</div>
            </template>
            <span v-else>—</span>
          </template>
          <template v-else-if="column.key === 'seen'">{{ ago(record.last_seen_at) }}</template>
          <template v-else-if="column.key === 'ops'">
            <a-popconfirm v-if="session.can('admin')" title="吊销后该节点证书立即失效，需重新注册。确定吊销？" ok-text="吊销" @confirm="revoke(record)">
              <a-button type="link" danger size="small">吊销</a-button>
            </a-popconfirm>
          </template>
        </template>
      </a-table>
    </div>

    <a-modal :open="!!enroll" title="添加节点" :footer="null" width="720px" @cancel="enroll = null">
      <template v-if="enroll">
        <p>在边缘服务器上（已安装 syapp 与 syapp-agent 软件包）以 root 执行下面的命令。令牌只能使用一次，<b>{{ dayjs(enroll.expires_at).format('HH:mm') }}</b> 前有效。</p>
        <pre class="cmd code">{{ enroll.command }}</pre>
        <a-button type="primary" @click="copy(enroll.command)">复制命令</a-button>
        <a-descriptions :column="1" size="small" style="margin-top: 20px">
          <a-descriptions-item label="控制台地址"><span class="code">{{ enroll.server }}</span></a-descriptions-item>
          <a-descriptions-item label="控制台 CA 指纹"><span class="code">{{ enroll.ca_fingerprint }}</span></a-descriptions-item>
        </a-descriptions>
        <p class="sub">节点首次连接时用 CA 指纹确认控制台身份，之后全程使用双向证书认证。节点上线后会自动同步到节点组的当前版本。</p>
      </template>
    </a-modal>

    <a-modal v-model:open="editing" title="节点组设置" ok-text="保存" @ok="saveGroup">
      <a-form layout="vertical">
        <a-form-item label="说明"><a-input v-model:value="gform.description" /></a-form-item>
        <a-form-item label="允许回源网段" extra="上游服务器地址必须落在这些网段内（逗号分隔 CIDR）。回环与云元数据地址始终禁止。">
          <a-input v-model:value="gform.allowed_cidrs" class="code" />
        </a-form-item>
        <a-form-item label="CDN / 负载均衡回源网段" extra="填写后，syapp 从 X-Forwarded-For 中取客户端真实 IP。">
          <a-input v-model:value="gform.real_ip_from" class="code" placeholder="例如 10.0.0.0/8" />
        </a-form-item>
      </a-form>
    </a-modal>
    <a-drawer :open="!!drift" :width="760" :title="`${driftNode?.name} 的手工改动`" @close="drift = null">
      <template v-if="drift">
        <a-alert v-if="drift.error" type="info" show-icon :message="drift.error" />
        <template v-else>
          <p class="sub">基于版本 {{ drift.revision_id }}，上报于 {{ drift.reported_at }}。节点只上报受管版本目录内的文件；私钥与超大文件只比较哈希。</p>
          <div v-for="f in drift.files" :key="f.path" class="dfile">
            <div><a-tag>{{ { modified: '修改', added: '新增', removed: '删除' }[f.change as string] }}</a-tag><span class="code">{{ f.path }}</span></div>
            <DiffView v-if="f.diff && f.diff.startsWith('---')" :diff="f.diff" />
            <div v-else class="sub">{{ f.diff }}</div>
          </div>
          <a-space style="margin-top: 12px">
            <a-button v-if="session.can('operator')" type="primary" :disabled="!session.writable" @click="adopt">采纳为片段</a-button>
            <a-popconfirm v-if="session.can('releaser')" :title="`把 ${driftNode?.name} 恢复为 ${driftNode?.applied_revision}？`" ok-text="恢复" @confirm="restore(driftNode); drift = null">
              <a-button>恢复为控制台版本</a-button>
            </a-popconfirm>
          </a-space>
          <p class="sub" style="margin-top: 8px">采纳：站点配置中新增的白名单内指令转为自定义片段（作者记为“节点手工修改 + 采纳人”），需另一人批准并发布；其余改动只能恢复。</p>
          <template v-if="adoptRes">
            <a-alert type="success" show-icon :message="`已提交 ${adoptRes.snippets.length} 个片段到“自定义片段”等待审批`" style="margin-top: 8px" />
            <a-alert v-for="(r, i) in adoptRes.rejected" :key="i" type="warning" :message="r" style="margin-top: 6px" />
          </template>
        </template>
      </template>
    </a-drawer>
  </div>
</template>

<style scoped>
.gm {
  margin-left: 6px;
  font-size: 11px;
  line-height: 16px;
}
.gsum {
  display: flex;
  flex-wrap: wrap;
  gap: 28px;
  margin-bottom: 14px;
  font-size: 13px;
  color: #6b7c80;
}
.gsum b {
  color: #16323a;
  font-weight: 600;
  margin-left: 6px;
}
.nm {
  font-weight: 600;
  color: #16323a;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
.warn {
  font-size: 12px;
  color: #a3332d;
}
.dfile {
  margin-bottom: 12px;
}
.restore {
  margin-left: 6px;
}
.cmd {
  white-space: pre-wrap;
  word-break: break-all;
  background: #16323a;
  color: #d9ecee;
  padding: 14px 16px;
  border-radius: 6px;
  margin: 12px 0;
}
</style>
