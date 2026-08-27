// Does a finished run's timeline read as Korean, or as identifiers?
//
// The screen is the claim; this opens it. Point it at a deployment with
// AGENTHUB_BASE_URL, AGENTHUB_USER and AGENTHUB_PASSWORD.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const base = process.env.AGENTHUB_BASE_URL ?? 'http://localhost:18080'
const user = process.env.AGENTHUB_USER ?? 'testadmin'
const password = process.env.AGENTHUB_PASSWORD ?? 'integration-password-2026'
const wanted = process.env.TIMELINE_TASK_ID ?? ''

const browser = await chromium.launch({ executablePath: chromiumPath(), args: ['--no-sandbox'] })
const page = await browser.newPage({ viewport: { width: 1400, height: 1000 } })
let failures = 0
const check = (ok, what) => { console.log(`${ok ? 'ok  ' : 'FAIL'} ${what}`); if (!ok) failures++ }

try {
  await page.goto(base, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(user)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${user}님`) }).waitFor({ timeout: 20000 })

  await page.goto(`${base}/tasks`, { waitUntil: 'networkidle' })
  // The run drawer opens from one specific button on a task row. Clicking
  // whatever button happens to be last opens confirmation dialogs instead.
  const runButtons = page.locator('table tbody tr button[title="실행 기록"], table tbody tr button[title="작업 일지"]')
  const available = await runButtons.count()
  check(available > 0, `실행 기록을 열 수 있는 작업이 ${available}건 있습니다`)
  await runButtons.first().click()
  await page.locator('ol.run-timeline').first().waitFor({ timeout: 20000 })

  const timeline = page.locator('ol.run-timeline li')
  const count = await timeline.count()
  check(count > 0, `타임라인에 ${count}줄이 보입니다`)
  const headings = []
  for (let i = 0; i < count; i++) {
    headings.push((await timeline.nth(i).locator('strong').innerText()).trim())
  }
  console.log('   ', headings.join(' | '))
  const identifiers = headings.filter((text) => /^[a-z][a-z_]*\.[a-z_.]+$/.test(text))
  check(identifiers.length === 0, `식별자 그대로 나온 줄 없음 (있으면: ${identifiers.join(', ')})`)
  const dangling = headings.filter((text) => text.endsWith('.'))
  check(dangling.length === 0, `점으로 끝나는 줄 없음 (있으면: ${dangling.join(', ')})`)
  const korean = headings.filter((text) => /[가-힣]/.test(text))
  check(korean.length === headings.length, `${korean.length}/${headings.length} 줄이 한국어로 읽힙니다`)

  await page.screenshot({ path: process.env.TIMELINE_SHOT ?? 'timeline.png', fullPage: false })
} catch (error) {
  console.log('FAIL', error.message)
  failures++
} finally {
  await browser.close()
}
process.exit(failures === 0 ? 0 : 1)
