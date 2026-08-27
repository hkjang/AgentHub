// Where does the console still speak the code's language?
//
// The run timeline used to print event identifiers where a person expects a
// name. That was one screen; this walks all of them and reports every visible
// string that reads as an identifier rather than as Korean — dotted lowercase
// names, snake_case, and SCREAMING_CASE — with the element it appeared in.
//
// It fails on any finding. An identifier a person is meant to see belongs in a
// <code> element, which this skips — so "put it in <code>" is the fix when the
// identifier is deliberate, and a label is the fix when it is not.
//
// A sweep that finds nothing proves nothing unless the scan is shown to fire, so
// it plants three identifiers in the live DOM and requires them back first.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const base = process.env.AGENTHUB_BASE_URL ?? 'http://localhost:18080'
const user = process.env.AGENTHUB_USER ?? 'testadmin'
const password = process.env.AGENTHUB_PASSWORD ?? 'integration-password-2026'

const ROUTES = [
  '/', '/catalog', '/agents', '/workspaces', '/runs', '/code-review',
  '/mcp/catalog', '/mcp/bundles', '/runtime', '/sessions', '/tasks', '/workflows',
  '/evaluation', '/reviews', '/developer',
  '/admin/settings', '/admin/overview', '/admin/execution', '/admin/policy',
  '/admin/dlp', '/admin/provenance', '/admin/runtime-settings', '/admin/operations',
  '/admin/runtime-profiles', '/admin/runtime-images', '/admin/models',
  '/admin/external-apps', '/admin/agent-servers', '/admin/mcp', '/admin/mcp-bundles',
  '/admin/users', '/admin/quotas', '/admin/security',
]

// An identifier a person was not meant to read: dotted lowercase, snake_case, or
// SCREAMING_CASE, with no Korean anywhere in it.
const IDENTIFIER = /^(?:[a-z][a-z0-9]*(?:[._][a-z0-9]+)+|[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)+)$/

const browser = await chromium.launch({ executablePath: chromiumPath(), args: ['--no-sandbox'] })
const page = await browser.newPage({ viewport: { width: 1500, height: 1100 } })
const findings = []
let failed = false
const scanPage = () => page.evaluate((source) => {
  const pattern = new RegExp(source)
  const out = []
  // Leaf elements only: a container's text is its children's.
  for (const el of document.querySelectorAll('body *')) {
    if (el.children.length > 0) continue
    const text = (el.textContent ?? '').trim()
    if (!text || text.length > 60) continue
    if (!pattern.test(text)) continue
    const tag = el.tagName.toLowerCase()
    // <code> is the place identifiers belong, and <pre> is a payload dump.
    if (tag === 'code' || tag === 'pre') continue
    const where = el.closest('[class]')?.className ?? ''
    out.push(`${tag}.${String(where).split(' ')[0]}  "${text}"`)
  }
  return [...new Set(out)]
}, IDENTIFIER.source)

try {
  await page.goto(base, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(user)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${user}님`) }).waitFor({ timeout: 20000 })

  // A sweep that finds nothing proves nothing until the detector is shown to
  // fire against a real page. This plants one identifier of each shape into the
  // live DOM, runs the same scan the routes get, and requires all three back.
  await page.evaluate(() => {
    for (const sample of ['runtime.acquiring', 'dead_letter', 'MAX_RUNTIMES']) {
      const probe = document.createElement('span')
      probe.className = 'vocabulary-probe'
      probe.textContent = sample
      document.body.appendChild(probe)
    }
  })
  const proof = (await scanPage()).filter((hit) => hit.includes('vocabulary-probe'))
  await page.evaluate(() => document.querySelectorAll('.vocabulary-probe').forEach((el) => el.remove()))
  if (proof.length !== 3) {
    console.log(`FAILED the scan found only ${proof.length}/3 planted identifiers; a clean sweep would mean nothing`)
    await browser.close()
    process.exit(1)
  }
  console.log('ok   심어 둔 식별자 3개를 그대로 찾아냅니다 — 이 스윕의 "0건"은 의미가 있습니다')

  for (const route of ROUTES) {
    await page.goto(base + route, { waitUntil: 'networkidle' }).catch(() => {})
    await page.waitForTimeout(700)
    const hits = [...await scanPage()]
    // The list is half a screen. The other half is what opens when somebody
    // clicks a row — the drawer where the run timeline lived, and where an
    // identifier is least likely to have been read by anyone.
    // Some rows open on a click; a task's run opens from one button on the row.
    const runButton = page.locator('table tbody tr button[title="실행 기록"], table tbody tr button[title="작업 일지"]').first()
    const firstCell = page.locator('table tbody tr td').first()
    const opener = (await runButton.count()) ? runButton : firstCell
    if (await opener.count()) {
      await opener.click({ timeout: 4000 }).catch(() => {})
      await page.waitForTimeout(900)
      for (const hit of await scanPage()) {
        if (!hits.includes(hit)) hits.push(`[열린 상세] ${hit}`)
      }
      const opened = await page.evaluate(() => document.querySelectorAll('.drawer, [role="dialog"], .detail-section').length)
      if (process.env.VOCAB_DEBUG) console.log(`    ${route}: 상세 요소 ${opened}개`)
      await page.keyboard.press('Escape').catch(() => {})
      await page.waitForTimeout(300)
    }
    if (hits.length) {
      findings.push({ route, hits })
      console.log(`${route}  (${hits.length})`)
      for (const hit of hits.slice(0, 12)) console.log(`    ${hit}`)
      if (hits.length > 12) console.log(`    … ${hits.length - 12} more`)
    }
  }
  const strings = findings.reduce((n, f) => n + f.hits.length, 0)
  console.log(`\n${findings.length} screens with identifier-looking text, ${strings} strings`)
  failed = strings > 0
} catch (error) {
  console.log('FAILED', error.message)
  failed = true
} finally {
  await browser.close()
}
process.exit(failed ? 1 : 0)
