<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'antdv-next'
import dayjs from 'dayjs'
import { api, download, errMsg } from '../api/client'

const revs = ref<any[]>([])
const crls = ref<any[]>([])
async function load() {
  ;[revs.value, crls.value] = await Promise.all([api.get('/revocations'), api.get('/crls')])
}
async function dl() {
  try {
    await download('/crls/latest/download', 'latest.crl')
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
        <h1>吊销</h1>
        <p>吊销列表是累积的、序号单调递增；客户控制台只接受比当前更新的吊销列表。在 License 页申请吊销。</p>
      </div>
      <a-button type="primary" :disabled="!crls.length" @click="dl">下载最新吊销列表</a-button>
    </div>
    <section class="panel">
      <h2>吊销记录</h2>
      <a-table :data-source="revs" row-key="id" size="middle" :pagination="{ pageSize: 20 }"
        :columns="[
          { title: 'License', dataIndex: 'license_id' },
          { title: '原因', dataIndex: 'reason' },
          { title: '状态', key: 'st', width: 160 },
          { title: '申请', key: 'who', width: 200 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'st'">
            <a-tag v-if="record.crl_serial" color="green">已发布 #{{ record.crl_serial }}</a-tag>
            <a-tag v-else color="orange">等待签名发布</a-tag>
          </template>
          <template v-else-if="column.key === 'who'">{{ record.created_by }} · {{ dayjs(record.created_at).format('YYYY-MM-DD HH:mm') }}</template>
        </template>
        <template #emptyText>没有吊销记录</template>
      </a-table>
    </section>
    <section class="panel">
      <h2>已发布的吊销列表</h2>
      <a-table :data-source="crls" row-key="id" size="middle" :pagination="false"
        :columns="[
          { title: '序号', dataIndex: 'serial', width: 90 },
          { title: '签名密钥', dataIndex: 'kid' },
          { title: '吊销条数', dataIndex: 'revoked', width: 100 },
          { title: '发布时间', key: 'ts', width: 180 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'ts'">{{ dayjs(record.published_at).format('YYYY-MM-DD HH:mm') }}</template>
        </template>
        <template #emptyText>还没有发布过吊销列表</template>
      </a-table>
    </section>
  </div>
</template>
