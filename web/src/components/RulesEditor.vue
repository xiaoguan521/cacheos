<script setup lang="ts">
// 访问规则（P10 规则积木）：条件全部满足时执行动作。原生规则所有版本可用；JWT 需要 lua 授权与节点的 syapp-lua 附加包。
import { computed } from 'vue'
import { useSession } from '../stores/session'
import RuleAction from './RuleAction.vue'

export interface Condition {
  kind: 'ip' | 'ip_set' | 'header' | 'user_agent' | 'path' | 'method'
  negate?: boolean
  cidrs?: string[]
  set?: string
  header?: string
  op?: string
  value?: string
  methods?: string[]
}
export interface Rule {
  name: string
  conditions: Condition[]
  action: any
}

const rules = defineModel<Rule[]>({ required: true })
const props = defineProps<{ upstreams: any[]; ipSets: any[] }>()
const session = useSession()
const luaLicensed = computed(() => !!session.license?.features?.includes('lua'))

const kinds = [
  { value: 'ip', label: 'IP / 网段' },
  { value: 'ip_set', label: 'IP 集 / 地域' },
  { value: 'path', label: '路径' },
  { value: 'header', label: '请求头' },
  { value: 'user_agent', label: 'User-Agent' },
  { value: 'method', label: '请求方法' },
]
const textOps = [
  { value: 'equals', label: '等于' },
  { value: 'prefix', label: '开头是' },
  { value: 'contains', label: '包含' },
  { value: 'regex', label: '正则' },
]
const pathOps = [
  { value: 'prefix', label: '前缀' },
  { value: 'exact', label: '精确' },
  { value: 'regex', label: '正则' },
]
const methods = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'].map((m) => ({ value: m, label: m }))

function newCondition(kind: Condition['kind'] = 'path'): Condition {
  return { ...baseCondition(kind), negate: false }
}
function baseCondition(kind: Condition['kind']): Condition {
  switch (kind) {
    case 'ip':
      return { kind, cidrs: [] }
    case 'ip_set':
      return { kind, set: props.ipSets[0]?.name || '' }
    case 'header':
      return { kind, header: 'X-Api-Key', op: 'exists' }
    case 'user_agent':
      return { kind, op: 'contains', value: '' }
    case 'method':
      return { kind, methods: ['POST'] }
    default:
      return { kind: 'path', op: 'prefix', value: '/' }
  }
}
function setKind(c: Condition, kind: Condition['kind']) {
  const neg = c.negate
  Object.keys(c).forEach((k) => delete (c as any)[k])
  Object.assign(c, newCondition(kind), { negate: !!neg })
}
function newAction(type: string): any {
  switch (type) {
    case 'redirect':
      return { type, status: 302, to: 'https://', preserve_path: false }
    case 'rate_limit':
      return { type, rate: 10, per: 's', burst: 20, key: 'ip' }
    case 'auth_request':
      return { type, auth: { upstream: props.upstreams[0]?.name || '', path: '/auth' } }
    case 'jwt':
      return { type, jwt: { alg: 'HS256', source: 'header', secret: '', leeway: 30 } }
    default:
      return { type: 'deny', status: 403 }
  }
}
function setAction(r: Rule, type: string) {
  r.action = newAction(type)
}
function addRule() {
  let i = rules.value.length + 1
  while (rules.value.some((r) => r.name === `rule-${i}`)) i++
  rules.value.push({ name: `rule-${i}`, conditions: [newCondition('path')], action: newAction('deny') })
}
function move(i: number, d: number) {
  const j = i + d
  if (j < 0 || j >= rules.value.length) return
  const [r] = rules.value.splice(i, 1)
  rules.value.splice(j, 0, r)
}
const hasJWT = computed(() => rules.value.some((r) => r.action.type === 'jwt'))
const valueNeeded = (c: Condition) => c.op !== 'exists'
</script>

