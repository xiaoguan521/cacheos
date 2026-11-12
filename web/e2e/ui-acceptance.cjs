// CacheOS 控制台端到端冒烟验收。
// 支持空数据目录自动初始化；已有数据目录需要提供 ADMIN_OTP：
//   BASE=https://127.0.0.1:8443 ADMIN_USER=admin ADMIN_PW='...' [ADMIN_OTP=123456] node web/e2e/ui-acceptance.cjs
// 依赖 playwright（仅测试使用，不进入产品构建）。
const crypto = require('node:crypto')
const fs = require('node:fs')
const os = require('node:os')
const path = require('node:path')
const { chromium, request } = require('playwright')

const BASE = process.env.BASE || 'http://127.0.0.1:8443'
const ADMIN_USER = process.env.ADMIN_USER || 'admin'
const ADMIN_PW = process.env.ADMIN_PW || ''
const ADMIN_OTP = process.env.ADMIN_OTP || ''
let failed = 0
const ok = (msg) => console.log(`ok   ${msg}`)
const bad = (msg) => { console.error(`FAIL ${msg}`); failed++ }
const check = (condition, msg, detail = '') => (condition ? ok(msg) : bad(detail ? `${msg}: ${detail}` : msg))

function totpCode(secret, now = Date.now()) {
  const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
  let buffer = 0n
  let bits = 0
  const bytes = []
  for (const char of secret.replace(/=+$/, '').toUpperCase()) {
    const value = alphabet.indexOf(char)
    if (value < 0) throw new Error(`无效的 TOTP 密钥字符: ${char}`)
    buffer = (buffer << 5n) | BigInt(value)
    bits += 5
    if (bits >= 8) {
      bits -= 8
      bytes.push(Number((buffer >> BigInt(bits)) & 0xffn))
      buffer &= (1n << BigInt(bits)) - 1n
    }
  }
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(now / 30000)))
  const digest = crypto.createHmac('sha1', Buffer.from(bytes)).update(counter).digest()
  const offset = digest[digest.length - 1] & 0x0f
  const value = ((digest[offset] & 0x7f) << 24) |
    ((digest[offset + 1] & 0xff) << 16) |
    ((digest[offset + 2] & 0xff) << 8) |
    (digest[offset + 3] & 0xff)
  return String(value % 1000000).padStart(6, '0')
}

async function loginAPI() {
  const ctx = await request.newContext({ baseURL: BASE, extraHTTPHeaders: { 'X-Requested-With': 'cacheos' } })
  const status = await ctx.get('/api/v1/setup/status')
  if (!status.ok()) throw new Error(`读取初始化状态失败：HTTP ${status.status()} ${await status.text()}`)
  const state = await status.json()
  if (!state.initialized) {
    const response = await ctx.post('/api/v1/setup', { data: { username: ADMIN_USER, password: ADMIN_PW } })
    if (!response.ok()) throw new Error(`初始化失败：HTTP ${response.status()} ${await response.text()}`)
    const body = await response.json()
    ok('空数据目录自动创建管理员')
    return { ctx, totpSecret: body.totp_secret }
  }
  const response = await ctx.post('/api/v1/auth/login', { data: { username: ADMIN_USER, password: ADMIN_PW, otp: ADMIN_OTP } })
  if (!response.ok()) throw new Error(`登录失败：HTTP ${response.status()} ${await response.text()}`)
  return { ctx, totpSecret: '' }
}

// ---- 新界面检查：验收环境没有真实主机、参数历史、升级包与槽位数据，这里用 page.route 拦截
// 相关 /api/v1 请求返回构造的响应来驱动界面。每段检查结束后取消拦截，已有检查不受影响。
const SHOT_DIR = process.env.SHOT_DIR || path.join(process.env.RUNNER_TEMP || os.tmpdir(), 'cacheos-ui')
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
// 本轮验收中由拦截主动返回的 HTTP 错误码（用于忽略浏览器对这些响应打印的 Failed to load resource）
const EXPECTED_HTTP_ERRORS = new Set()
const isExpectedConsoleError = (m) => {
  const status = Number((m.text().match(/status of (\d{3})/) || [])[1])
  return m.text().startsWith('Failed to load resource') && EXPECTED_HTTP_ERRORS.has(status) && (m.location().url || '').includes('/api/v1/')
}

async function shot(page, name) {
  fs.mkdirSync(SHOT_DIR, { recursive: true })
  const file = path.join(SHOT_DIR, `${name}.png`)
  await sleep(600)
  await page.screenshot({ path: file })
  ok(`截图已保存 ${file}`)
}

// handlers: [{ method, path: RegExp（匹配去掉 /api/v1 前缀后的路径）, respond(record) => { status, body } }]
// 未匹配的请求放行到真实控制台。匹配到的请求记录到返回的列表中。
async function mockAPI(page, handlers) {
  const seen = []
  await page.route('**/api/v1/**', async (route) => {
    const req = route.request()
    const url = new URL(req.url())
    const apiPath = url.pathname.slice('/api/v1'.length)
    let body
    try { body = req.postDataJSON() } catch { body = undefined }
    const record = { method: req.method(), path: apiPath, query: Object.fromEntries(url.searchParams), body }
    const handler = handlers.find((h) => h.method === req.method() && h.path.test(apiPath))
    if (!handler) return route.fallback()
    seen.push(record)
    const res = await handler.respond(record)
    if ((res.status ?? 200) >= 400) EXPECTED_HTTP_ERRORS.add(res.status)
    await route.fulfill({ status: res.status ?? 200, contentType: 'application/json', body: JSON.stringify(res.body ?? {}) })
  })
  return seen
}

