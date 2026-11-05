<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { message, Modal } from 'antdv-next'
import dayjs from 'dayjs'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const session = useSession()
const rows = ref<any[]>([])
const open = ref(false)
const form = reactive({ kid: '', public_key: '', note: '' })
const keyStatus: Record<string, { t: string; c: string }> = {
  active: { t: '启用', c: 'green' },
  retired: { t: '停用（仅验证）', c: 'default' },
  revoked: { t: '已作废', c: 'red' },
}
async function load() {
  rows.value = await api.get('/keys')
}
async function save() {
  try {
    await api.post('/keys', form)
    message.success('公钥已登记')
    open.value = false
    Object.assign(form, { kid: '', public_key: '', note: '' })
    await load()
  } catch (e) {
    message.error(errMsg(e))
  }
}
function setStatus(k: any, status: string) {
  const run = async () => {
    try {
      await api.put(`/keys/${k.id}/status`, { status })
      message.success('已更新')
      await load()
    } catch (e) {
      message.error(errMsg(e))
    }
  }
  if (status !== 'revoked') return run()
  Modal.confirm({
    title: `作废 ${k.kid}？`,
    content: '作废不可撤销：之后不再接受该密钥签名的批次。若私钥泄露，还需发布包含该 kid 的新版本（revokedKIDs）。',
    okButtonProps: { danger: true },
    onOk: run,
  })
}
onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>签名公钥</h1>
        <p>本系统只保存公钥。私钥在离线签名机上以口令加密文件存在，由 syapp-license-signer keygen 生成。</p>
      </div>
      <a-button v-if="session.has('admin')" type="primary" @click="open = true">登记公钥</a-button>
    </div>
    <section class="panel">
      <a-table :data-source="rows" row-key="id" size="middle" :pagination="false" :scroll="{ x: 880 }"
        :columns="[
          { title: 'kid', key: 'kid' },
          { title: '指纹', key: 'fp' },
          { title: '状态', key: 'status', width: 140 },
          { title: '备注', dataIndex: 'note' },
          { title: '登记', key: 'who', width: 170 },
          { title: '', key: 'op', width: 150 },
        ]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'kid'">
            <span class="num">{{ record.kid }}</span>
            <a-tooltip v-if="!record.builtin" title="该公钥尚未内置到当前版本的控制台中：用它签发的 License 需等含此公钥的版本发布后才能被客户验证。">
              <a-tag color="orange" style="margin-left: 6px">未内置</a-tag>
            </a-tooltip>
          </template>
          <template v-else-if="column.key === 'fp'"><span class="num">{{ record.fingerprint }}</span></template>
          <template v-else-if="column.key === 'status'"><a-tag :color="keyStatus[record.status]?.c">{{ keyStatus[record.status]?.t }}</a-tag></template>
          <template v-else-if="column.key === 'who'">{{ record.created_by }} · {{ dayjs(record.created_at).format('YYYY-MM-DD') }}</template>
          <template v-else-if="column.key === 'op' && session.has('admin') && record.status !== 'revoked'">
            <a-space :size="4">
              <a-button v-if="record.status === 'active'" size="small" @click="setStatus(record, 'retired')">停用</a-button>
              <a-button v-else size="small" @click="setStatus(record, 'active')">启用</a-button>
              <a-button size="small" danger @click="setStatus(record, 'revoked')">作废</a-button>
            </a-space>
          </template>
        </template>
        <template #emptyText>还没有登记签名公钥</template>
      </a-table>
    </section>
    <a-modal v-model:open="open" title="登记签名公钥" @ok="save">
      <a-alert type="info" show-icon style="margin-bottom: 12px"
        message="把离线签名机上 keygen 输出的 kid 与公钥抄过来，并与输出的指纹逐位核对（两人复核）。切勿粘贴私钥文件。" />
      <a-form layout="vertical">
        <a-form-item label="kid（如 etos-prod-2026）"><a-input v-model:value="form.kid" /></a-form-item>
        <a-form-item label="公钥（base64）"><a-input v-model:value="form.public_key" /></a-form-item>
        <a-form-item label="备注"><a-input v-model:value="form.note" placeholder="密钥仪式日期、保管人等" /></a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>
