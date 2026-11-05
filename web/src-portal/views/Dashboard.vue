<script setup lang="ts">
import { onMounted, ref } from 'vue'
import dayjs from 'dayjs'
import { api } from '../api/client'
import { statusName } from '../stores/session'

const d = ref<any>(null)
onMounted(async () => (d.value = await api.get('/dashboard')))
const order = ['submitted', 'approved', 'batched', 'issued', 'draft', 'rejected', 'revoked']
</script>

<template>
  <div class="page" v-if="d">
    <div class="page-head">
      <div>
        <h1>总览</h1>
        <p>License 流转：销售起草 → 商务审批 → 签名员导出批次 → 离线签名 → 导入签发</p>
      </div>
    </div>
    <a-alert v-if="!d.active_keys" type="warning" show-icon style="margin-bottom: 16px"
      message="还没有启用的签名公钥：完成离线密钥仪式后，由管理员在“签名公钥”中登记。" />
    <a-alert v-if="!d.audit_intact" type="error" show-icon style="margin-bottom: 16px" message="审计哈希链校验失败，请到“审计”页查看。" />
    <div class="cards">
      <router-link v-for="s in order" :key="s" class="card" :to="{ name: 'licenses', query: { status: s } }">
        <div class="n num">{{ d.counts[s] || 0 }}</div>
        <div class="l">{{ statusName[s] }}</div>
      </router-link>
      <router-link class="card" :to="{ name: 'revocations' }">
        <div class="n num">{{ d.pending_revocations }}</div>
        <div class="l">待发布的吊销</div>
      </router-link>
      <router-link class="card" :to="{ name: 'batches' }">
        <div class="n num">{{ d.open_batches }}</div>
        <div class="l">等待签名的批次</div>
      </router-link>
    </div>
    <section class="panel">
      <h2>60 天内到期、尚未续期</h2>
      <a-table :data-source="d.expiring" row-key="id" :pagination="false" size="middle"
        :columns="[
          { title: 'License', dataIndex: 'license_id' },
          { title: '客户', dataIndex: 'customer_name' },
          { title: '到期日', key: 'exp' },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'exp'">{{ dayjs(record.expires_at).format('YYYY-MM-DD') }}（{{ dayjs(record.expires_at).diff(dayjs(), 'day') }} 天）</template>
        </template>
        <template #emptyText>没有即将到期的 License</template>
      </a-table>
    </section>
  </div>
</template>

<style scoped>
.cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 12px;
  margin-bottom: 16px;
}
.card {
  background: #fff;
  border: 1px solid #e1e7e6;
  border-radius: 8px;
  padding: 16px;
  color: inherit;
}
.card:hover {
  border-color: #0e7c86;
}
.n {
  font-size: 24px;
  font-weight: 600;
  color: #16323a;
}
.l {
  color: #6b7c80;
  font-size: 13px;
}
</style>