const unmockAPI = (page) => page.unroute('**/api/v1/**')
const callsTo = (seen, method, re) => seen.filter((r) => r.method === method && re.test(r.path))
const license = (features = ['online_reshard']) => ({ state: 'VALID', reason: '', writable: true, edition: 'enterprise', features, license_id: 'lic_ui', customer: 'UI 验收', hosts_limit: 20, instances_limit: 200, memory_gb_limit: 1024, days_left: 300, installation_id: 'inst_ui', hosts_used: 3, instances_used: 6, memory_mb_used: 2048, crl: null })
const me = (cliExec) => ({ body: { username: 'admin', role: 'admin', cli_exec: cliExec } })

function clusterUnit(mode = 'cluster') {
  const instances = mode === 'cluster'
    ? [
      { id: 11, kind: 'server', host: 'node-a', address: '10.0.0.11', host_id: 1, port: 7001, role: 'master', desired_role: 'master', state: 'running', shard: 's1', cluster_slots: '0-5460' },
      { id: 12, kind: 'server', host: 'node-b', address: '10.0.0.12', host_id: 2, port: 7002, role: 'master', desired_role: 'master', state: 'running', shard: 's2', cluster_slots: '5461-10922' },
      { id: 13, kind: 'server', host: 'node-c', address: '10.0.0.13', host_id: 3, port: 7003, role: 'master', desired_role: 'master', state: 'running', shard: 's3', cluster_slots: '10923-16383' },
    ]
    : [{ id: 11, kind: 'server', host: 'node-a', address: '10.0.0.11', host_id: 1, port: 7001, role: 'master', desired_role: 'master', state: 'running' }]
  return { id: 1, name: `验收单元-${mode}`, description: 'UI 验收', mode, status: 'running', revision_id: 'r3', params: { maxmemory: '1gb', appendonly: 'yes' }, instances, accounts: [] }
}

const REVISIONS = [
  { id: 'r3', note: '当前版本', created_by: 'admin', created_at: '2026-11-10T08:00:00Z', params: { maxmemory: '1gb', appendonly: 'yes' } },
  { id: 'r2', note: '调小内存', created_by: 'admin', created_at: '2026-11-09T08:00:00Z', params: { maxmemory: '512mb', appendonly: 'yes' } },
  { id: 'r1', note: '初始版本', created_by: 'admin', created_at: '2026-11-08T08:00:00Z', params: { maxmemory: '256mb' } },
]

// A. 参数回滚（UnitDetail 参数版本列表 + 差异确认框）
async function checkParamRollback(page, errors) {
  console.log('== 新界面 A：参数回滚')
  const R = {
    preview: { status: 200, body: { base_revision: 'r3', changed: { maxmemory: '512mb' }, old: { maxmemory: '1gb' }, hot: ['maxmemory'], restart: [] } },
    rollback: { status: 202, body: { operation: { id: 9 } } },
  }
  const seen = await mockAPI(page, [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license() }) },
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
    { method: 'GET', path: /^\/units\/1$/, respond: () => ({ body: clusterUnit() }) },
    { method: 'GET', path: /^\/units\/1\/metrics$/, respond: () => ({ body: { instances: {} } }) },
    { method: 'GET', path: /^\/units\/1\/revisions$/, respond: () => ({ body: REVISIONS }) },
    { method: 'POST', path: /^\/units\/1\/params\/preview$/, respond: () => R.preview },
    { method: 'POST', path: /^\/units\/1\/revisions\/r2\/rollback$/, respond: () => R.rollback },
  ])
  try {
    await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
    const timeline = (rev) => page.locator('.ant-timeline-item', { hasText: rev })
    const rollbackBtn = page.locator('button', { hasText: '回滚到此版本' })
    check((await rollbackBtn.count()) === 2, '参数版本列表：只有非当前版本行有"回滚到此版本"（共 2 个）', `实际 ${await rollbackBtn.count()}`)
    check((await timeline('r3').locator('button', { hasText: '回滚到此版本' }).count()) === 0, '当前版本 r3 行没有回滚按钮')

    // 预览 + 差异确认框
    const previewReq = page.waitForRequest((r) => r.url().includes('/params/preview') && r.method() === 'POST')
    await timeline('r2').locator('button', { hasText: '回滚到此版本' }).click()
    const previewBody = (await previewReq).postDataJSON()
    const dialog = page.getByRole('dialog')
    await dialog.waitFor({ timeout: 10000 })
    check((await dialog.getByText('回滚参数到 r2').count()) > 0, '回滚确认框标题包含目标版本 r2')
    const diffText = await dialog.locator('.diff').innerText()
    check(diffText.includes('-maxmemory=1gb') && diffText.includes('+maxmemory=512mb'), '回滚确认框用 DiffView 展示差异', diffText)
    check(previewBody?.set?.maxmemory === '512mb' && Array.isArray(previewBody?.reset), '预览请求体 set 为目标版本参数', JSON.stringify(previewBody))
    await shot(page, 'A-param-rollback')

    // 确认回滚
    const rollbackReq = page.waitForRequest((r) => r.url().includes('/revisions/r2/rollback') && r.method() === 'POST')
    await dialog.getByRole('button', { name: '确认回滚' }).click()
    const body = (await rollbackReq).postDataJSON()
    check(body.parent_revision_id === 'r3' && !!body.note && body.restart === false, '回滚请求体含 parent_revision_id=r3 与 note', JSON.stringify(body))
    check(await page.getByText('回滚操作 #9 已提交').first().waitFor({ timeout: 10000 }).then(() => true, () => false), '回滚提交成功后提示操作编号')
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })

    // 409 冲突：提示可读，弹框保持打开
    R.rollback = { status: 409, body: { code: 'REVISION_CONFLICT', message: '参数版本已变化，请刷新后重试' } }
    await timeline('r2').locator('button', { hasText: '回滚到此版本' }).click()
    await dialog.waitFor({ timeout: 10000 })
    await dialog.getByRole('button', { name: '确认回滚' }).click()
    await page.getByText('参数版本已变化，请刷新后重试').first().waitFor({ timeout: 10000 })
    check(true, '回滚返回 409 时显示中文冲突提示')
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })

    // 差异为空：确认按钮禁用并提示无需回滚
    R.preview = { status: 200, body: { base_revision: 'r3', changed: {}, old: {}, hot: [], restart: [] } }
    await timeline('r1').locator('button', { hasText: '回滚到此版本' }).click()
    await dialog.waitFor({ timeout: 10000 })
    check((await dialog.getByText('目标版本与当前参数一致，无需回滚').count()) > 0, '差异为空时提示无需回滚')
    check(await dialog.getByRole('button', { name: '确认回滚' }).isDisabled(), '差异为空时确认按钮禁用')
    await page.keyboard.press('Escape')
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })

    // 预览 403：提示可读，不打开弹框
    R.preview = { status: 403, body: { code: 'FORBIDDEN', message: '当前角色无权预览参数变更' } }
    await timeline('r2').locator('button', { hasText: '回滚到此版本' }).click()
    await page.getByText('当前角色无权预览参数变更').first().waitFor({ timeout: 10000 })
    check(true, '预览返回 403 时显示中文提示')
    check((await page.getByRole('dialog').count()) === 0, '预览失败时不打开回滚确认框')
  } finally {
    await unmockAPI(page)
  }
  const fresh = errors.splice(0)
  check(fresh.length === 0, 'A 参数回滚：无页面异常与 console error', fresh.join('; '))
}

