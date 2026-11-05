<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const templates = ref<any[]>([])
const catalog = ref<any[]>([])
const open = ref(false)
const editing = ref<any>(null)
const form = reactive({ name: '', description: '', paramsText: '{}' })

const catalogNames = computed(() => catalog.value.map((p: any) => p.name).filter(Boolean).join('、'))

async function load() {
  try {
    const [t, c] = await Promise.all([api.get('/param-templates'), api.get('/params/catalog')])
    templates.value = t
    catalog.value = c
  } catch (e) {
    message.error(errMsg(e))
  }
}

function edit(template?: any) {
  editing.value = template || null
  form.name = template?.name || ''
  form.description = template?.description || ''
  form.paramsText = JSON.stringify(template?.params || {}, null, 2)
  open.value = true
}

async function save() {
  let params: Record<string, string>
  try {
    params = JSON.parse(form.paramsText)
    if (!params || Array.isArray(params) || typeof params !== 'object') throw new Error('参数必须是 JSON 对象')
  } catch (e) {
    message.error(`参数 JSON 无效：${e instanceof Error ? e.message : String(e)}`)
    return
  }
  try {
    const body = { name: form.name, description: form.description, params }
    if (editing.value) await api.put(`/param-templates/${editing.value.id}`, body)
    else await api.post('/param-templates', body)
    open.value = false
    message.success('模板已保存')
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}

async function remove(template: any) {
  try {
    await api.del(`/param-templates/${template.id}`)
    message.success('模板已删除')
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
      <div><h1>参数模板</h1><p>模板只保存受管参数；内存上限由部署单元规格决定。</p></div>
      <a-space><a-button @click="load">刷新</a-button><a-button type="primary" :disabled="!session.can('operator') || !session.writable" @click="edit()">新建模板</a-button></a-space>
    </div>
    <section class="panel">
      <a-alert v-if="catalogNames" type="info" show-icon style="margin-bottom: 16px" :message="`可用参数：${catalogNames}`" />
      <a-table :data-source="templates" row-key="id" :pagination="false" :columns="[{ title: '名称', key: 'name' }, { title: '说明', dataIndex: 'description' }, { title: '类型', key: 'type', width: 100 }, { title: '参数数', key: 'count', width: 100 }, { title: '更新时间', key: 'updated', width: 180 }, { title: '', key: 'actions', width: 130 }]">
        <template #emptyText><a-empty description="还没有参数模板" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'"><b>{{ record.name }}</b><a-tag v-if="record.builtin" color="blue" style="margin-left: 8px">内置</a-tag></template>
          <template v-else-if="column.key === 'type'">{{ record.builtin ? '系统模板' : '自定义' }}</template>
          <template v-else-if="column.key === 'count'">{{ Object.keys(record.params || {}).length }}</template>
          <template v-else-if="column.key === 'updated'"><span class="num">{{ record.updated_at ? new Date(record.updated_at).toLocaleString() : '—' }}</span></template>
          <template v-else-if="column.key === 'actions' && !record.builtin && session.can('operator')"><a-space :size="8"><a @click="edit(record)">编辑</a><a-popconfirm title="删除这个参数模板？" @confirm="remove(record)"><a class="danger">删除</a></a-popconfirm></a-space></template>
        </template>
      </a-table>
    </section>

    <a-modal v-model:open="open" :title="editing ? '编辑参数模板' : '新建参数模板'" ok-text="保存" @ok="save">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="form.name" :disabled="!!editing?.builtin" placeholder="例如 high-throughput" /></a-form-item>
        <a-form-item label="说明"><a-input v-model:value="form.description" /></a-form-item>
        <a-form-item label="参数 JSON" extra="格式为 { &quot;参数名&quot;: &quot;值&quot; }，服务端会校验并归一化。"><a-textarea v-model:value="form.paramsText" :rows="12" class="code" /></a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.danger { color: #a3332d; }
</style>
