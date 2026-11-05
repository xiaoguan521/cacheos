<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const data = ref<any>(null)
const typeName: Record<string, string> = { webhook: 'Webhook', wecom: '企业微信机器人', dingtalk: '钉钉机器人', email: '邮件' }
const unit: Record<string, string> = { node_offline: '分钟', error_rate: '%', cert_expiry: '天', drift: '' }
const hint: Record<string, string> = {
  node_offline: '节点超过该时长没有心跳',
  error_rate: '近 5 分钟某站点 5xx 占比达到该值（请求不少于 50 次）',
  cert_expiry: '证书剩余天数少于该值',
  drift: '节点上的配置被手工改动',
}

async function load() {
  data.value = await api.get('/alerts')
}
onMounted(load)

const ruleEdit = reactive<Record<string, any>>({})
function ruleOf(k: string) {
  if (!ruleEdit[k]) {
    const r = data.value.rules.find((x: any) => x.kind === k)
    ruleEdit[k] = { enabled: r.enabled, threshold: r.threshold, channel_ids: (r.channel_ids || '').split(',').filter(Boolean).map(Number) }
  }
  return ruleEdit[k]
}
async function saveRule(k: string) {
  try {
    await api.put(`/alert-rules/${k}`, ruleOf(k))
    message.success('已保存')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

const open = ref(false)
const editId = ref(0)
const form = reactive({ name: '', type: 'webhook', url: '', secret: '', smtp_user: '', mail_from: '', mail_to: '', enabled: true })
function edit(ch?: any) {
  editId.value = ch?.id || 0
  Object.assign(form, { name: '', type: 'webhook', url: '', secret: '', smtp_user: '', mail_from: '', mail_to: '', enabled: true }, ch ? { ...ch, secret: '' } : {})
  open.value = true
}
async function saveChannel() {
  try {
    if (editId.value) await api.put(`/alert-channels/${editId.value}`, form)
    else await api.post('/alert-channels', form)
    open.value = false
    message.success('已保存')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function test(ch: any) {
  try {
    await api.post(`/alert-channels/${ch.id}/test`, {})
    message.success('测试消息已发送')
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function del(ch: any) {
  await api.del(`/alert-channels/${ch.id}`)
  await load()
}
const channelOpts = computed(() => (data.value?.channels || []).map((c: any) => ({ value: c.id, label: c.name })))
const typeOpts = computed(() =>
  Object.entries(typeName).map(([value, label]) => ({ value, label, disabled: (value === 'wecom' || value === 'dingtalk') && !data.value?.im_licensed })),
)
</script>

<template>
  <div class="page" v-if="data">
    <div class="page-head">
      <div>
        <h1>告警</h1>
        <p>控制台每分钟评估一次规则，告警触发和恢复时各推送一次；同一对象不会重复推送。</p>
      </div>
    </div>

    <section class="panel">
      <h2>规则</h2>
      <div class="rules">
        <div v-for="r in data.rules" :key="r.kind" class="rule">
          <div class="rh">
            <a-switch :checked="ruleOf(r.kind).enabled" :disabled="!session.can('admin')" @change="(v: any) => (ruleOf(r.kind).enabled = v)" />
            <b>{{ data.kind_names[r.kind] }}</b>
          </div>
          <div class="sub">{{ hint[r.kind] }}</div>
          <a-space wrap>
            <a-input-number v-if="unit[r.kind]" v-model:value="ruleOf(r.kind).threshold" :min="0" :addon-after="unit[r.kind]" style="width: 150px" />
            <a-select v-model:value="ruleOf(r.kind).channel_ids" mode="multiple" placeholder="推送通道" style="min-width: 220px" :options="channelOpts" />
            <a-button v-if="session.can('admin')" size="small" :disabled="!session.writable" @click="saveRule(r.kind)">保存</a-button>
          </a-space>
        </div>
      </div>
    </section>

    <section class="panel">
      <div class="phead">
        <h2>通道</h2>
        <a-button v-if="session.can('admin')" size="small" :disabled="!session.writable" @click="edit()">新建通道</a-button>
      </div>
      <p v-if="!data.im_licensed" class="sub">当前授权不包含企业微信 / 钉钉告警（专业版及以上），可使用邮件与 Webhook。</p>
      <a-table :data-source="data.channels" row-key="id" :pagination="false" size="middle"
        :columns="[{ title: '名称', dataIndex: 'name' }, { title: '类型', key: 'type' }, { title: '地址', key: 'url' }, { title: '状态', key: 'st', width: 90 }, { title: '', key: 'op', width: 200 }]">
        <template #emptyText><a-empty description="还没有告警通道" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'type'">{{ typeName[record.type] }}</template>
          <template v-else-if="column.key === 'url'"><span class="code url">{{ record.url }}</span><a-tag v-if="record.has_secret" style="margin-left: 6px">已配置密钥</a-tag></template>
          <template v-else-if="column.key === 'st'"><a-tag :color="record.enabled ? 'green' : 'default'">{{ record.enabled ? '启用' : '停用' }}</a-tag></template>
          <template v-else-if="column.key === 'op' && session.can('admin')">
            <a-space :size="4">
              <a-button size="small" @click="test(record)">发送测试</a-button>
              <a-button size="small" @click="edit(record)">编辑</a-button>
              <a-popconfirm title="删除该通道？" @confirm="del(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </section>

    <section class="panel">
      <h2>最近告警</h2>
      <a-table :data-source="data.events" row-key="id" size="small" :pagination="{ pageSize: 20 }"
        :columns="[{ title: '时间', key: 'ts', width: 150 }, { title: '规则', key: 'kind', width: 130 }, { title: '内容', dataIndex: 'message' }, { title: '状态', key: 'st', width: 160 }, { title: '通知', dataIndex: 'notify' }]">
        <template #emptyText><a-empty description="没有告警" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'ts'">{{ dayjs(record.fired_at).format('MM-DD HH:mm:ss') }}</template>
          <template v-else-if="column.key === 'kind'">{{ data.kind_names[record.kind] }}</template>
          <template v-else-if="column.key === 'st'">
            <a-tag v-if="record.status === 'firing'" color="red">告警中</a-tag>
            <span v-else class="sub">已恢复 {{ dayjs(record.resolved_at).format('MM-DD HH:mm') }}</span>
          </template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="open" :title="editId ? '编辑通道' : '新建通道'" @ok="saveChannel">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="form.name" /></a-form-item>
        <a-form-item label="类型"><a-select v-model:value="form.type" :options="typeOpts" /></a-form-item>
        <template v-if="form.type === 'email'">
          <a-form-item label="邮件服务器（smtp://主机:587 使用 STARTTLS，smtps://主机:465）"><a-input v-model:value="form.url" placeholder="smtp://smtp.example.com:587" /></a-form-item>
          <a-form-item label="用户名"><a-input v-model:value="form.smtp_user" autocomplete="off" /></a-form-item>
          <a-form-item :label="editId ? '口令（留空不改）' : '口令'"><a-input-password v-model:value="form.secret" autocomplete="new-password" /></a-form-item>
          <a-form-item label="发件人"><a-input v-model:value="form.mail_from" /></a-form-item>
          <a-form-item label="收件人（逗号分隔）"><a-input v-model:value="form.mail_to" /></a-form-item>
        </template>
        <template v-else>
          <a-form-item label="地址"><a-input v-model:value="form.url" :placeholder="form.type === 'wecom' ? 'https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=…' : form.type === 'dingtalk' ? 'https://oapi.dingtalk.com/robot/send?access_token=…' : 'https://'" /></a-form-item>
          <a-form-item v-if="form.type === 'dingtalk'" :label="editId ? '加签密钥（留空不改）' : '加签密钥（可选）'"><a-input-password v-model:value="form.secret" autocomplete="new-password" /></a-form-item>
        </template>
        <a-checkbox v-model:checked="form.enabled">启用</a-checkbox>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.rules {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(320px, 1fr));
  gap: 12px;
}
.rule {
  border: 1px solid #e1e7e6;
  border-radius: 8px;
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.rh {
  display: flex;
  gap: 8px;
  align-items: center;
}
.phead {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 8px;
}
.phead h2 {
  margin: 0;
}
.sub {
  font-size: 12px;
  color: #6b7c80;
}
.url {
  word-break: break-all;
}
</style>