// B. agent 升级（Hosts）与版本库"种类"列（System）
async function checkAgentUpgrade(page, errors) {
  console.log('== 新界面 B：agent 升级')
  const host = (id, name, extra) => ({ id, name, hostname: `${name}.internal`, address: `10.0.0.${10 + id}`, arch: 'x86_64', os_release: 'Kylin V10', status: 'online', online: true, zone: 'az1', engine_current: '1.30.5-1', engine_versions: '1.30.5-1', agent_version: '1.2.0', cpu_percent: 12, cpu_cores: 8, mem_used_bytes: 2e9, mem_total_bytes: 8e9, last_seen_at: new Date().toISOString(), instances: [], warnings: [], ...extra })
  const hosts = [
    host(1, 'node-a', {}),
    host(2, 'node-b', { agent_version: '1.3.0' }),
    host(3, 'node-c', { online: false, status: 'offline', agent_version: '1.0.0' }),
    host(4, 'node-d', { arch: 'arm64', agent_version: '1.0.0' }),
  ]
  const packages = [
    { id: 1, name: 'sycache-agent', version: '1.3.0', arch: 'x86_64', variant: '', size: 4096, kid: 'kid-ui', created_by: 'admin', created_at: '2026-11-10T08:00:00Z' },
    { id: 2, name: 'sycache-agent', version: '1.2.1', arch: 'x86_64', variant: '', size: 4096, kid: 'kid-ui', created_by: 'admin', created_at: '2026-11-10T08:00:00Z' },
    { id: 3, name: 'sycache', version: '1.30.6-1', arch: 'x86_64', variant: '', size: 9e6, kid: 'kid-ui', created_by: 'admin', created_at: '2026-11-10T08:00:00Z' },
  ]
  const R = { upgrade: { status: 202, body: {} } }
  const seen = await mockAPI(page, [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license([]) }) },
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
    { method: 'GET', path: /^\/hosts$/, respond: () => ({ body: hosts }) },
    { method: 'GET', path: /^\/engine-packages$/, respond: () => ({ body: packages }) },
    { method: 'POST', path: /^\/hosts\/1\/upgrade-agent$/, respond: () => R.upgrade },
  ])
  try {
    await page.goto(`${BASE}/hosts`, { waitUntil: 'networkidle' })
    const row = (name) => page.locator('tr', { hasText: `${name}.internal` })
    check((await row('node-a').getByText('agent 1.2.0').count()) === 1, '主机列表显示 agent 版本')
    check((await row('node-a').getByRole('button', { name: '升级 agent' }).count()) === 1, '有同架构更高版本时显示"升级 agent"')
    check((await row('node-b').getByRole('button', { name: '升级 agent' }).count()) === 0, '已是最新版本时不显示升级入口')
    check((await row('node-c').getByRole('button', { name: '升级 agent' }).count()) === 0, '离线主机不显示升级入口')
    check((await row('node-d').getByRole('button', { name: '升级 agent' }).count()) === 0, '没有同架构升级包时不显示升级入口')

    await shot(page, 'B-hosts-list')
    await row('node-a').getByRole('button', { name: '升级 agent' }).click()
    const dialog = page.getByRole('dialog')
    await dialog.waitFor({ timeout: 10000 })
    check((await dialog.getByText('升级主机 agent').count()) > 0, '升级弹框标题正确')
    const select = dialog.locator('.ant-select').first()
    check((await select.innerText()).includes('1.3.0'), '目标版本默认选择最高版本 1.3.0', await select.innerText())
    await select.click()
    await page.locator('.ant-select-item-option', { hasText: '1.2.1' }).click()
    check((await select.innerText()).includes('1.2.1'), '可在弹框中改选较低的可升级版本 1.2.1')
    await shot(page, 'B-agent-upgrade')

    // 取消原生确认框：不发请求
    page.once('dialog', (d) => d.dismiss())
    await dialog.getByRole('button', { name: /^升\s*级$/ }).click()
    await sleep(500)
    check(callsTo(seen, 'POST', /upgrade-agent$/).length === 0, '确认框取消时不发起升级请求')

    // 确认升级
    let confirmText = ''
    page.once('dialog', (d) => { confirmText = d.message(); d.accept() })
    const upgradeReq = page.waitForRequest((r) => r.url().includes('/hosts/1/upgrade-agent') && r.method() === 'POST')
    await dialog.getByRole('button', { name: /^升\s*级$/ }).click()
    const body = (await upgradeReq).postDataJSON()
    check(body?.version === '1.2.1', '升级请求体 version 为所选版本', JSON.stringify(body))
    check(confirmText.includes('1.2.0') && confirmText.includes('1.2.1'), '升级前的确认文案包含当前与目标版本', confirmText)
    check((await page.getByText('已升级到 1.2.1').count()) > 0 || (await page.getByText('已升级到 1.2.1').waitFor({ timeout: 10000 }).then(() => true, () => false)), '升级成功后提示目标版本')
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })

    // 409 验签失败 / 403 无权限：可读中文提示
    for (const [status, code, msg] of [[409, 'AGENT_VERIFY_FAILED', 'agent 升级包签名校验失败'], [403, 'FORBIDDEN', '当前角色无权升级主机 agent']]) {
      R.upgrade = { status, body: { code, message: msg } }
      page.once('dialog', (d) => d.accept())
      await row('node-a').getByRole('button', { name: '升级 agent' }).click()
      await dialog.waitFor({ timeout: 10000 })
      await dialog.getByRole('button', { name: /^升\s*级$/ }).click()
      const shown = await page.getByText(msg).first().waitFor({ timeout: 10000 }).then(() => true, () => false)
      check(shown, `升级返回 ${status} 时显示中文提示`)
      await page.keyboard.press('Escape')
      await dialog.waitFor({ state: 'hidden', timeout: 10000 })
    }
  } finally {
    await unmockAPI(page)
  }

  // 系统页版本库"种类"列
  await mockAPI(page, [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license([]) }) },
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
    { method: 'GET', path: /^\/engine-packages$/, respond: () => ({ body: packages }) },
  ])
  try {
    await page.goto(`${BASE}/system`, { waitUntil: 'networkidle' })
    const headers = await page.locator('.ant-table-thead th').allInnerTexts()
    check(headers.some((t) => t.includes('种类')), '版本库表格有"种类"列', headers.join('|'))
    check((await page.locator('tr', { hasText: '1.3.0' }).first().innerText()).includes('agent 自升级'), 'sycache-agent 包的种类显示为"agent 自升级"')
    check((await page.locator('tr', { hasText: '1.30.6-1' }).first().innerText()).includes('数据面引擎'), 'sycache 包的种类显示为"数据面引擎"')
    await page.locator('.ant-table-thead th', { hasText: '种类' }).first().scrollIntoViewIfNeeded()
    await shot(page, 'B-system-packages')
  } finally {
    await unmockAPI(page)
  }
  const fresh = errors.splice(0)
  check(fresh.length === 0, 'B agent 升级：无页面异常与 console error', fresh.join('; '))
}

