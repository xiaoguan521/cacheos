<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message } from 'antdv-next'
import dayjs from 'dayjs'
import { api, errMsg } from '../api/client'
import { editionName, useSession } from '../stores/session'

const session = useSession()
const q = ref('')
const rows = ref<any[]>([])
const loading = ref(false)
const current = ref<any>(null)
const orders = ref<any[]>([])

const custOpen = ref(false)
const custEditing = ref<number | null>(null)
const cust = reactive({ code: '', name: '', contact: '', email: '', phone: '', sales_owner: '', note: '' })

const orderOpen = ref(false)
const order = reactive({ order_no: '', edition: 'standard', nodes: 10, sites: 50, days: 365, grace_days: 30, amount: '', contract_no: '', note: '' })

async function load() {
  loading.value = true
  try {
    rows.value = await api.get('/customers', { q: q.value })
  } finally {
    loading.value = false
  }
}
async function pick(r: any) {
  current.value = r
  orders.value = await api.get('/orders', { customer_id: r.id })
}
function openCust(r?: any) {
  custEditing.value = r?.id ?? null
  Object.assign(cust, { code: '', name: '', contact: '', email: '', phone: '', sales_owner: '', note: '' }, r || {})
  custOpen.value = true
}
async function saveCust() {
  try {
    if (custEditing.value) await api.put(`/customers/${custEditing.value}`, cust)
    else await api.post('/customers', cust)
    message.success('已保存')
    custOpen.value = false
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function saveOrder() {
  try {
    await api.post('/orders', { ...order, customer_id: current.value.id })
    message.success('订单已创建')
    orderOpen.value = false
    await pick(current.value)
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
        <h1>客户与订单</h1>
        <p>订单决定 License 的版本、节点数、站点数与有效期；起草 License 时选择订单。</p>
      </div>
      <a-space>
        <a-input-search v-model:value="q" placeholder="编码 / 名称" allow-clear @search="load" style="width: 220px" />
        <a-button v-if="session.has('sales')" type="primary" @click="openCust()">新建客户</a-button>
      </a-space>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" :loading="loading" size="middle"
        :on-row="(r: any) => ({ onClick: () => pick(r), style: { cursor: 'pointer' } })"
        :row-class-name="(r: any) => (current && r.id === current.id ? 'sel' : '')"
        :columns="[
          { title: '编码', dataIndex: 'code', width: 140 },
          { title: '名称', dataIndex: 'name' },
          { title: '联系人', dataIndex: 'contact' },
          { title: '销售', dataIndex: 'sales_owner' },
          { title: '', key: 'op', width: 80 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'op' && session.has('sales')">
            <a @click.stop="openCust(record)">编辑</a>
          </template>
        </template>
        <template #emptyText>还没有客户</template>
      </a-table>
    </section>
    <section class="panel" v-if="current">
      <div class="row-head">
        <h2>{{ current.name }} 的订单</h2>
        <a-button v-if="session.has('sales')" @click="orderOpen = true">新建订单</a-button>
      </div>
      <a-table :data-source="orders" row-key="id" :pagination="false" size="middle"
        :columns="[
          { title: '订单号', dataIndex: 'order_no' },
          { title: '版本', key: 'edition' },
          { title: '节点 / 站点', key: 'limits' },
          { title: '有效期', key: 'days' },
          { title: '合同号', dataIndex: 'contract_no' },
          { title: '金额', dataIndex: 'amount' },
          { title: '创建', key: 'created' },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'edition'">{{ editionName[record.edition] || record.edition }}</template>
          <template v-else-if="column.key === 'limits'">{{ record.nodes || '不限' }} / {{ record.sites || '不限' }}</template>
          <template v-else-if="column.key === 'days'">{{ record.days }} 天（宽限 {{ record.grace_days }} 天）</template>
          <template v-else-if="column.key === 'created'">{{ record.created_by }} · {{ dayjs(record.created_at).format('YYYY-MM-DD') }}</template>
        </template>
        <template #emptyText>该客户还没有订单</template>
      </a-table>
    </section>

    <a-modal v-model:open="custOpen" :title="custEditing ? '编辑客户' : '新建客户'" @ok="saveCust">
      <a-form layout="vertical">
        <a-form-item label="客户编码（写入 License，创建后不可改）"><a-input v-model:value="cust.code" :disabled="!!custEditing" placeholder="如 C0001" /></a-form-item>
        <a-form-item label="名称"><a-input v-model:value="cust.name" /></a-form-item>
        <a-form-item label="联系人"><a-input v-model:value="cust.contact" /></a-form-item>
        <a-form-item label="邮箱"><a-input v-model:value="cust.email" /></a-form-item>
        <a-form-item label="电话"><a-input v-model:value="cust.phone" /></a-form-item>
        <a-form-item label="销售负责人"><a-input v-model:value="cust.sales_owner" /></a-form-item>
        <a-form-item label="备注"><a-textarea v-model:value="cust.note" :rows="2" /></a-form-item>
      </a-form>
    </a-modal>
    <a-modal v-model:open="orderOpen" title="新建订单" @ok="saveOrder">
      <a-form layout="vertical">
        <a-form-item label="订单号"><a-input v-model:value="order.order_no" /></a-form-item>
        <a-form-item label="版本">
          <a-select v-model:value="order.edition" :options="Object.entries(editionName).map(([value, label]) => ({ value, label }))" />
        </a-form-item>
        <a-space>
          <a-form-item label="节点数（0 不限）"><a-input-number v-model:value="order.nodes" :min="0" /></a-form-item>
          <a-form-item label="站点数（0 不限）"><a-input-number v-model:value="order.sites" :min="0" /></a-form-item>
        </a-space>
        <a-space>
          <a-form-item label="有效天数"><a-input-number v-model:value="order.days" :min="1" /></a-form-item>
          <a-form-item label="宽限天数"><a-input-number v-model:value="order.grace_days" :min="0" /></a-form-item>
        </a-space>
        <a-form-item label="合同号"><a-input v-model:value="order.contract_no" /></a-form-item>
        <a-form-item label="金额"><a-input v-model:value="order.amount" /></a-form-item>
        <a-form-item label="备注"><a-textarea v-model:value="order.note" :rows="2" /></a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.row-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}
.row-head h2 {
  margin: 0;
}
:deep(.sel td) {
  background: #eef7f7 !important;
}
</style>
