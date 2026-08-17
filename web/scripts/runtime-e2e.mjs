// Opens every running Agent Runtime's browser workspace through the AgentHub
// session gateway and verifies the runtime UI actually boots: real document,
// no error page, and a screenshot for review.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { mkdir } from 'node:fs/promises'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const shotDir = process.env.AGENTHUB_SHOT_DIR ?? '../coverage/runtimes'

const problems = []
const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
try {
  const context = await browser.newContext({ viewport: { width: 1600, height: 1000 } })
  const page = await context.newPage()
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })

  // The portal reads the CSRF token from the agenthub_csrf cookie.
  const csrf = await page.evaluate(() => {
    const hit = document.cookie.split('; ').find((c) => c.startsWith('agenthub_csrf='))
    return hit ? decodeURIComponent(hit.split('=').slice(1).join('=')) : null
  })
  if (!csrf) throw new Error('CSRF cookie not present after login')
  const agents = await page.evaluate(async () => {
    const r = await fetch('/api/v1/agents', { credentials: 'include' })
    return (await r.json()).items
  })
  const running = agents.filter((a) => a.runtime && ['running', 'ready'].includes(String(a.runtime.status).toLowerCase()))
  if (!running.length) throw new Error('no running runtimes to verify')

  await mkdir(shotDir, { recursive: true })
  for (const agent of running) {
    const launch = await page.evaluate(
      async ([id, token]) => {
        const r = await fetch(`/api/v1/runtimes/${id}/launch`, {
          method: 'POST',
          credentials: 'include',
          headers: token ? { 'X-CSRF-Token': token } : {},
        })
        return { status: r.status, body: await r.json() }
      },
      [agent.runtime.id, csrf],
    )
    if (launch.status !== 201 || !launch.body.url) {
      problems.push(`${agent.name}: launch failed ${launch.status} ${JSON.stringify(launch.body)}`)
      continue
    }
    const tab = await context.newPage()
    const errors = []
    tab.on('pageerror', (e) => errors.push(String(e).slice(0, 200)))
    try {
      const response = await tab.goto(launch.body.url, { waitUntil: 'domcontentloaded', timeout: 45000 })
      if (!response || response.status() >= 400) {
        problems.push(`${agent.name}: gateway returned ${response?.status()}`)
      }
      await tab.waitForTimeout(3500)
      const title = await tab.title()
      const text = (await tab.locator('body').innerText()).trim()
      if (/Runtime 세션을 열 수 없습니다|Runtime에 연결하지 못했습니다/.test(text)) {
        problems.push(`${agent.name}: gateway error page — ${text.slice(0, 120)}`)
      }
      // The gateway proxies runtime UIs verbatim; nothing may be injected into them.
      const injected = await tab.locator('.agenthub-runtime-topbar').count()
      if (injected > 0) problems.push(`${agent.name}: AgentHub markup was injected into the runtime UI`)
      console.log(
        `  ${agent.runtimeType.padEnd(9)} ${agent.name.padEnd(22)} title="${title}" body=${text.length}b injected=${injected} pageerrors=${errors.length}`,
      )
      await tab.screenshot({ path: `${shotDir}/${agent.runtimeType}_${agent.name.replace(/\W+/g, '_')}.png`, fullPage: false })
    } catch (e) {
      problems.push(`${agent.name}: ${String(e).slice(0, 200)}`)
    } finally {
      await tab.close()
    }
  }
} catch (error) {
  problems.push(`fatal: ${String(error).slice(0, 300)}`)
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`FAILED with ${problems.length} problem(s):`)
  for (const p of problems) console.error('  - ' + p)
  process.exit(1)
}
console.log('OK: every running runtime workspace opened through the session gateway')