// C. 诊断（Diagnose）：慢日志按命令聚合、内存建议、原始 JSON 折叠
async function checkDiagnose(page, errors) {
  console.log('== 新界面 C：诊断')
  const slowInstances = [
    { instance_id: 11, host_id: 1, port: 7001, role: 'master', report: { slowlog: 2 }, aggregate: [
      { command: 'GET', count: 120, total_us: 98000, avg_us: 816, max_us: 5200, last_timestamp: 1789000000 },
      { command: 'HGETALL', count: 3, total_us: 45000, avg_us: 15000, max_us: 30000, last_timestamp: 1789000100 },
    ] },
    { instance_id: 12, host_id: 2, port: 7002, role: 'replica', report: { slowlog: 0 }, aggregate: [] },
  ]
  const memInstances = [{ instance_id: 11, host_id: 1, port: 7001, role: 'master', report: { used_memory: 1024 }, advice: [
    { level: 'warning', code: 'maxmemory_unset', message: '未设置 maxmemory，内存增长不受限制' },
    { level: 'info', code: 'frag_ratio', message: '内存碎片率 1.62 偏高，可观察是否需要整理' },
  ] }]
  const R = { override: null }
  const seen = await mockAPI(page, [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license([]) }) },
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
    { method: 'GET', path: /^\/units\/1$/, respond: () => ({ body: clusterUnit() }) },
    { method: 'GET', path: /^\/units\/1\/diagnose$/, respond: (req) => {
      if (R.override) return R.override
      return { body: { kind: req.query.kind, sample: 100, instances: req.query.kind === 'slowlog' ? slowInstances : memInstances } }
    } },
  ])
  try {
    await page.goto(`${BASE}/units/1/diagnose`, { waitUntil: 'networkidle' })
    await page.locator('.controls .ant-select').nth(1).click()
    await page.locator('.ant-select-item-option', { hasText: '慢日志' }).click()
    await page.getByRole('button', { name: '运行诊断' }).click()
    await page.getByText('按命令聚合（按总耗时降序）').first().waitFor({ timeout: 10000 })
    check((await page.getByText('按命令聚合（按总耗时降序）').count()) === 2, '慢日志页签每个实例显示"按命令聚合"表格')
    check((await page.locator('.ant-table-tbody tr', { hasText: 'HGETALL' }).count()) === 1, '聚合表包含 HGETALL 行')
    check((await page.getByText('总耗时 (µs)').count()) > 0, '聚合表列头为总耗时 (µs)')
    check((await page.getByText('没有慢日志记录').count()) === 1, '聚合为空的实例显示空状态')
    check((await page.getByText('内存建议').count()) === 0, '慢日志页签不显示内存建议')
    const details = page.locator('details.raw')
    check((await details.count()) === 1 && !(await details.first().getAttribute('open')), '原始 JSON 位于默认折叠的 details 中')
    await details.locator('summary').click()
    check((await details.locator('pre').innerText()).includes('"aggregate"'), '展开后可看到原始 JSON')
    await shot(page, 'C-diagnose-slowlog')

    // 内存页签：建议列表
    await page.locator('.controls .ant-select').nth(1).click()
    await page.locator('.ant-select-item-option', { hasText: '内存' }).click()
    await page.getByRole('button', { name: '运行诊断' }).click()
    await page.locator('.ant-alert-title', { hasText: '未设置 maxmemory，内存增长不受限制' }).waitFor({ timeout: 10000 })
    check((await page.getByText('内存建议').count()) === 1, '内存页签显示建议列表')
    check((await page.locator('.ant-alert-title', { hasText: '内存碎片率 1.62 偏高' }).count()) === 1, '内存建议逐条展示')
    check((await page.getByText('按命令聚合').count()) === 0, '内存页签不显示慢日志聚合表')

    // 无建议
    R.override = { body: { kind: 'memory', sample: 100, instances: [{ instance_id: 11, host_id: 1, port: 7001, role: 'master', report: {}, advice: [] }] } }
    await page.getByRole('button', { name: '运行诊断' }).click()
    await page.getByText('未发现需要关注的内存问题。').waitFor({ timeout: 10000 })
    check(true, '无内存建议时显示空结论')

    // 409：错误横幅可读
    R.override = { status: 409, body: { code: 'DIAGNOSE_BUSY', message: '实例正在执行其他诊断，请稍后重试' } }
    await page.getByRole('button', { name: '运行诊断' }).click()
    await page.getByText('实例正在执行其他诊断，请稍后重试').first().waitFor({ timeout: 10000 })
    check(true, '诊断返回 409 时显示中文错误')
    R.override = null
  } finally {
    await unmockAPI(page)
  }
  check(callsTo(seen, 'GET', /diagnose$/).length >= 4, '诊断请求按所选类型发出')
  const fresh = errors.splice(0)
  check(fresh.length === 0, 'C 诊断：无页面异常与 console error', fresh.join('; '))
}

