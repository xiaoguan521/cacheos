<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import AuthFrame from '../components/AuthFrame.vue'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const router = useRouter()
const session = useSession()
const form = reactive({ old_password: '', new_password: '', confirm: '' })
const loading = ref(false)
const error = ref('')

async function submit() {
  error.value = ''
  if (form.new_password !== form.confirm) {
    error.value = '两次输入的新口令不一致'
    return
  }
  loading.value = true
  try {
    await api.put('/auth/password', { old_password: form.old_password, new_password: form.new_password })
    await session.loadMe()
    await session.loadLicense()
    router.push('/dashboard')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <AuthFrame title="修改口令">
    <p class="hint">首次登录或口令已过有效期，需要先设置新口令。新口令至少 10 个字符，包含大写字母、小写字母、数字、符号中的至少 3 类，且不能与最近使用过的口令相同。</p>
    <a-form layout="vertical" :model="form" @finish="submit">
      <a-form-item label="当前口令" name="old_password" :rules="[{ required: true, message: '请输入当前口令' }]">
        <a-input-password v-model:value="form.old_password" size="large" autocomplete="current-password" />
      </a-form-item>
      <a-form-item label="新口令" name="new_password" :rules="[{ required: true, message: '请输入新口令' }]">
        <a-input-password v-model:value="form.new_password" size="large" autocomplete="new-password" />
      </a-form-item>
      <a-form-item label="再次输入新口令" name="confirm" :rules="[{ required: true, message: '请再次输入' }]">
        <a-input-password v-model:value="form.confirm" size="large" autocomplete="new-password" />
      </a-form-item>
      <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />
      <a-button type="primary" html-type="submit" size="large" block :loading="loading">保存并进入控制台</a-button>
    </a-form>
  </AuthFrame>
</template>

<style scoped>
.hint {
  color: #6b7c80;
  font-size: 13px;
  margin: 0 0 16px;
}
</style>
