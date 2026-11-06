<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AuthFrame from '../components/AuthFrame.vue'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const router = useRouter()
const route = useRoute()
const session = useSession()
const form = reactive({ username: '', password: '', otp: '' })
const loading = ref(false)
const error = ref((route.query.sso_error as string) || '')
const sso = ref(false)
onMounted(async () => {
  sso.value = !!(await api.get('/setup/status').catch(() => ({}))).oidc
})

async function submit() {
  error.value = ''
  loading.value = true
  try {
    const r = await api.post('/auth/login', form)
    if (r.must_change_password) {
      router.push({ name: 'password' })
      return
    }
    await session.loadMe()
    await session.loadLicense()
    router.push((route.query.next as string) || '/dashboard')
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <AuthFrame title="登录控制台">
    <a-form layout="vertical" :model="form" @finish="submit">
      <a-form-item label="用户名" name="username" :rules="[{ required: true, message: '请输入用户名' }]">
        <a-input v-model:value="form.username" size="large" autocomplete="username" autofocus />
      </a-form-item>
      <a-form-item label="密码" name="password" :rules="[{ required: true, message: '请输入密码' }]">
        <a-input-password v-model:value="form.password" size="large" autocomplete="current-password" />
      </a-form-item>
      <a-form-item label="身份验证器验证码" name="otp">
        <a-input v-model:value="form.otp" size="large" inputmode="numeric" autocomplete="one-time-code" maxlength="6" placeholder="如未启用则留空" />
      </a-form-item>
      <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />
      <a-button type="primary" html-type="submit" size="large" block :loading="loading">登录</a-button>
      <a-button v-if="sso" size="large" block style="margin-top: 12px" href="/api/v1/auth/oidc/start">使用单点登录</a-button>
    </a-form>
  </AuthFrame>
</template>
