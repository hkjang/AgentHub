import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const base = process.env.BASE_URL ?? 'http://127.0.0.1:18090'
const launch = process.env.LAUNCH_PATH
const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true, args: ['--no-sandbox', '--host-resolver-rules=MAP * 127.0.0.1'] })
const context = await browser.newContext()
const page = await context.newPage()
const problems = []
page.on('console', (m) => { if (m.type() === 'error') problems.push('console: ' + m.text()) })
page.on('response', (r) => { if (r.status() >= 400) problems.push(`${r.status()} ${r.url()}`) })
page.on('websocket', (ws) => {
  console.log('ws opened:', ws.url())
  ws.on('close', () => console.log('ws closed'))
})
// Sign in first: the launch ticket is exchanged for a session on this origin.
await page.goto(base, { waitUntil: 'networkidle' })
await page.getByLabel('아이디').fill('admin')
await page.getByLabel('비밀번호').fill('local-development-password')
await page.getByRole('button', { name: '로그인', exact: true }).click()
await page.getByRole('heading', { name: /admin님/ }).waitFor({ timeout: 20000 })

await page.goto(launch.startsWith('http') ? launch : base + launch, { waitUntil: 'networkidle' })
await page.waitForSelector('.xterm-screen', { timeout: 20000 })
await page.waitForTimeout(12000)
await page.keyboard.type('안녕하세요')
await page.keyboard.press('Enter')
await page.waitForTimeout(6000)
const screen = await page.evaluate(() => Array.from({length: 30}, (_, i) => window.term?.buffer.active.getLine(i)?.translateToString(true) ?? '').join('\n'))
console.log('--- screen ---')
console.log(screen.replace(/\n{3,}/g, '\n'))
console.log('--- problems:', problems.length ? problems : 'none')
await browser.close()
