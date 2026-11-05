<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import dayjs from 'dayjs'
import { message } from 'antdv-next'
import { api, errMsg } from '../api/client'
import { editionName, licenseStateName, useSession } from '../stores/session'
import LicenseImport from '../components/LicenseImport.vue'

const session = useSession()
const lic = computed(() => session.license)
const users = ref<any[]>([])
const version = ref<any>({})

const approval = ref<{ enabled: boolean; licensed: boolean }>({ enabled: false, licensed: false })
onMounted(async () => {
  await session.loadLicense()
  approval.value = await api.get('/release-approval')
  if (session.can('admin')) users.value = await api.get('/users')
})
const tokens = ref<any[]>([])
const tkOpen = ref(false)
const tkForm = reactive({ name: '', role: 'operator', expire_days: 90 })
const newToken = ref('')
async function loadTokens() {
  tokens.value = await api.get('/api-tokens').catch(() => [])
}
onMounted(loadTokens)
async function createToken() {
  try {
    const r = await api.post('/api-tokens', tkForm)
    newToken.value = r.token
    tkOpen.value = false
    await loadTokens()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function revokeToken(id: number) {
  try {
    await api.del(`/api-tokens/${id}`)
    await loadTokens()
  } catch (e) {
    message.error(errMsg(e))
  }
}
const apiLicensed = computed(() => !!lic.value?.features?.includes('api_token'))

async function setApproval(on: boolean) {
  try {
    approval.value = await api.put('/release-approval', { enabled: on })
    message.success(on ? '已开启发布审批' : '已关闭发布审批')
  } catch (e) {
    message.error(errMsg(e))
  }
}

const featureName: Record<string, string> = {
  core: '站点、证书、发布、回滚、监控',
  api_token: '开放 API',
  batch_release: '批量发布',
  release_approval: '发布审批',
  sso: 'LDAP / OIDC 单点登录',
  audit_export: '审计导出',
  alert_im: '企业微信 / 钉钉告警',
  stream: '四层代理',
  ha: '高可用',
  canary: '灰度发布',
  gm_tls: '国密 TLS',
}
const allFeatures = Object.keys(featureName)
const tone = computed(() => (lic.value?.writable ? (lic.value.state === 'VALID' ? 'ok' : 'warn') : 'bad'))

const uOpen = ref(false)
const uForm = reactive({ username: '', password: '', role: 'operator' })
async function addUser() {
  try {
    await api.post('/users', uForm)
    message.success('已创建')
    uOpen.value = false
    users.value = await api.get('/users')
  } catch (e) {
    message.error(errMsg(e))
  }
}
const pol = ref<any>(null)
const ldap = ref<any>(null)
const ldapTestOpen = ref(false)
const ldapTest = reactive({ username: '', password: '' })
onMounted(async () => {
  if (!session.can('admin')) return
  pol.value = await api.get('/security-policy')
  ldap.value = await api.get('/sso/ldap')
  ldap.value.config.role_map ||= []
})
async function reloadUsers() {
  users.value = await api.get('/users')
}
async function updUser(u: any, body: any) {
  try {
    await api.put(`/users/${u.id}`, body)
    await reloadUsers()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function resetPw(u: any) {
  const pw = window.prompt(`为 ${u.username} 设置临时口令（对方首次登录须修改）`)
  if (pw) await updUser(u, { reset_password: pw })
}
async function delUser(u: any) {
  try {
    await api.del(`/users/${u.id}`)
    await reloadUsers()
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function savePolicy() {
  try {
    pol.value = await api.put('/security-policy', pol.value)
    message.success('已保存')
  } catch (e) {
    message.error(errMsg(e))
  }
}
const oidcCfg = ref<any>(null)
const origin = window.location.origin
onMounted(async () => {
  if (!session.can('admin')) return
  oidcCfg.value = await api.get('/sso/oidc')
  oidcCfg.value.config.role_map ||= []
})
async function saveOIDC() {
  try {
    oidcCfg.value = await api.put('/sso/oidc', oidcCfg.value.config)
    oidcCfg.value.config.role_map ||= []
    message.success('已保存')
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function saveLDAP() {
  try {
    ldap.value = await api.put('/sso/ldap', ldap.value.config)
    ldap.value.config.role_map ||= []
    message.success('已保存')
  } catch (e) {
    message.error(errMsg(e))
  }
}
async function testLDAP() {
  try {
    const r = await api.post('/sso/ldap/test', ldapTest)
    message.success(`认证成功：${r.dn}，角色 ${roleName[r.role] || r.role}`)
    ldapTestOpen.value = false
  } catch (e) {
    message.error(errMsg(e))
  }
}
const roleName: Record<string, string> = { admin: '管理员', operator: '运维', releaser: '发布员', viewer: '只读' }
</script>

<template>
  <div class="page">
    <div class="page-head">
      <div>
        <h1>授权与系统</h1>
        <p>授权过期只限制控制台的新增变更，从不停止或干扰节点上的 syapp 流量。</p>
      </div>
    </div>

    <div class="cols">
      <section class="panel lic" :class="tone" v-if="lic">
        <div class="state">
          <span class="badge">{{ licenseStateName[lic.state] }}</span>
          <span v-if="lic.edition" class="ed">{{ editionName[lic.edition] }}</span>
        </div>
        <h2 v-if="lic.customer">{{ lic.customer }}</h2>
        <p v-if="lic.reason" class="reason">{{ lic.reason }}</p>
        <dl v-if="lic.license_id">
          <dt>授权编号</dt>
          <dd class="code">{{ lic.license_id }}</dd>
          <dt>到期日</dt>
          <dd>{{ dayjs(lic.expires_at).format('YYYY-MM-DD') }}<span class="sub">（{{ lic.days_left >= 0 ? `剩余 ${lic.days_left} 天` : `已过期 ${-lic.days_left} 天` }}）</span></dd>
          <dt>维保到期</dt>
          <dd>{{ dayjs(lic.maintenance_until).format('YYYY-MM-DD') }}</dd>
          <dt>节点</dt>
          <dd>
            <a-progress :percent="lic.nodes_limit ? Math.round((lic.nodes_used / lic.nodes_limit) * 100) : 0" :show-info="false" size="small" />
            <span class="num">{{ lic.nodes_used }} / {{ lic.nodes_limit || '不限' }}</span>
          </dd>
          <dt>站点</dt>
          <dd>
            <a-progress :percent="lic.sites_limit ? Math.round((lic.sites_used / lic.sites_limit) * 100) : 0" :show-info="false" size="small" />
            <span class="num">{{ lic.sites_used }} / {{ lic.sites_limit || '不限' }}</span>
          </dd>
          <dt>安装 ID</dt>
          <dd class="code">{{ lic.installation_id }}</dd>
          <dt>吊销列表</dt>
          <dd v-if="lic.crl">序号 {{ lic.crl.serial }}<span class="sub">（{{ dayjs(lic.crl.issued_at).format('YYYY-MM-DD') }}，{{ lic.crl.revoked }} 项）</span></dd>
          <dd v-else class="sub">无</dd>
        </dl>
        <template v-else>
          <dl>
            <dt>安装 ID</dt>
            <dd class="code">{{ lic.installation_id }}</dd>
          </dl>
        </template>
        <h3>功能</h3>
        <ul class="feats">
          <li v-for="f in allFeatures" :key="f" :class="{ on: lic.features?.includes(f) }">{{ featureName[f] }}</li>
        </ul>
      </section>

      <section class="panel" v-if="session.can('admin')">
        <h2>{{ lic?.license_id ? '更新授权' : '导入授权' }}</h2>
        <LicenseImport />
      </section>
    </div>

    <section class="panel">
      <div class="uhead">
        <h2>开放 API 令牌</h2>
        <a-button size="small" :disabled="!apiLicensed || !session.writable" @click="tkOpen = true">新建令牌</a-button>
      </div>
      <p class="muted">
        用 <span class="code">Authorization: Bearer &lt;令牌&gt;</span> 调用 <span class="code">/api/v1</span>，接口清单见 <a href="/api/v1/openapi.json" target="_blank">openapi.json</a>。令牌权限不高于创建者，创建者被降级时随之降级。
        <template v-if="!apiLicensed">当前授权不包含“开放 API”。</template>
      </p>
      <a-alert v-if="newToken" type="success" show-icon closable style="margin: 8px 0" @close="newToken = ''">
        <template #message>令牌只显示这一次，请立即保存：<span class="code">{{ newToken }}</span></template>
      </a-alert>
      <a-table v-if="tokens.length" :data-source="tokens" row-key="id" :pagination="false" size="small"
        :columns="[{ title: '名称', dataIndex: 'name' }, { title: '前缀', key: 'prefix' }, { title: '用户 / 角色', key: 'who' }, { title: '到期', key: 'exp' }, { title: '最近使用', key: 'used' }, { title: '', key: 'op', width: 80 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'prefix'"><span class="code">{{ record.prefix }}…</span></template>
          <template v-else-if="column.key === 'who'">{{ record.username }} · {{ roleName[record.role] }}</template>
          <template v-else-if="column.key === 'exp'">{{ record.expires_at ? dayjs(record.expires_at).format('YYYY-MM-DD') : '不过期' }}</template>
          <template v-else-if="column.key === 'used'">{{ record.last_used_at ? dayjs(record.last_used_at).format('MM-DD HH:mm') + ' ' + record.last_used_ip : '从未' }}</template>
          <template v-else-if="column.key === 'op'">
            <a-tag v-if="record.revoked">已撤销</a-tag>
            <a-popconfirm v-else title="撤销后立即失效，确定？" @confirm="revokeToken(record.id)"><a class="danger">撤销</a></a-popconfirm>
          </template>
        </template>
      </a-table>
      <a-modal v-model:open="tkOpen" title="新建 API 令牌" @ok="createToken">
        <a-form layout="vertical">
          <a-form-item label="名称"><a-input v-model:value="tkForm.name" placeholder="例如 ci-pipeline" /></a-form-item>
          <a-form-item label="角色（不高于自己）">
            <a-select v-model:value="tkForm.role" :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" />
          </a-form-item>
          <a-form-item label="有效天数（0 表示不过期）"><a-input-number v-model:value="tkForm.expire_days" :min="0" :max="3650" /></a-form-item>
        </a-form>
      </a-modal>
    </section>

    <section class="panel" v-if="session.can('admin')">
      <div class="uhead">
        <h2>发布审批</h2>
        <a-switch :checked="approval.enabled" :disabled="!approval.enabled && (!approval.licensed || !session.writable)" @change="(v: any) => setApproval(!!v)" />
      </div>
      <p class="muted">
        开启后，每次发布都要由另一位发布员看过差异并批准才会下发（提交人不能批准自己的申请）。回滚、漂移恢复与证书续期不经审批，保证应急与授权过期时可用。
        <template v-if="!approval.licensed">当前授权不包含“发布审批”（专业版及以上）。</template>
      </p>
    </section>

    <section class="panel" v-if="session.can('admin')">
      <div class="uhead">
        <h2>用户</h2>
        <a-button size="small" :disabled="!session.writable" @click="uOpen = true">新建用户</a-button>
      </div>
      <a-table :data-source="users" row-key="id" :pagination="false" size="middle" :columns="[{ title: '用户名', key: 'name' }, { title: '角色', key: 'role', width: 130 }, { title: '状态', key: 'st' }, { title: '最近登录', key: 'login' }, { title: '', key: 'op', width: 200 }]">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'name'">{{ record.username }}<a-tag v-if="record.source && record.source !== 'local'" style="margin-left: 6px">{{ record.source.toUpperCase() }}</a-tag></template>
          <template v-else-if="column.key === 'role'">
            <a-select size="small" :value="record.role" style="width: 110px" :disabled="record.username === session.username || !session.writable"
              :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" @change="(v: any) => updUser(record, { role: v })" />
          </template>
          <template v-else-if="column.key === 'st'">
            <a-tag v-if="record.disabled" color="red">已停用</a-tag>
            <a-tag v-else-if="record.locked_until && dayjs(record.locked_until).isAfter(dayjs())" color="orange">已锁定</a-tag>
            <a-tag v-else-if="record.must_change_password" color="gold">待改口令</a-tag>
            <a-tag v-else color="green">正常</a-tag>
          </template>
          <template v-else-if="column.key === 'login'">{{ record.last_login_at ? dayjs(record.last_login_at).format('YYYY-MM-DD HH:mm') : '从未' }}</template>
          <template v-else-if="column.key === 'op' && record.username !== session.username">
            <a-space :size="4">
              <a v-if="record.locked_until && dayjs(record.locked_until).isAfter(dayjs())" @click="updUser(record, { unlock: true })">解锁</a>
              <a @click="updUser(record, { disabled: !record.disabled })">{{ record.disabled ? '启用' : '停用' }}</a>
              <a v-if="!record.source || record.source === 'local'" @click="resetPw(record)">重置口令</a>
              <a-popconfirm :title="`删除用户 ${record.username}？其 API 令牌一并撤销`" @confirm="delUser(record)"><a class="danger">删除</a></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </section>

    <section class="panel" v-if="session.can('admin') && pol">
      <div class="uhead">
        <h2>账号安全策略</h2>
        <a-button size="small" :disabled="!session.writable" @click="savePolicy">保存</a-button>
      </div>
      <p class="muted">按等保 2.0 三级要求的默认值：口令复杂度与有效期、历史口令、连续失败锁定、会话空闲超时与最长有效期。新建或重置口令的账号首次登录必须修改口令。</p>
      <div class="pol">
        <label>口令最短长度<a-input-number v-model:value="pol.min_length" :min="8" :max="64" /></label>
        <label>字符类别（至少）<a-input-number v-model:value="pol.min_classes" :min="1" :max="4" /></label>
        <label>口令有效期（天，0 不过期）<a-input-number v-model:value="pol.expire_days" :min="0" :max="3650" /></label>
        <label>不得重复最近口令数<a-input-number v-model:value="pol.history" :min="0" :max="24" /></label>
        <label>连续失败锁定（次）<a-input-number v-model:value="pol.lock_threshold" :min="3" :max="20" /></label>
        <label>锁定时长（分钟）<a-input-number v-model:value="pol.lock_minutes" :min="1" :max="1440" /></label>
        <label>会话空闲超时（分钟）<a-input-number v-model:value="pol.idle_minutes" :min="5" :max="480" /></label>
        <label>会话最长有效期（小时）<a-input-number v-model:value="pol.max_hours" :min="1" :max="24" /></label>
      </div>
    </section>

    <section class="panel" v-if="session.can('admin') && oidcCfg">
      <div class="uhead">
        <h2>OIDC 单点登录</h2>
        <a-button size="small" type="primary" :disabled="!session.writable" @click="saveOIDC">保存</a-button>
      </div>
      <p class="muted">授权码 + PKCE。登录页出现“使用单点登录”按钮；首次登录自动创建账号，角色按声明映射并每次刷新。<template v-if="!oidcCfg.licensed">当前授权不包含单点登录（专业版及以上）。</template></p>
      <div class="pol">
        <label class="wide">启用<a-switch v-model:checked="oidcCfg.config.enabled" :disabled="!oidcCfg.licensed" /></label>
        <label class="wide">Issuer<a-input v-model:value="oidcCfg.config.issuer" placeholder="https://sso.example.com/realms/corp" /></label>
        <label>Client ID<a-input v-model:value="oidcCfg.config.client_id" /></label>
        <label>Client Secret<a-input-password v-model:value="oidcCfg.config.client_secret" :placeholder="oidcCfg.config.has_client_secret ? '已保存，留空不改' : ''" autocomplete="new-password" /></label>
        <label class="wide">回调地址（在身份提供方登记）<a-input v-model:value="oidcCfg.config.redirect_url" :placeholder="`${origin}/api/v1/auth/oidc/callback`" /></label>
        <label>用户名声明<a-input v-model:value="oidcCfg.config.username_claim" /></label>
        <label>角色声明<a-input v-model:value="oidcCfg.config.role_claim" /></label>
        <label>默认角色<a-select v-model:value="oidcCfg.config.default_role" :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" /></label>
      </div>
      <div class="sub" style="margin: 8px 0 4px">角色映射（声明中的值 → 角色，取最高）</div>
      <div v-for="(m, i) in oidcCfg.config.role_map" :key="i" class="rm">
        <a-input v-model:value="m.group" placeholder="edge-admins" />
        <a-select v-model:value="m.role" style="width: 120px" :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" />
        <a class="danger" @click="oidcCfg.config.role_map.splice(i, 1)">移除</a>
      </div>
      <a @click="(oidcCfg.config.role_map ||= []).push({ group: '', role: 'viewer' })">添加映射</a>
    </section>

    <section class="panel" v-if="session.can('admin') && ldap">
      <div class="uhead">
        <h2>LDAP / AD 登录</h2>
        <a-space>
          <a-button size="small" @click="ldapTestOpen = true">测试</a-button>
          <a-button size="small" type="primary" :disabled="!session.writable" @click="saveLDAP">保存</a-button>
        </a-space>
      </div>
      <p class="muted">本地没有的用户名会交给 LDAP 认证，首次登录自动创建账号，角色按组映射（每次登录刷新）。只支持 ldaps:// 或 ldap:// + StartTLS。<template v-if="!ldap.licensed">当前授权不包含单点登录（专业版及以上）。</template></p>
      <div class="pol">
        <label class="wide">启用<a-switch v-model:checked="ldap.config.enabled" :disabled="!ldap.licensed" /></label>
        <label class="wide">地址<a-input v-model:value="ldap.config.url" placeholder="ldaps://ad.example.com:636" /></label>
        <label class="wide">服务账号 DN<a-input v-model:value="ldap.config.bind_dn" placeholder="cn=reader,dc=example,dc=com" /></label>
        <label class="wide">服务账号口令<a-input-password v-model:value="ldap.config.bind_password" :placeholder="ldap.config.has_bind_password ? '已保存，留空不改' : ''" autocomplete="new-password" /></label>
        <label class="wide">Base DN<a-input v-model:value="ldap.config.base_dn" placeholder="dc=example,dc=com" /></label>
        <label class="wide">用户过滤器<a-input v-model:value="ldap.config.user_filter" placeholder="(&(objectClass=person)(sAMAccountName=%s))" /></label>
        <label>默认角色<a-select v-model:value="ldap.config.default_role" :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" /></label>
        <label><span>内网自签证书</span><a-checkbox v-model:checked="ldap.config.skip_tls_verify">不校验服务器证书</a-checkbox></label>
      </div>
      <div class="sub" style="margin: 8px 0 4px">组映射（memberOf 中的组 DN → 角色，取最高）</div>
      <div v-for="(m, i) in ldap.config.role_map" :key="i" class="rm">
        <a-input v-model:value="m.group" placeholder="cn=edge-admins,ou=groups,dc=example,dc=com" />
        <a-select v-model:value="m.role" style="width: 120px" :options="Object.entries(roleName).map(([value, label]) => ({ value, label }))" />
        <a class="danger" @click="ldap.config.role_map.splice(i, 1)">移除</a>
      </div>
      <a @click="(ldap.config.role_map ||= []).push({ group: '', role: 'viewer' })">添加映射</a>
      <a-modal v-model:open="ldapTestOpen" title="测试 LDAP 登录" @ok="testLDAP">
        <a-form layout="vertical">
          <a-form-item label="用户名"><a-input v-model:value="ldapTest.username" /></a-form-item>
          <a-form-item label="口令"><a-input-password v-model:value="ldapTest.password" /></a-form-item>
        </a-form>
      </a-modal>
    </section>

    <section class="panel about">
      <h2>关于</h2>
      <p>EdgeTraffic OS，数据面 syapp。</p>
      <p class="sub">The syapp data plane is based on NGINX Open Source (BSD-2-Clause). 完整的第三方许可声明见安装目录 /opt/syapp/share/doc/NOTICE。</p>
    </section>

    <a-modal v-model:open="uOpen" title="新建用户" ok-text="创建" @ok="addUser">
      <a-form layout="vertical">
        <a-form-item label="用户名"><a-input v-model:value="uForm.username" /></a-form-item>
        <a-form-item label="初始密码"><a-input-password v-model:value="uForm.password" /></a-form-item>
        <a-form-item label="角色">
          <a-radio-group v-model:value="uForm.role" :options="[{ label: '运维', value: 'operator' }, { label: '发布员', value: 'releaser' }, { label: '只读', value: 'viewer' }, { label: '管理员', value: 'admin' }]" />
        </a-form-item>
      </a-form>
    </a-modal>
  </div>
</template>

<style scoped>
.cols {
  display: grid;
  grid-template-columns: 1.2fr 1fr;
  gap: 16px;
  margin-bottom: 16px;
  align-items: start;
}
.cols .panel + .panel {
  margin-top: 0;
}
.lic {
  border-top: 4px solid #2f9e5b;
}
.lic.warn {
  border-top-color: #d9a21b;
}
.lic.bad {
  border-top-color: #c8423b;
}
.state {
  display: flex;
  gap: 10px;
  align-items: center;
  margin-bottom: 8px;
}
.badge {
  font-size: 13px;
  font-weight: 600;
  padding: 2px 10px;
  border-radius: 10px;
  background: #e6f3ec;
  color: #1f6f43;
}
.warn .badge {
  background: #fbf1d8;
  color: #8a6510;
}
.bad .badge {
  background: #f8e3e1;
  color: #a3332d;
}
.ed {
  color: #6b7c80;
}
.lic h2 {
  font-size: 18px;
}
.reason {
  color: #a3332d;
  margin: -6px 0 12px;
}
dl {
  display: grid;
  grid-template-columns: 88px 1fr;
  row-gap: 10px;
  margin: 0 0 16px;
  font-size: 13px;
}
dt {
  color: #6b7c80;
}
dd {
  margin: 0;
  color: #1f2a2e;
  display: flex;
  align-items: center;
  gap: 10px;
}
dd :deep(.ant-progress) {
  width: 160px;
  margin: 0;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
h3 {
  font-size: 13px;
  color: #16323a;
  margin: 0 0 8px;
}
.feats {
  list-style: none;
  padding: 0;
  margin: 0;
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 6px 16px;
  font-size: 13px;
}
.feats li {
  color: #a7b3b5;
  padding-left: 18px;
  position: relative;
}
.feats li::before {
  content: '';
  position: absolute;
  left: 0;
  top: 7px;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  background: #d6dedd;
}
.feats li.on {
  color: #1f2a2e;
}
.feats li.on::before {
  background: #0e7c86;
}
.uhead {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.about p {
  margin: 0 0 4px;
}
@media (max-width: 980px) {
  .cols {
    grid-template-columns: 1fr;
  }
}
.muted {
  margin: 8px 0 0;
  font-size: 13px;
  color: #6b7c80;
}
.danger {
  color: #a3332d;
}
.pol {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 12px;
}
.pol label {
  display: flex;
  flex-direction: column;
  gap: 4px;
  font-size: 13px;
  color: #445a5f;
}
.pol label.wide {
  grid-column: span 2;
}
.rm {
  display: flex;
  gap: 8px;
  align-items: center;
  margin-bottom: 6px;
}
.sub {
  font-size: 12px;
  color: #6b7c80;
}
</style>
