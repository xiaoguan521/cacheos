// 控制台前端验收（规格书 12.1 P5）：用真实浏览器（Playwright + Chromium）检查
//   1. 各角色可见的页面与可操作按钮符合权限矩阵，且服务端同样拒绝越权调用（前端隐藏不算约束）；
//   2. 未授权的功能置灰（标准版授权下的四层代理、发布审批；授权过期时只读）；
//   3. 发布进度实时刷新：点击发布后不刷新页面，节点状态自动更新到“已生效”。
// 在 demo 环境（demo/e2e.sh 之后：管理员已初始化、专业版授权有效、节点在线）中运行：
//   BASE=http://127.0.0.1:8443 ADMIN_PW=Demo@2026! LIC_TOOL=… LIC_KEY=… LIC_KID=… node web/e2e/ui-acceptance.cjs
// 依赖 playwright（Apache-2.0，仅测试使用，不进入产品构建）。
const { chromium, request } = require('playwright')
const { execFileSync } = require('child_process')

const BASE = process.env.BASE || 'http://127.0.0.1:8443'
const ADMIN_PW = process.env.ADMIN_PW || 'Demo@2026!'
const { LIC_TOOL, LIC_KEY, LIC_KID } = process.env
let fail = 0
const ok = (m) => console.log(`ok   ${m}`)
const bad = (m) => {
  console.log(`FAIL ${m}`)
  if (process.env.GITHUB_ACTIONS) console.log(`::error title=ui-acceptance::${m}`)
  fail++
}
const check = (cond, m, detail = '') => (cond ? ok(m) : bad(detail ? `${m}（${detail}）` : m))

const ROLES = ['admin', 'operator', 'releaser', 'viewer']
const rank = { viewer: 1, releaser: 2, operator: 3, admin: 4 }
const pw = (role) => (role === 'admin' ? ADMIN_PW : `Ui@2026!${role}-ok`)
const user = (role) => (role === 'admin' ? 'admin' : `ui-${role}`)

// 权限矩阵：页面、元素、最低角色（与 internal/console/api 的路由权限一致）
const MATRIX = [
  { path: '/nodes', kind: 'button', name: '添加节点', min: 'operator' },
  { path: '/nodes', kind: 'button', name: '节点组设置', min: 'operator' },
  { path: '/sites', kind: 'button', name: '新建站点', min: 'operator' },
  { path: '/snippets', kind: 'button', name: '新建片段', min: 'operator' },
  { path: '/certificates', kind: 'button', name: '设置', min: 'admin' },
  { path: '/dataplane', kind: 'heading', name: '版本库', min: 'operator' },
  { path: '/dataplane', kind: 'button', name: '导入升级包', min: 'admin' },
  { path: '/audit', kind: 'button', name: '导出 CSV', min: 'admin' },
  { path: '/system', kind: 'heading', name: '用户', min: 'admin' },
  { path: '/system', kind: 'heading', name: '账号安全策略', min: 'admin' },
]
// 服务端权限：同一组操作，低于最低角色的调用必须返回 403
const API_MATRIX = [
  { method: 'post', path: '/api/v1/sites', data: { group_id: 1, spec: { name: 'x', domains: ['x.test'], routes: [] } }, min: 'operator' },
  { method: 'post', path: '/api/v1/users', data: { username: 'x', password: 'x', role: 'viewer' }, min: 'admin' },
  { method: 'post', path: '/api/v1/node-groups/1/publish', data: {}, min: 'releaser' },
  { method: 'get', path: '/api/v1/audit/export', min: 'admin' },
  { method: 'post', path: '/api/v1/node-groups/1/enroll', data: {}, min: 'operator' },
]

async function apiAs(role, password = pw(role)) {
  const ctx = await request.newContext({ baseURL: BASE, extraHTTPHeaders: { 'X-Requested-With': 'etos' } })
  const r = await ctx.post('/api/v1/auth/login', { data: { username: user(role), password } })
  if (!r.ok()) throw new Error(`login ${role}: ${r.status()} ${await r.text()}`)
  return ctx
}

