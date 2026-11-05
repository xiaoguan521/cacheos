<script setup lang="ts">
import { onMounted, ref } from 'vue'
import dayjs from 'dayjs'
import { api } from '../api/client'

const rows = ref<any[]>([])
const verify = ref<{ intact: boolean; first_bad_id: number } | null>(null)
const loading = ref(false)
async function load() {
  loading.value = true
  try {
    ;[rows.value, verify.value] = await Promise.all([api.get('/audit', { limit: 500 }), api.get('/audit/verify')])
  } finally {
    loading.value = false
  }
}
onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>审计</h1>
        <p>每条记录包含上一条的哈希，任何删改都会使校验失败。</p>
      </div>
      <a-button :loading="loading" @click="load">重新校验</a-button>
    </div>
    <a-alert v-if="verify" :type="verify.intact ? 'success' : 'error'" show-icon style="margin-bottom: 16px"
      :message="verify.intact ? '哈希链完整' : `哈希链在第 ${verify.first_bad_id} 条记录处断开，审计记录可能被篡改`" />
    <section class="panel">
      <a-table :data-source="rows" row-key="id" size="small" :loading="loading" :pagination="{ pageSize: 50 }" :scroll="{ x: 900 }"
        :columns="[
          { title: '时间', key: 'ts', width: 160 },
          { title: '用户', dataIndex: 'username', width: 110 },
          { title: 'IP', dataIndex: 'ip', width: 130 },
          { title: '动作', dataIndex: 'action', width: 170 },
          { title: '对象', key: 'obj', width: 200 },
          { title: '结果', dataIndex: 'result', width: 80 },
          { title: '详情', key: 'detail' },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'ts'">{{ dayjs(record.ts).format('YYYY-MM-DD HH:mm:ss') }}</template>
          <template v-else-if="column.key === 'obj'">{{ record.object_type }} {{ record.object_id }}</template>
          <template v-else-if="column.key === 'detail'"><span class="num detail">{{ record.detail }}</span></template>
        </template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.detail {
  font-size: 12px;
  word-break: break-all;
}
</style>
