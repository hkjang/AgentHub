import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const base = process.env.BASE_URL ?? 'http://127.0.0.1:18090'
const launch = process.env.LAUNCH_URL
const browser = await chromium.launch({
  executablePath: chromiumPath(), headless: true,
  args: ['--no-sandbox', '--host-resolver-rules=MAP * 127.0.0.1'],
})
const context = await browser.newContext()
const page = await context.newPage()
const seen = []
page.on('response', async (r) => {
  const url = new URL(r.url())
  if (!url.pathname.startsWith('/api/v1/')) return
  const headers = await r.allHeaders().catch(() => ({}))
  seen.push({ status: r.status(), path: url.pathname, setCookie: headers['set-cookie']?.split('\n').map((c) => c.split('=')[0]).join(',') ?? '' })
})
await page.goto(base, { waitUntil: 'networkidle' })
await page.getByLabel('아이디').fill('admin')
await page.getByLabel('비밀번호').fill('local-development-password')
await page.getByRole('button', { name: '로그인', exact: true }).click()
await page.getByRole('heading', { name: /admin님/ }).waitFor({ timeout: 20000 })
seen.length = 0

await page.goto(launch, { waitUntil: 'domcontentloaded' })
await page.waitForTimeout(20000)
console.log('--- Langflow API calls the editor made ---')
for (const item of seen.slice(0, 14)) console.log(`  ${item.status} ${item.path}${item.setCookie ? '   set-cookie: ' + item.setCookie : ''}`)
const bad = seen.filter((i) => i.status >= 400)
console.log('--- failures:', bad.length ? bad.map((i) => `${i.status} ${i.path}`) : 'none')
console.log('--- cookies stored:', (await context.cookies()).filter((c) => !c.name.startsWith('agenthub_')).map((c) => c.name))
// XHR rather than fetch: Langflow patches fetch, and /tags is the one the user
// saw fail with a 500.
for (const path of ['/api/v1/tags', '/api/v1/projects/', '/api/v1/flows/?get_all=true&header_flows=true']) {
  const status = await page.evaluate((p) => new Promise((resolve) => {
    const xhr = new XMLHttpRequest()
    xhr.open('GET', p, true)
    xhr.withCredentials = true
    xhr.onloadend = () => resolve(xhr.status)
    xhr.send()
  }), path)
  console.log(`  direct ${path} -> ${status}`)
}
console.log('--- page text:', (await page.locator('body').innerText().catch(() => '')).slice(0, 160).replace(/\n+/g, ' | '))
await browser.close()