function issue(inst, extra) {
  return execFileSync(LIC_TOOL, ['issue', '-key', LIC_KEY, '-kid', LIC_KID, '-installation-id', inst, '-binding', 'installation_only',
    '-customer', 'UI 验收', '-nodes', '10', '-sites', '200', ...extra]).toString()
}
const isoDay = (d) => new Date(Date.now() + d * 86400000).toISOString().slice(0, 10)
async function importLicense(admin, raw) {
  const r = await admin.post('/api/v1/license/import', { data: { raw } })
  return (await r.json()).state || ''
}

async function login(browser, role) {
  const ctx = await browser.newContext({ locale: 'zh-CN' })
  const page = await ctx.newPage()
  await page.goto(`${BASE}/login`)
  await page.locator('input[autocomplete="username"]').fill(user(role))
  await page.locator('input[autocomplete="current-password"]').fill(pw(role))
  await btn(page, '登录').click()
  await page.waitForURL(/\/dashboard/, { timeout: 15000 })
  return { ctx, page }
}
// antd 会在两个汉字的按钮文字中间插入空格（“登 录”），按钮名按“字符间可有空白”匹配
const bname = (t) => new RegExp('^\\s*' + [...t].map((c) => c.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('\\s*') + '\\s*$')
const btn = (page, t) => page.getByRole('button', { name: bname(t) })
const el = (page, m) => (m.kind === 'button' ? btn(page, m.name) : page.getByRole('heading', { name: m.name, exact: true }))

async function main() {
  const admin = await apiAs('admin')
  const lic0 = await (await admin.get('/api/v1/license')).json()
  check(lic0.state === 'VALID' && lic0.edition === 'professional', '前提：专业版授权有效', lic0.state)

  // 每个角色一个账号（新账号首次登录须改密码，先经 API 改好）
  for (const role of ROLES.slice(1)) {
    const init = `Ui@2026!${role}-init`
    await admin.post('/api/v1/users', { data: { username: user(role), password: init, role } })
    const c = await apiAs(role, init).catch(() => null)
    if (c) await c.put('/api/v1/auth/password', { data: { old_password: init, new_password: pw(role) } })
  }

  const browser = await chromium.launch()
  try {
    console.log('== 1. 角色权限矩阵（界面）')
    for (const role of ROLES) {
      const { ctx, page } = await login(browser, role)
      for (const m of MATRIX) {
        await page.goto(`${BASE}${m.path}`)
        await page.getByRole('heading', { level: 1 }).first().waitFor({ timeout: 10000 }).catch(() => {})
        await page.waitForLoadState('networkidle')
        const want = rank[role] >= rank[m.min]
        const n = await el(page, m).count()
        check(want ? n > 0 : n === 0, `${role}：${m.path} 的“${m.name}”${want ? '可见' : '不可见'}`, `实际 ${n} 个`)
      }
      await page.goto(`${BASE}/system`)
      await page.waitForLoadState('networkidle')
      check((await page.getByText('Based on NGINX Open Source (BSD-2-Clause)').count()) > 0, `${role}：关于页保留上游声明`)
      await ctx.close()
    }

    console.log('== 1b. 角色权限矩阵（服务端）')
    for (const role of ROLES.slice(1)) {
      const c = await apiAs(role)
      for (const a of API_MATRIX) {
        if (rank[role] >= rank[a.min]) continue
        const r = await c[a.method](a.path, a.data ? { data: a.data } : undefined)
        check(r.status() === 403, `${role}：${a.method.toUpperCase()} ${a.path} 被服务端拒绝`, `HTTP ${r.status()}`)
      }
    }

    console.log('== 2. 未授权功能置灰')
    if (LIC_TOOL) {
      const inst = lic0.installation_id
      check((await importLicense(admin, issue(inst, ['-edition', 'standard', '-days', '365']))) === 'VALID', '导入标准版授权')
      {
        const { ctx, page } = await login(browser, 'operator')
        await page.goto(`${BASE}/streams`)
        await page.waitForLoadState('networkidle')
        const b = btn(page, '新建')
        check((await b.count()) > 0 && (await b.isDisabled()), '标准版：四层代理“新建”置灰')
        check((await page.getByText('当前授权不包含四层代理').count()) > 0, '标准版：四层代理显示未授权说明')
        await ctx.close()
      }
      {
        const { ctx, page } = await login(browser, 'admin')
        await page.goto(`${BASE}/system`)
        await page.waitForLoadState('networkidle')
        const sw = page.locator('section', { has: page.getByRole('heading', { name: '发布审批', exact: true }) }).getByRole('switch')
        check((await sw.count()) > 0 && (await sw.isDisabled()), '标准版：发布审批开关置灰')
        await ctx.close()
      }
      check((await importLicense(admin, issue(inst, ['-edition', 'professional', '-expires', isoDay(-20), '-grace', '15', '-maintenance', isoDay(365)]))) === 'EXPIRED', '导入已过期授权')
      {
        const { ctx, page } = await login(browser, 'operator')
        for (const [path, name] of [['/sites', '新建站点'], ['/nodes', '添加节点']]) {
          await page.goto(`${BASE}${path}`)
          await page.waitForLoadState('networkidle')
          const b = btn(page, name)
          check((await b.count()) > 0 && (await b.isDisabled()), `授权过期：${path} 的“${name}”置灰`)
        }
        await ctx.close()
      }
      check((await importLicense(admin, issue(inst, ['-edition', 'professional', '-days', '365']))) === 'VALID', '恢复专业版授权')
    } else {
      bad('未设置 LIC_TOOL，无法切换授权验证置灰')
    }

    console.log('== 3. 发布进度实时刷新')
    const groups = await (await admin.get('/api/v1/node-groups')).json()
    const g = groups.find((x) => x.id === 1)
    // 改一下上游权重，产生一处待发布变更（不碰站点域名与证书）
    const ups = await (await admin.get('/api/v1/upstreams?group_id=1')).json()
    const u = ups[0]
    u.spec.servers[0].weight = ((u.spec.servers[0].weight || 1) % 9) + 1
    const pr = await admin.put(`/api/v1/upstreams/${u.id}`, { data: { spec: u.spec, version: u.version } })
    check(pr.ok(), '准备待发布变更', `HTTP ${pr.status()} ${(await pr.text()).slice(0, 200)}`)
    {
      const { ctx, page } = await login(browser, 'releaser')
      await page.goto(`${BASE}/releases`)
      await page.waitForLoadState('networkidle')
      await page.evaluate(() => { window.__noReload = true })
      const polls = []
      page.on('response', (r) => { if (/\/api\/v1\/deployments\/\d+$/.test(r.url())) polls.push(r.status()) })
      await page.getByPlaceholder('发布说明，例如：首页改版').fill('UI 验收：实时刷新')
      await btn(page, `发布到 ${g.name}`).click()
      const panel = page.locator('section.panel').filter({ has: page.locator('.cur', { hasText: 'UI 验收：实时刷新' }) })
      const done = await panel.getByText('已生效').first().waitFor({ timeout: 60000 }).then(() => true).catch(() => false)
      check(done, '发布后不刷新页面，状态自动变为“已生效”')
      // 没有在线节点时发布立即完成，不需要轮询；demo 环境有在线节点，必须看到轮询
      if (g.online > 0) check(polls.length > 0, '页面自动轮询发布进度', `${polls.length} 次`)
      else console.log('     （没有在线节点，跳过轮询检查）')
      check(await page.evaluate(() => window.__noReload === true), '整个过程没有刷新页面')
      const lanes = await panel.getByText('已生效').count()
      check(lanes >= 1 + g.online, `每个在线节点的结果都显示为“已生效”（${g.online} 个节点）`, `显示 ${lanes} 处`)
      await ctx.close()
    }
  } finally {
    await browser.close()
  }
  console.log(`---- ${fail} failed`)
  process.exit(fail ? 1 : 0)
}

main().catch((e) => {
  bad(`异常：${e.message}`)
  process.exit(1)
})
