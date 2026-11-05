<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { api, errMsg } from '../api/client'
import { useSession } from '../stores/session'

const router = useRouter()
const route = useRoute()
const session = useSession()
const initialized = ref(true)
const form = reactive({ username: '', password: '', confirm: '' })
const loading = ref(false)
const error = ref('')

onMounted(async () => {
  initialized.value = (await api.get<{ initialized: boolean }>('/setup/status')).initialized
})

async function submit() {
  error.value = ''
  if (!initialized.value && form.password !== form.confirm) {
    error.value = '两次输入的口令不一致'
    return
  }
  loading.value = true
  try {
    await api.post(initialized.value ? '/auth/login' : '/setup', { username: form.username, password: form.password })
    await session.load()
    router.push(String(route.query.next || '/'))
  } catch (e) {
    error.value = errMsg(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="wrap">
    <form class="card" @submit.prevent="submit">
      <h1>EdgeTraffic OS 授权后台</h1>
      <p class="sub">{{ initialized ? '登录' : '首次使用：创建管理员账号' }}</p>
      <a-form layout="vertical" :model="form" name="login">
        <a-form-item label="用户名" name="username"><a-input v-model:value="form.username" autocomplete="username" /></a-form-item>
        <a-form-item label="口令" name="password"><a-input-password v-model:value="form.password" :autocomplete="initialized ? 'current-password' : 'new-password'" /></a-form-item>
        <a-form-item v-if="!initialized" label="再次输入口令（至少 10 个字符）" name="confirm"><a-input-password v-model:value="form.confirm" autocomplete="new-password" /></a-form-item>
      </a-form>
      <a-alert v-if="error" type="error" :message="error" show-icon style="margin-bottom: 14px" />
      <a-button type="primary" html-type="submit" block size="large" :loading="loading">{{ initialized ? '登录' : '创建并登录' }}</a-button>
      <p class="note">本系统只保存签名公钥与业务数据；License 由离线签名机签名。</p>
    </form>
  </div>
</template>

<style scoped>
.wrap {
  min-height: 100vh;
  display: grid;
  place-items: center;
  padding: 16px;
}
.card {
  width: 100%;
  max-width: 380px;
  background: #fff;
  border: 1px solid #e1e7e6;
  border-radius: 10px;
  padding: 28px;
}
h1 {
  font-size: 19px;
  margin: 0;
  color: #16323a;
}
.sub {
  color: #6b7c80;
  margin: 6px 0 18px;
}
.note {
  color: #6b7c80;
  font-size: 12px;
  margin: 14px 0 0;
}
</style>