<template>
  <div class="rules">
    <div class="rules-head">
      <div>
        <h3>访问规则</h3>
        <span class="sub">条件全部满足时执行动作。执行顺序：拒绝 / 跳转 → 限速 → 外部鉴权 → JWT；ACME 证书验证不受影响。</span>
      </div>
      <a-button size="small" @click="addRule">添加规则</a-button>
    </div>
    <a-alert v-if="hasJWT" type="info" show-icon class="lua-note"
      message="JWT 规则需要节点组内每个节点都安装与其 syapp 版本匹配的 syapp-lua 附加包；未安装时预览与发布会列出缺少附加包的节点。" />
    <div v-for="(rule, i) in rules" :key="i" class="rule">
      <div class="rule-top">
        <a-input v-model:value="rule.name" class="code" style="width: 180px" placeholder="规则名" />
        <span class="sub">满足下面全部条件时</span>
        <span class="grow" />
        <a-button type="text" size="small" :disabled="i === 0" @click="move(i, -1)">上移</a-button>
        <a-button type="text" size="small" :disabled="i === rules.length - 1" @click="move(i, 1)">下移</a-button>
        <a-button type="text" size="small" danger @click="rules.splice(i, 1)">删除</a-button>
      </div>

      <div v-for="(c, j) in rule.conditions" :key="j" class="cond">
        <a-select :value="c.kind" :options="kinds" style="width: 130px" @change="(v: any) => setKind(c, v)" />
        <a-select v-model:value="c.negate" :options="[{ value: false, label: '是' }, { value: true, label: '不是' }]" style="width: 76px" />
        <template v-if="c.kind === 'ip'">
          <a-select v-model:value="c.cidrs" mode="tags" :open="false" class="grow code" placeholder="10.0.0.0/8 或 203.0.113.7，回车添加" />
        </template>
        <template v-else-if="c.kind === 'ip_set'">
          <a-select v-model:value="c.set" class="grow" placeholder="选择 IP 集" :options="ipSets.map((s) => ({ value: s.name, label: `${s.label || s.name}（${s.count} 条）` }))">
            <template #notFoundContent><router-link to="/ip-sets">先创建 IP 集或导入地域库</router-link></template>
          </a-select>
        </template>
        <template v-else-if="c.kind === 'method'">
          <a-select v-model:value="c.methods" mode="multiple" :options="methods" class="grow" />
        </template>
        <template v-else>
          <a-input v-if="c.kind === 'header'" v-model:value="c.header" class="code" style="width: 150px" placeholder="X-Api-Key" />
          <a-select v-model:value="c.op" style="width: 100px"
            :options="c.kind === 'path' ? pathOps : c.kind === 'header' ? [...textOps, { value: 'exists', label: '存在' }] : textOps" />
          <a-input v-if="valueNeeded(c)" v-model:value="c.value" class="grow code" :placeholder="c.op === 'regex' ? '正则表达式' : '匹配值'" />
        </template>
        <a-button type="text" size="small" @click="rule.conditions.splice(j, 1)">✕</a-button>
      </div>
      <a v-if="rule.conditions.length < 8" class="add-cond" @click="rule.conditions.push(newCondition())">+ 条件</a>
      <span v-if="!rule.conditions.length" class="sub">（没有条件：对所有请求生效）</span>

      <RuleAction :action="rule.action" :upstreams="upstreams" :lua-licensed="luaLicensed" @type="(t: string) => setAction(rule, t)" />
    </div>
    <a-empty v-if="!rules.length" description="没有访问规则" :image="false" />
  </div>
</template>

<style scoped>
.rules-head {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin: 8px 0 12px;
}
.rules-head h3 {
  margin: 0 0 2px;
  font-size: 14px;
}
.sub {
  font-size: 12px;
  color: #8a9a9d;
}
.lua-note {
  margin-bottom: 12px;
}
.rule {
  border: 1px solid #e3eaeb;
  border-left: 3px solid #b9534a;
  border-radius: 6px;
  padding: 10px 12px;
  margin-bottom: 10px;
  background: #fcfdfd;
}
.rule-top,
.cond,
.act,
.jwt-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.rule-top {
  margin-bottom: 8px;
}
.cond {
  margin: 6px 0;
}
.grow {
  flex: 1;
  min-width: 140px;
}
.add-cond {
  font-size: 12px;
  margin-right: 8px;
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
