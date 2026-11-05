// CacheOS 控制台端到端冒烟验收。
// 需要一个已初始化的控制台：
//   BASE=https://127.0.0.1:8443 ADMIN_USER=admin ADMIN_PW='...' node web/e2e/ui-acceptance.cjs
// 依赖 playwright（仅测试使用，不进入产品构建）。
const { chromium, request } = require('playwright')

const BASE = process.env.BASE || 'http://127.0.0.1:8443'
const ADMIN_USER = process.env.ADMIN_USER || 'admin'
const ADMIN_PW = process.env.ADMIN_PW || ''
let failed = 0
const ok = (msg) => console.log(`ok   ${msg}`)
const bad = (msg) => { console.error(`FAIL ${msg}`); failed++ }
const check = (condition, msg) => (condition ? ok(msg) : bad(msg))

async function loginAPI() {
  const ctx = await request.newContext({ baseURL: BASE, extraHTTPHeaders: { 'X-Requested-With': 'cacheos' } })
  const response = await ctx.post('/api/v1/auth/login', { data: { username: ADMIN_USER, password: ADMIN_PW } })
  if (!response.ok()) throw new Error(`登录失败：HTTP ${response.status()} ${await response.text()}`)
  return ctx
}

async function main() {
  if (!ADMIN_PW) throw new Error('请设置 ADMIN_PW')
  const api = await loginAPI()
  try {
    console.log('== API smoke')
    const endpoints = ['/api/v1/auth/me', '/api/v1/license', '/api/v1/dashboard', '/api/v1/hosts', '/api/v1/units', '/api/v1/operations', '/api/v1/param-templates', '/api/v1/params/catalog', '/api/v1/alerts', '/api/v1/audit']
    for (const endpoint of endpoints) {
      const response = await api.get(endpoint)
      check(response.ok(), `${endpoint} 返回 ${response.status()}`)
    }

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
