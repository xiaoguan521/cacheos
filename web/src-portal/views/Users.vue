<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message } from 'antdv-next'
import dayjs from 'dayjs'
import { api, errMsg } from '../api/client'
import { roleName, useSession } from '../stores/session'

const session = useSession()
const rows = ref<any[]>([])
const open = ref(false)
const editing = ref<any>(null)
const form = reactive({ username: '', display_name: '', password: '', roles: [] as string[], disabled: false })
const roleOptions = Object.entries(roleName).map(([value, label]) => ({ value, label }))
const roles = (r: string) => (r ? r.split(',') : [])

async function load() {
  rows.value = await api.get('/users')
}
function edit(u?: any) {
  editing.value = u || null
  Object.assign(form, { username: u?.username || '', display_name: u?.display_name || '', password: '', roles: u ? roles(u.roles) : ['sales'], disabled: !!u?.disabled })
  open.value = true
}
async function save() {
  try {
    if (editing.value) await api.put(`/users/${editing.value.id}`, form)
    else await api.post('/users', form)
    message.success('已保存')
    open.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>用户</h1>
        <p>建议销售、商务、签名员由不同的人担任；同一张 License 的审批人不能是起草人或提交人。</p>
      </div>
      <a-button type="primary" @click="edit()">新建用户</a-button>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" size="middle" :pagination="false"
        :columns="[
          { title: '用户名', dataIndex: 'username' },
          { title: '姓名', dataIndex: 'display_name' },
          { title: '角色', key: 'roles' },
          { title: '状态', key: 'st', width: 90 },
          { title: '最近登录', key: 'last', width: 160 },
          { title: '', key: 'op', width: 70 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'roles'"><a-tag v-for="r in roles(record.roles)" :key="r">{{ roleName[r] || r }}</a-tag></template>
          <template v-else-if="column.key === 'st'"><a-tag :color="record.disabled ? 'red' : 'green'">{{ record.disabled ? '已停用' : '正常' }}</a-tag></template>
          <template v-else-if="column.key === 'last'">{{ record.last_login_at ? dayjs(record.last_login_at).format('YYYY-MM-DD HH:mm') : '—' }}</template>
          <template v-else-if="column.key === 'op'"><a @click="edit(record)">编辑</a></template>
        </template>
      </a-table>
    </section>
    <a-modal v-model:open="open" :title="editing ? `编辑 ${editing.username}` : '新建用户'" @ok="save">
      <a-form layout="vertical">
        <template v-if="!editing">
          <a-form-item label="用户名"><a-input v-model:value="form.username" /></a-form-item>
          <a-form-item label="姓名"><a-input v-model:value="form.display_name" /></a-form-item>
        </template>
        <a-form-item :label="editing ? '重置口令（留空不改，至少 10 个字符）' : '初始口令（至少 10 个字符）'">
          <a-input-password v-model:value="form.password" autocomplete="new-password" />
        </a-form-item>
        <a-form-item label="角色"><a-checkbox-group v-model:value="form.roles" :options="roleOptions" /></a-form-item>
        <a-form-item v-if="editing">
          <a-checkbox v-model:checked="form.disabled" :disabled="editing.id === session.me?.id">停用该账号</a-checkbox>
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>