// D. 集群再平衡（UnitDetail 在线迁槽区域）
async function checkRebalance(page, errors) {
  console.log('== 新界面 D：集群再平衡')
  const unbalanced = { body: { dry_run: true, plan: {
    unit_id: 1, basis: 'saved_state', balanced: false, tolerance: 0, moved: 101,
    nodes: [
      { instance_id: 11, name: 'node-a:7001', before: 5461, after: 5461, target: 5461 },
      { instance_id: 12, name: 'node-b:7002', before: 5462, after: 5461, target: 5461 },
      { instance_id: 13, name: 'node-c:7003', before: 5461, after: 5562, target: 5461 },
    ],
    steps: [{ index: 1, source_instance_id: 12, target_instance_id: 13, source: 'node-b:7002', target: 'node-c:7003', slots: [{ start: 10000, end: 10100 }], count: 101 }],
  } } }
  const balanced = { body: { dry_run: true, plan: { unit_id: 1, basis: 'saved_state', balanced: true, tolerance: 0, moved: 0, nodes: [], steps: [] } } }
  const R = { dry: unbalanced, exec: { status: 202, body: { operation: { id: 21 } } } }
  const unitMocks = (features) => [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license(features) }) },
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
    { method: 'GET', path: /^\/units\/1$/, respond: () => ({ body: clusterUnit() }) },
    { method: 'GET', path: /^\/units\/1\/metrics$/, respond: () => ({ body: { instances: {} } }) },
    { method: 'GET', path: /^\/units\/1\/revisions$/, respond: () => ({ body: REVISIONS }) },
  ]
  const seen = await mockAPI(page, [
    ...unitMocks(),
    { method: 'POST', path: /^\/units\/1\/rebalance$/, respond: (req) => (req.body?.dry_run ? R.dry : R.exec) },
  ])
  try {
    await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
    const rebalanceBtn = page.getByRole('button', { name: '再平衡' })
    check((await rebalanceBtn.count()) === 1 && (await rebalanceBtn.isEnabled()), '集群形态在线迁槽区域有可用的"再平衡"按钮')
    await rebalanceBtn.click()
    const dialog = page.getByRole('dialog')
    await dialog.waitFor({ timeout: 10000 })
    await page.getByText('本次共迁移 101 个槽').waitFor({ timeout: 10000 })
    const dry = callsTo(seen, 'POST', /rebalance$/).at(-1)
    check(dry?.body?.dry_run === true, '打开弹框先以 dry_run: true 请求计划', JSON.stringify(dry?.body))
    check((await dialog.getByText('node-b:7002').count()) > 0 && (await dialog.getByText('node-c:7003').count()) > 0, '计划表展示源 → 目标')
    check((await dialog.getByText('10000-10100').count()) > 0, '计划表展示槽范围')
    check((await dialog.getByRole('button', { name: '确认执行' }).count()) === 1, '有待执行步骤时按钮为"确认执行"')
    await shot(page, 'D-rebalance-plan')

    const execReq = page.waitForRequest((r) => r.url().includes('/rebalance') && r.method() === 'POST' && r.postDataJSON()?.dry_run === false)
    await dialog.getByRole('button', { name: '确认执行' }).click()
    const execBody = (await execReq).postDataJSON()
    check(execBody.dry_run === false, '确认后以 dry_run: false 再调用一次', JSON.stringify(execBody))
    check((await page.getByText('再平衡操作 #21 已提交').count()) > 0 || (await page.getByText('再平衡操作 #21 已提交').waitFor({ timeout: 10000 }).then(() => true, () => false)), '再平衡提交后提示操作编号')
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })

    // 已平衡：提示无需操作，不下发执行
    R.dry = balanced
    const before = callsTo(seen, 'POST', /rebalance$/).length
    await rebalanceBtn.click()
    await page.getByText('各主节点槽数已在目标范围内，无需操作。').waitFor({ timeout: 10000 })
    check(true, '已平衡时提示无需操作')
    await dialog.locator('.ant-modal-footer').getByRole('button', { name: /^关\s*闭$/ }).click()
    await dialog.waitFor({ state: 'hidden', timeout: 10000 })
    check(callsTo(seen, 'POST', /rebalance$/).slice(before).every((r) => r.body?.dry_run === true), '已平衡时不发送 dry_run: false')

    // 计划请求 409：错误可读
    R.dry = { status: 409, body: { code: 'UNIT_BUSY', message: '部署单元正在执行其他操作，请稍后再试' } }
    await rebalanceBtn.click()
    await page.getByText('部署单元正在执行其他操作，请稍后再试').first().waitFor({ timeout: 10000 })
    check(true, '计划预览返回 409 时显示中文提示')
  } finally {
    await unmockAPI(page)
  }

  // 非集群形态：没有在线迁槽区域与再平衡按钮
  for (const mode of ['standalone', 'sentinel']) {
    await mockAPI(page, [
      { method: 'GET', path: /^\/license$/, respond: () => ({ body: license() }) },
      { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) },
      { method: 'GET', path: /^\/units\/1$/, respond: () => ({ body: clusterUnit(mode) }) },
      { method: 'GET', path: /^\/units\/1\/metrics$/, respond: () => ({ body: { instances: {} } }) },
      { method: 'GET', path: /^\/units\/1\/revisions$/, respond: () => ({ body: REVISIONS }) },
    ])
    try {
      await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
      check((await page.getByRole('button', { name: '再平衡' }).count()) === 0, `${mode} 形态没有"再平衡"按钮`)
      check((await page.getByText('在线迁槽').count()) === 0, `${mode} 形态没有"在线迁槽"区域`)
    } finally {
      await unmockAPI(page)
    }
  }

  // 授权不含在线迁槽：按钮禁用
  await mockAPI(page, unitMocks([]))
  try {
    await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
    check(await page.getByRole('button', { name: '再平衡' }).isDisabled(), '授权不含在线迁槽时"再平衡"按钮禁用')
  } finally {
    await unmockAPI(page)
  }
  const fresh = errors.splice(0)
  check(fresh.length === 0, 'D 集群再平衡：无页面异常与 console error', fresh.join('; '))
}

