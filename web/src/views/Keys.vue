<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'

const route = useRoute()
const router = useRouter()
const unit = ref<any>(null)
const pattern = ref('app:*')
const cursor = ref('0')
const rows = ref<any[]>([])
const loading = ref(false)
const selected = ref<any>(null)
const id = computed(() => String(route.params.id))

async function search(reset = true) {
  loading.value = true
  try {
    const r = await api.get(`/units/${id.value}/keys`, { pattern: pattern.value, cursor: reset ? '0' : cursor.value, count: 100 })
    rows.value = reset ? (r.items || []) : rows.value.concat(r.items || [])
    cursor.value = r.cursor || '0'
  } catch (e) { message.error(errMsg(e)) } finally { loading.value = false }
}

async function reveal(row: any) {
  try {
    const r = await api.get(`/units/${id.value}/keys`, { pattern: pattern.value, key: row.key, reveal: 1 })
    selected.value = r.items?.[0] || row
  } catch (e) { message.error(errMsg(e)) }
}

onMounted(async () => {
  try { unit.value = await api.get(`/units/${id.value}`); await search() } catch (e) { message.error(errMsg(e)) }
})
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div><a @click="router.push({ name: 'unit-detail', params: { id } })">← {{ unit?.name || '部署单元' }}</a><h1>Key 浏览器</h1><p>只允许前缀 SCAN；默认只展示 Key、类型、TTL 与内存大小，明文查看需要单独授权并记审计。</p></div>
    </div>
    <section class="panel controls">
      <a-input v-model:value="pattern" style="max-width: 360px" placeholder="例如 app:*" @press-enter="search()" />
      <a-button type="primary" :loading="loading" @click="search()">扫描</a-button>
      <a-button :disabled="cursor === '0' || loading" @click="search(false)">下一页</a-button>
    </section>
    <section class="panel">
      <a-table :data-source="rows" row-key="key" size="small" virtual :scroll="{ y: 560, x: 760 }" :pagination="false" :columns="[{ title: 'Key', dataIndex: 'key', width: 420 }, { title: '类型', dataIndex: 'type', width: 100 }, { title: 'TTL', key: 'ttl', width: 140 }, { title: '大小', key: 'size', width: 120 }, { title: '', key: 'action', width: 100 }]">
        <template #emptyText><a-empty description="没有匹配的 Key" /></template>
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'ttl'">{{ record.ttl_ms < 0 ? '永久' : `${record.ttl_ms} ms` }}</template>
          <template v-else-if="column.key === 'size'">{{ record.bytes || 0 }} B</template>
          <template v-else-if="column.key === 'action'"><a @click="reveal(record)">查看值</a></template>
        </template>
      </a-table>
    </section>
    <a-modal :open="!!selected" title="Key 值（已审计）" :footer="null" @cancel="selected = null">
      <div class="meta"><span>{{ selected?.key }}</span><span>{{ selected?.type }}</span></div>
      <pre class="value">{{ JSON.stringify(selected?.value, null, 2) }}</pre>
    </a-modal>
  </div>
</template>

<style scoped>
.controls { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.meta { display: flex; justify-content: space-between; color: #7b8b8e; font-size: 12px; margin-bottom: 12px; }
.value { max-height: 520px; overflow: auto; background: #f4f7f7; padding: 12px; border-radius: 6px; white-space: pre-wrap; word-break: break-word; }
</style>
