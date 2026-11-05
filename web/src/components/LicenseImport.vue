<script setup lang="ts">
import { ref } from 'vue'
import { message } from 'antdv-next'
import { api, errMsg, http } from '../api/client'
import { useSession } from '../stores/session'

const emit = defineEmits<{ imported: [] }>()
const session = useSession()
const raw = ref('')
const fileEl = ref<HTMLInputElement>()
const loading = ref(false)
const error = ref('')

async function downloadRequest() {
  const r = await http.get('/license/request', { responseType: 'blob' })
  const a = document.createElement('a')
  a.href = URL.createObjectURL(r.data)
  a.download = 'activation-request.json'
  a.click()
  URL.revokeObjectURL(a.href)
}

function readFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  f.text().then((t) => (raw.value = t.trim()))
}

async function submit() {
  error.value = ''
  loading.value = true
  try {
    await api.post('/license/import', { raw: raw.value })
    await session.loadLicense()
    message.success('授权已导入')
    raw.value = ''
    emit('imported')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="imp">
    <ol class="how">
      <li>
        下载激活申请文件，发给厂商。
        <a-button size="small" @click="downloadRequest">下载激活申请</a-button>
      </li>
      <li>收到 .lic 授权文件后，选择文件或粘贴内容导入。厂商随附的吊销列表（etos-crl1. 开头的一行）可以一起粘贴，也可以单独导入。</li>
    </ol>
    <input ref="fileEl" type="file" accept=".lic,.crl,.txt" hidden @change="readFile" />
    <a-button size="small" style="margin-bottom: 10px" @click="fileEl?.click()">选择 .lic 文件</a-button>
    <a-textarea v-model:value="raw" :rows="5" placeholder="粘贴 License 内容（可附带吊销列表，每行一项）" class="code" />
    <a-alert v-if="error" type="error" :message="error" show-icon style="margin-top: 12px" />
    <a-button type="primary" block size="large" style="margin-top: 16px" :disabled="!raw" :loading="loading" @click="submit">导入授权</a-button>
  </div>
</template>

<style scoped>
.how {
  margin: 0 0 16px;
  padding-left: 18px;
  color: #3b4a4e;
  line-height: 2;
}
.file {
  margin-bottom: 10px;
  font-size: 13px;
}
</style>