// E. Web 命令行（Cli.vue）
async function checkCli(page, errors) {
  console.log('== 新界面 E：Web 命令行')
  const base = [
    { method: 'GET', path: /^\/license$/, respond: () => ({ body: license() }) },
    { method: 'GET', path: /^\/units\/1$/, respond: () => ({ body: clusterUnit() }) },
    { method: 'GET', path: /^\/units\/1\/metrics$/, respond: () => ({ body: { instances: {} } }) },
    { method: 'GET', path: /^\/units\/1\/revisions$/, respond: () => ({ body: REVISIONS }) },
  ]
  // 未授权：入口隐藏，直接访问显示提示
  await mockAPI(page, [...base, { method: 'GET', path: /^\/auth\/me$/, respond: () => me(false) }])
  try {
    await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
    check((await page.getByRole('button', { name: 'Web 命令行' }).count()) === 0, '未授权用户在单元详情看不到"Web 命令行"入口')
    await page.goto(`${BASE}/units/1/cli`, { waitUntil: 'networkidle' })
    check((await page.getByText('未获得命令行授权，请联系管理员').count()) > 0, '未授权直接访问命令行页显示提示')
    check((await page.getByPlaceholder(/SET app:k/).count()) === 0, '未授权时不显示命令输入框')
  } finally {
    await unmockAPI(page)
  }

  // 已授权
  const R = { exec: { status: 200, body: { reply: { type: 'simple', value: 'OK' }, truncated: false } } }
  const seen = await mockAPI(page, [...base,
    { method: 'GET', path: /^\/auth\/me$/, respond: () => me(true) },
    { method: 'POST', path: /^\/units\/1\/exec$/, respond: () => R.exec },
  ])
  try {
    await page.goto(`${BASE}/units/1`, { waitUntil: 'networkidle' })
    const entry = page.getByRole('button', { name: 'Web 命令行' })
    check((await entry.count()) === 1, '授权用户在单元详情看到"Web 命令行"入口')
    await entry.click()
    await page.waitForURL(/\/units\/1\/cli/, { timeout: 10000 })
    const input = page.getByPlaceholder(/SET app:k/)
    await input.waitFor({ timeout: 10000 })

    // 带引号的 argv
    const execReq = page.waitForRequest((r) => r.url().includes('/exec') && r.method() === 'POST')
    await input.fill('SET k "a b"')
    await input.press('Enter')
    const req = await execReq
    check(new URL(req.url()).searchParams.get('instance_id') === '11', '执行请求带 instance_id 查询参数')
    check(JSON.stringify(req.postDataJSON()) === JSON.stringify({ argv: ['SET', 'k', 'a b'] }), '带引号的命令解析为正确 argv', JSON.stringify(req.postDataJSON()))
    await page.locator('.out .line', { hasText: /^OK$/ }).waitFor({ timeout: 10000 })
    check((await page.locator('.out .cmd', { hasText: '$ SET k "a b"' }).count()) === 1, '输出区回显命令')
    const instanceLabel = await page.locator('.controls .ant-select').first().innerText()
    check(instanceLabel.includes('端口 7001') && instanceLabel.includes('master'), '实例选择框显示端口与角色（而不是内部实例 ID）', instanceLabel)

    // error 回复标红
    R.exec = { status: 200, body: { reply: { type: 'error', value: 'WRONGTYPE Operation against a key holding the wrong kind of value' }, truncated: false } }
    await input.fill('LPUSH k x')
    await input.press('Enter')
    const errLine = page.locator('.out .line.error').first()
    await errLine.waitFor({ timeout: 10000 })
    check((await errLine.innerText()).includes('(error) WRONGTYPE'), 'error 类型回复以 (error) 展示')
    check((await errLine.evaluate((el) => getComputedStyle(el).color)) === 'rgb(192, 57, 43)', 'error 回复标红')

    // truncated 截断提示 + 数组格式化
    R.exec = { status: 200, body: { reply: { type: 'array', value: [{ type: 'bulk', value: 'x' }] }, truncated: true } }
    await input.fill('LRANGE big 0 -1')
    await input.press('Enter')
    await page.getByText('输出已截断：超过 256 KiB 的部分未显示').waitFor({ timeout: 10000 })
    check(true, 'truncated 为 true 时显示截断提示')
    check((await page.locator('.out').innerText()).includes('1) "x"'), '数组回复按序号格式化')
    await shot(page, 'E-cli')

    // 上下键翻历史
    await input.click()
    await input.press('ArrowUp')
    check((await input.inputValue()) === 'LRANGE big 0 -1', '↑ 翻出上一条命令', await input.inputValue())
    await input.press('ArrowUp')
    check((await input.inputValue()) === 'LPUSH k x', '连续 ↑ 翻出更早的命令')
    await input.press('ArrowDown')
    check((await input.inputValue()) === 'LRANGE big 0 -1', '↓ 翻回较新的命令')
    await input.press('ArrowDown')
    check((await input.inputValue()) === '', '↓ 到底后清空输入')
    R.exec = { status: 200, body: { reply: { type: 'integer', value: 3 }, truncated: false } }
    await input.fill('INCR n')
    await input.press('Enter')
    await page.locator('.out .line', { hasText: '(integer) 3' }).waitFor({ timeout: 10000 })
    R.exec = { status: 200, body: { reply: { type: 'null' }, truncated: false } }
    await input.fill('GET missing')
    await input.press('Enter')
    await page.locator('.out .line', { hasText: /^\(nil\)$/ }).first().waitFor({ timeout: 10000 })
    check(true, '整数与 nil 回复按类型格式化')

    // 解析错误不发请求
    const execsBefore = callsTo(seen, 'POST', /exec$/).length
    await input.fill('SET k "unterminated')
    await input.press('Enter')
    await page.getByText('双引号未闭合').first().waitFor({ timeout: 10000 })
    check(callsTo(seen, 'POST', /exec$/).length === execsBefore, '引号未闭合时不发起执行请求')
    await input.fill('')

    // 403：错误标红并显示中文原因
    R.exec = { status: 403, body: { code: 'CLI_EXEC_DENIED', message: '未获得命令行授权，请联系管理员' } }
    await input.fill('PING')
    await input.press('Enter')
    await page.locator('.out .line.error', { hasText: '未获得命令行授权' }).waitFor({ timeout: 10000 })
    check(true, '403 时错误标红并显示中文原因')

    // 清屏
    await page.getByRole('button', { name: /^清\s*屏$/ }).click()
    check((await page.getByText('输入数据命令后回车执行').count()) === 1, '清屏后清空输出区')

    const stored = await page.evaluate(() => { try { return JSON.stringify(Object.fromEntries(Object.entries(localStorage))) } catch { return '' } })
    check(!stored.includes('SET k') && !stored.includes('LPUSH') && !stored.includes('LRANGE'), '命令历史没有写入 localStorage')
  } finally {
    await unmockAPI(page)
  }
  const fresh = errors.splice(0)
  check(fresh.length === 0, 'E Web 命令行：无页面异常与 console error', fresh.join('; '))
}

