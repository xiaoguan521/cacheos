<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import AuthFrame from '../components/AuthFrame.vue'
import LicenseImport from '../components/LicenseImport.vue'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const router = useRouter()
const session = useSession()
const step = ref(0)
const form = reactive({ username: 'admin', password: '', confirm: '' })
const totp = ref<any>(null)
const loading = ref(false)
const error = ref('')

onMounted(async () => {
  const st = await api.get('/setup/status')
  if (st.initialized) {
    // 已有管理员：未授权时登录后继续导入 License
    router.replace({ name: 'login' })
  }
})

async function createAdmin() {
  error.value = ''
  if (form.password !== form.confirm) {
    error.value = '两次输入的密码不一致'
    return
  }
  loading.value = true
  try {
    totp.value = await api.post('/setup', { username: form.username, password: form.password })
    await session.loadMe()
    await session.loadLicense()
    step.value = 1
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}

async function done() {
  await session.loadLicense()
  router.push('/dashboard')
}
</script>

<template>
  <AuthFrame :title="step === 0 ? '初始化控制台' : '导入授权'" :hint="step === 0 ? '首次启动，先创建管理员账号。' : '导入 License 后即可添加节点、发布配置。'">
    <a-steps :current="step" size="small" style="margin-bottom: 24px" :items="[{ title: '管理员' }, { title: '授权' }]" />
    <a-form v-if="step === 0" layout="vertical" :model="form" @finish="createAdmin">
      <a-form-item label="用户名" name="username" :rules="[{ required: true, pattern: /^[a-z0-9][a-z0-9-]{1,62}$/, message: '小写字母、数字和连字符，2–63 位' }]">
        <a-input v-model:value="form.username" size="large" />
      </a-form-item>
      <a-form-item label="密码" name="password" :rules="[{ required: true, min: 8, message: '至少 8 位' }]">
        <a-input-password v-model:value="form.password" size="large" autocomplete="new-password" />
      </a-form-item>
      <a-form-item label="确认密码" name="confirm" :rules="[{ required: true, message: '请再次输入密码' }]">
        <a-input-password v-model:value="form.confirm" size="large" autocomplete="new-password" />
      </a-form-item>
      <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 16px" />
      <a-button type="primary" html-type="submit" size="large" block :loading="loading">创建管理员</a-button>
    </a-form>
    <template v-else>
      <a-alert v-if="totp" type="warning" show-icon style="margin-bottom: 16px">
        <template #message>已启用双因素认证</template>
        <template #description>
          请把下面的密钥导入身份验证器；后续登录必须输入 6 位验证码。<br />
          <span class="code">{{ totp.totp_secret }}</span><br />
          <a :href="totp.totp_uri">打开 otpauth 链接</a>
        </template>
      </a-alert>
      <LicenseImport @imported="done" />
      <a-button type="link" block style="margin-top: 8px" @click="done">稍后导入，先进入控制台</a-button>
    </template>
  </AuthFrame>
</template>
