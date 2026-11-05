<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg, http } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const groups = ref<any[]>([])
const gid = ref(0)
const history = ref<any[]>([])
const current = ref<any>(null) // { import, report, result, certs_pending }
const uploading = ref(false)
const applying = ref(false)

async function load() {
  if (!groups.value.length) {
    groups.value = await api.get('/node-groups')
    if (!gid.value && groups.value.length) gid.value = groups.value[0].id
  }
  history.value = await api.get('/imports', { group_id: gid.value })
}
watch(gid, () => {
  current.value = null
  load()
})
onMounted(load)

function pick() {
  const el = document.createElement('input')
  el.type = 'file'
  el.accept = '.txt,.conf,.gz,.tgz'
  el.onchange = async () => {
    const f = el.files?.[0]
    if (!f) return
    const fd = new FormData()
    fd.append('group_id', String(gid.value))
    fd.append('file', f)
    uploading.value = true
    try {
      const r = (await http.post('/imports', fd)).data
      current.value = { ...r, certs_pending: Object.values(r.report.sites || {}).some((s: any) => s.cert_found) }
      await load()
    } catch (e) {
      message.error(errMsg(e))
    } finally {
      uploading.value = false
    }
  }
  el.click()
}
async function open(id: number) {
  current.value = await api.get(`/imports/${id}`)
}
async function apply() {
  applying.value = true
  try {
    const r = await api.post(`/imports/${current.value.import.id}/apply`, {})
    message.success(`已导入 ${r.sites.length} 个站点（草稿），${r.snippets.length} 个片段待审批`)
    await open(current.value.import.id)
    await load()
  } catch (e) {
    message.error(errMsg(e))
  } finally {
    applying.value = false
  }
}
async function download() {
  const r = await http.get(`/imports/${current.value.import.id}/report`, { responseType: 'blob' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(r.data)
  a.download = `import-report-${current.value.import.id}.json`
  a.click()
  URL.revokeObjectURL(a.href)
}
const rep = computed(() => current.value?.report)
const statusTag: Record<string, { t: string; c: string }> = {
  full: { t: '完全转换', c: 'green' },
  partial: { t: '部分转换', c: 'gold' },
  failed: { t: '无法转换', c: 'red' },
}
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>配置导入</h1>
        <p>上传现有配置（<span class="code">syapp -T</span> 的输出，或配置目录打包的 tar.gz，可含证书）。自动转成站点、上游与证书；转不了的部分逐行列入报告。导入结果只进草稿，之后照常预览、审批、发布。</p>
      </div>
      <a-space>
        <a-segmented v-if="groups.length > 1" v-model:value="gid" :options="groups.map((g) => ({ label: g.name, value: g.id }))" />
        <a-button v-if="session.can('operator')" type="primary" :loading="uploading" :disabled="!session.writable" @click="pick">上传配置</a-button>
      </a-space>
    </div>

    <template v-if="rep">
      <section class="panel">
        <div class="phead">
          <h2>导入报告 #{{ current.import.id }} <span class="sub">{{ current.import.file_name }}</span></h2>
          <a-space>
            <a-button @click="download">下载报告</a-button>
            <a-button v-if="!current.import.applied_at && session.can('operator')" type="primary" :loading="applying" :disabled="!session.writable" @click="apply">应用为草稿</a-button>
            <a-tag v-else-if="current.import.applied_at" color="green">{{ current.import.applied_by }} 已于 {{ dayjs(current.import.applied_at).format('MM-DD HH:mm') }} 应用</a-tag>
          </a-space>
        </div>
        <div class="stats">
          <div><b class="num">{{ rep.summary.servers }}</b><span>server 块</span></div>
          <div><b class="num">{{ rep.summary.full }}</b><span>完全转换</span></div>
          <div><b class="num">{{ rep.summary.partial }}</b><span>部分转换</span></div>
          <div><b class="num">{{ rep.summary.failed }}</b><span>无法转换</span></div>
          <div><b class="num">{{ rep.upstreams.length }}</b><span>上游组</span></div>
          <div><b class="num">{{ rep.summary.issues }}</b><span>未转换条目</span></div>
        </div>
        <a-alert v-if="!current.import.applied_at && rep.sites.some((s: any) => s.cert_found)" type="info" show-icon style="margin-top: 12px"
          message="上传内容中的证书与私钥只在内存中保留 30 分钟等待确认；应用时加密存入证书库，不会出现在报告里。" />
      </section>

      <section class="panel" v-for="s in rep.sites" :key="s.name">
        <div class="phead">
          <h2>{{ s.name }} <a-tag :color="statusTag[s.status].c">{{ statusTag[s.status].t }}</a-tag></h2>
          <span class="sub code">{{ s.file }}:{{ s.line }}</span>
        </div>
        <div class="sub">{{ (s.spec.domains || []).join('、') }}<template v-if="s.spec.https"> · HTTPS{{ s.spec.force_https ? '（强制跳转）' : '' }}{{ s.cert_found ? ' · 证书已找到' : '' }}</template></div>
        <ul class="routes">
          <li v-for="r in s.spec.routes" :key="r.path"><span class="code">{{ r.match === 'exact' ? '= ' : '' }}{{ r.path }}</span> → {{ r.mode }}<template v-if="r.proxy">（{{ r.proxy.upstream }}）</template><template v-if="r.redirect">（{{ r.redirect.code }} {{ r.redirect.to }}）</template><a-tag v-if="s.location_snippets[r.path]" style="margin-left: 6px">片段</a-tag></li>
        </ul>
        <template v-if="s.server_snippet">
          <div class="sub">转为 server 片段（应用后需另一人审批）：</div>
          <pre class="snip">{{ s.server_snippet }}</pre>
        </template>
        <a-table v-if="s.issues.length" :data-source="s.issues" :pagination="false" size="small" :row-key="(r: any) => r.file + r.line + r.directive"
          :columns="[{ title: '位置', key: 'loc', width: 260 }, { title: '指令', dataIndex: 'directive', width: 160 }, { title: '原因', key: 'reason' }]">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'loc'"><span class="code">{{ record.file }}:{{ record.line }}</span></template>
            <template v-else-if="column.key === 'reason'"><a-tag v-if="record.blocking" color="red">需人工处理</a-tag>{{ record.reason }}</template>
          </template>
        </a-table>
      </section>

      <section class="panel" v-if="rep.global.length || rep.http_snippet">
        <h2>全局与其他内容</h2>
        <template v-if="rep.http_snippet">
          <div class="sub">http 层白名单内的指令转为片段（应用后需审批）：</div>
          <pre class="snip">{{ rep.http_snippet }}</pre>
        </template>
        <a-table :data-source="rep.global" :pagination="{ pageSize: 20 }" size="small" :row-key="(r: any) => r.file + r.line + r.directive + r.reason"
          :columns="[{ title: '位置', key: 'loc', width: 260 }, { title: '指令', dataIndex: 'directive', width: 160 }, { title: '说明', dataIndex: 'reason' }]">
          <template #bodyCell="{ column, record }">
            <template v-if="column.key === 'loc'"><span class="code">{{ record.file }}:{{ record.line }}</span></template>
          </template>
        </a-table>
      </section>

      <section class="panel" v-if="current.result">
        <h2>应用结果</h2>
        <p>新建上游：{{ current.result.upstreams.join('、') || '无' }}；导入证书：{{ current.result.certificates.join('、') || '无' }}；提交片段 {{ current.result.snippets.length }} 个（到“自定义片段”审批）。</p>
        <ul>
          <li v-for="s in current.result.sites" :key="s.name">{{ s.name }}：<a-tag :color="s.enabled ? 'green' : 'default'">{{ s.enabled ? '已启用（草稿）' : '已停用' }}</a-tag>{{ s.note }}</li>
        </ul>
        <a-alert v-for="(e, i) in current.result.errors" :key="i" type="warning" :message="e" style="margin-top: 6px" />
      </section>
    </template>

    <section class="panel">
      <h2>导入记录</h2>
      <a-table :data-source="history" row-key="id" :pagination="false" size="middle"
        :columns="[{ title: '#', dataIndex: 'id', width: 60 }, { title: '文件', dataIndex: 'file_name' }, { title: '结果', key: 'sum' }, { title: '上传', key: 'by', width: 170 }, { title: '状态', key: 'st', width: 110 }, { title: '', key: 'op', width: 80 }]">
        <template #emptyText><a-empty description="还没有导入过配置" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'sum'">{{ record.sites }} 个站点：完全 {{ record.full }} / 部分 {{ record.partial }} / 无法 {{ record.failed }}</template>
          <template v-else-if="column.key === 'by'">{{ record.created_by }} · {{ dayjs(record.created_at).format('MM-DD HH:mm') }}</template>
          <template v-else-if="column.key === 'st'"><a-tag :color="record.applied_at ? 'green' : 'default'">{{ record.applied_at ? '已应用' : '未应用' }}</a-tag></template>
          <template v-else-if="column.key === 'op'"><a @click="open(record.id)">查看</a></template>
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
  gap: 12px;
  flex-wrap: wrap;
  margin-bottom: 8px;
}
.phead h2 {
  margin: 0;
}
.stats {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
  gap: 10px;
}
.stats div {
  display: flex;
  flex-direction: column;
}
.stats b {
  font-size: 20px;
  color: #16323a;
}
.stats span,
.sub {
  font-size: 12px;
  color: #6b7c80;
}
.routes {
  margin: 8px 0;
  padding-left: 18px;
}
.snip {
  margin: 4px 0 8px;
  padding: 8px 10px;
  background: #f6f8f8;
  border: 1px solid #e1e7e6;
  border-radius: 6px;
  font-family: 'JetBrains Mono', 'SFMono-Regular', Menlo, Consolas, monospace;
  font-size: 12px;
  white-space: pre-wrap;
}
</style>