async function newFeatureChecks(page) {
  await page.setViewportSize({ width: 1280, height: 900 })
  const errors = []
  page.on('pageerror', (e) => errors.push(`pageerror: ${e.message}`))
  page.on('console', (m) => { if (m.type() === 'error' && !isExpectedConsoleError(m)) errors.push(`console: ${m.text()}`) })
  await checkParamRollback(page, errors)
  await checkAgentUpgrade(page, errors)
  await checkDiagnose(page, errors)
  await checkRebalance(page, errors)
  await checkCli(page, errors)
}

async function main() {
  if (!ADMIN_PW) throw new Error('请设置 ADMIN_PW')
  const session = await loginAPI()
  const api = session.ctx
  try {
    console.log('== API smoke')
    const endpoints = ['/api/v1/auth/me', '/api/v1/license', '/api/v1/dashboard', '/api/v1/hosts', '/api/v1/units', '/api/v1/operations', '/api/v1/param-templates', '/api/v1/params/catalog', '/api/v1/alerts', '/api/v1/audit']
    for (const endpoint of endpoints) {
      const response = await api.get(endpoint)
      check(response.ok(), `${endpoint} 返回 ${response.status()}`)
    }
    const unitsResponse = await api.get('/api/v1/units')
    const units = unitsResponse.ok() ? await unitsResponse.json() : []

    const browser = await chromium.launch()
    const context = await browser.newContext({ locale: 'zh-CN' })
    const page = await context.newPage()
    const failedRequests = []
    page.on('response', (response) => {
      if (response.url().includes('/api/v1/') && response.status() >= 500) failedRequests.push(`${response.status()} ${response.url()}`)
    })
    try {
      await page.goto(`${BASE}/login`, { waitUntil: 'domcontentloaded' })
      await page.locator('input[autocomplete="username"]').fill(ADMIN_USER)
      await page.locator('input[autocomplete="current-password"]').fill(ADMIN_PW)
      const otp = page.locator('input#otp')
      const otpValue = session.totpSecret ? totpCode(session.totpSecret) : ADMIN_OTP
      if (await otp.count() > 0 && otpValue) await otp.fill(otpValue)
      await page.getByRole('button', { name: /登\s*录/ }).click()
      await page.waitForURL(/\/dashboard/, { timeout: 15000 })
      check((await page.getByRole('heading', { name: 'CacheOS 总览' }).count()) === 1, '登录后进入 CacheOS 总览')

      const pages = [
        ['/dashboard', 'CacheOS 总览'],
        ['/hosts', '主机'],
        ['/units', '部署单元'],
        ['/operations', '操作记录'],
        ['/templates', '参数模板'],
        ['/alerts', '告警'],
        ['/audit', '审计'],
        ['/system', '授权与系统'],
      ]
      if (Array.isArray(units) && units.length > 0) {
        const unitID = units[0].id
        pages.push(
          [`/units/${unitID}`, units[0].name],
          [`/units/${unitID}/diagnose`, '性能诊断'],
          [`/units/${unitID}/backups`, '备份与恢复'],
          [`/units/${unitID}/migrations`, '在线数据迁移'],
          [`/units/${unitID}/keys`, 'Key 浏览器'],
        )
      }
      for (const [path, heading] of pages) {
        await page.goto(`${BASE}${path}`, { waitUntil: 'networkidle' })
        check((await page.getByRole('heading', { name: heading, exact: true }).count()) > 0, `${path} 页面可打开`)
      }

      await page.goto(`${BASE}/hosts`, { waitUntil: 'networkidle' })
      check((await page.getByRole('button', { name: /生成接入命令/ }).count()) === 1, '主机页显示接入命令入口')
      await page.goto(`${BASE}/units`, { waitUntil: 'networkidle' })
      check((await page.getByRole('button', { name: /创建部署单元/ }).count()) === 1, '部署单元页显示创建入口')
      await newFeatureChecks(page)
      check(failedRequests.length === 0, '页面请求没有产生 5xx', failedRequests.join('; '))
    } finally {
      await context.close()
      await browser.close()
    }
  } finally {
    await api.dispose()
  }
  console.log(`---- ${failed} failed`)
  process.exit(failed ? 1 : 0)
}

main().catch((error) => {
  bad(error.message)
  process.exit(1)
})
