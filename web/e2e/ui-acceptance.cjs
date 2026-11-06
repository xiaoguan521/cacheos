// CacheOS 控制台端到端冒烟验收。
// 支持空数据目录自动初始化；已有数据目录需要提供 ADMIN_OTP：
//   BASE=https://127.0.0.1:8443 ADMIN_USER=admin ADMIN_PW='...' [ADMIN_OTP=123456] node web/e2e/ui-acceptance.cjs
// 依赖 playwright（仅测试使用，不进入产品构建）。
const crypto = require('node:crypto')
const { chromium, request } = require('playwright')

const BASE = process.env.BASE || 'http://127.0.0.1:8443'
const ADMIN_USER = process.env.ADMIN_USER || 'admin'
const ADMIN_PW = process.env.ADMIN_PW || ''
const ADMIN_OTP = process.env.ADMIN_OTP || ''
let failed = 0
const ok = (msg) => console.log(`ok   ${msg}`)
const bad = (msg) => { console.error(`FAIL ${msg}`); failed++ }
const check = (condition, msg) => (condition ? ok(msg) : bad(msg))

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
