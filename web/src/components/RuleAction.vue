<script setup lang="ts">
// 规则动作编辑（RulesEditor 的一部分）：action 对象由父组件持有，这里原地修改字段。
import { computed } from 'vue'

const props = defineProps<{ action: any; upstreams: any[]; luaLicensed: boolean }>()
const emit = defineEmits<{ type: [string] }>()

const actions = computed(() => [
  { value: 'deny', label: '拒绝' },
  { value: 'redirect', label: '跳转' },
  { value: 'rate_limit', label: '限速' },
  { value: 'auth_request', label: '外部鉴权' },
  { value: 'jwt', label: props.luaLicensed ? 'JWT 校验（Lua）' : 'JWT 校验（需要 lua 授权）', disabled: !props.luaLicensed },
])
const denyCodes = [403, 401, 404, 429, 444, 451, 503].map((s) => ({ value: s, label: s === 444 ? '444（直接断开）' : String(s) }))
const redirectCodes = [301, 302, 307, 308].map((s) => ({ value: s, label: String(s) }))
</script>

<template>
  <div class="act">
    <span class="lbl">则</span>
    <a-select :value="action.type" :options="actions" style="width: 170px" @change="(v: any) => emit('type', v)" />
    <template v-if="action.type === 'deny'">
      <a-select v-model:value="action.status" style="width: 200px" :options="denyCodes" />
    </template>
    <template v-if="action.type === 'redirect'">
      <a-select v-model:value="action.status" style="width: 90px" :options="redirectCodes" />
      <a-input v-model:value="action.to" class="grow code" placeholder="https://new.example.com/" />
      <a-checkbox v-model:checked="action.preserve_path">保留路径</a-checkbox>
    </template>
    <template v-if="action.type === 'rate_limit'">
      <a-input-number v-model:value="action.rate" :min="1" :max="100000" style="width: 100px" />
      <a-select v-model:value="action.per" style="width: 90px" :options="[{ value: 's', label: '次/秒' }, { value: 'm', label: '次/分' }]" />
      <span class="sub">突发</span>
      <a-input-number v-model:value="action.burst" :min="0" :max="100000" style="width: 90px" />
      <a-select v-model:value="action.key" style="width: 110px" :options="[{ value: 'ip', label: '按 IP' }, { value: 'header', label: '按请求头' }]" />
      <a-input v-if="action.key === 'header'" v-model:value="action.key_header" class="code" style="width: 130px" placeholder="X-Tenant" />
    </template>
    <template v-if="action.type === 'auth_request'">
      <a-select v-model:value="action.auth.upstream" style="width: 160px" placeholder="鉴权服务" :options="upstreams.map((u) => ({ value: u.name, label: u.name }))" />
      <a-input v-model:value="action.auth.path" class="grow code" placeholder="/auth/verify" />
      <span class="sub">2xx 放行，401 / 403 原样返回</span>
    </template>
    <template v-if="action.type === 'jwt'">
      <a-select v-model:value="action.jwt.alg" style="width: 100px" :options="['HS256', 'RS256'].map((a) => ({ value: a, label: a }))" />
      <a-select v-model:value="action.jwt.source" style="width: 170px"
        :options="[{ value: 'header', label: 'Authorization: Bearer' }, { value: 'cookie', label: 'Cookie' }]" />
      <a-input v-if="action.jwt.source === 'cookie'" v-model:value="action.jwt.cookie" class="code" style="width: 130px" placeholder="Cookie 名" />
    </template>
  </div>
  <div v-if="action.type === 'jwt'" class="jwt">
    <a-input-password v-if="action.jwt.alg === 'HS256'" v-model:value="action.jwt.secret" autocomplete="new-password"
      :placeholder="action.jwt.has_secret ? '已设置（留空不修改）' : 'HS256 密钥，至少 32 个字符'" />
    <a-textarea v-else v-model:value="action.jwt.public_key" :rows="3" class="code" placeholder="-----BEGIN PUBLIC KEY-----（RSA ≥ 2048 位）" />
    <div class="jwt-row">
      <a-input v-model:value="action.jwt.issuer" placeholder="要求的 iss（可选）" />
      <a-input v-model:value="action.jwt.audience" placeholder="要求的 aud（可选）" />
      <a-input-number v-model:value="action.jwt.leeway" :min="0" :max="300" addon-after="秒偏差" style="width: 160px" />
      <a-input v-model:value="action.jwt.subject_header" class="code" placeholder="转发 sub 的请求头（可选）" />
    </div>
  </div>
</template>

<style scoped>
.act,
.jwt-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.act {
  margin-top: 10px;
  padding-top: 10px;
  border-top: 1px dashed #e3eaeb;
}
.lbl {
  font-weight: 600;
  color: #16323a;
}
.grow {
  flex: 1;
  min-width: 140px;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
.jwt {
  margin-top: 8px;
  display: grid;
  gap: 8px;
}
.jwt-row > * {
  flex: 1;
  min-width: 140px;
}
</style>
