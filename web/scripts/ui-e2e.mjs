// Full-menu browser walk: visits every application route, fails on any console
// error, uncaught exception, failed request or React error boundary, and checks
// that each page rendered its own heading rather than a blank shell.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { mkdir, writeFile } from 'node:fs/promises'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const shotDir = process.env.AGENTHUB_SHOT_DIR ?? '../coverage/e2e'

// [route, expected heading]
const ROUTES = [
  ['/', /님, 안녕하세요/],
  ['/catalog', /에이전트 카탈로그/],
  ['/agents', /내 에이전트/],
  ['/agents/builder', /에이전트 빌더/],
  ['/runtime', /내 런타임/],
  ['/sessions', /런타임 세션/],
  ['/tasks', /작업 대기열/],
  ['/workspaces', /내 작업공간/],
  ['/workspaces/snapshots', /작업공간 스냅샷/],
  ['/mcp/catalog', /MCP/],
  ['/mcp/bundles', /MCP/],
  ['/workflows', /에이전트 워크플로/],
  ['/evaluation', /에이전트 사전검증/],
  ['/reviews', /(검토|승인)/],
  ['/developer', /(시크릿|API)/],
  ['/admin/settings', /시스템 설정/],
  ['/admin/overview', /운영 현황/],
  ['/admin/execution', /실행 제어/],
  ['/admin/policy', /정책/],
  ['/admin/dlp', /내용 검사/],
  ['/admin/operations', /로그 · 감사/],
  ['/admin/runtime-profiles', /런타임 프로파일/],
  ['/admin/runtime-images', /런타임 이미지/],
  ['/admin/models', /모델 엔드포인트/],
  ['/admin/mcp', /MCP 서버/],
  ['/admin/mcp-bundles', /MCP 번들/],
  ['/admin/users', /사용자/],
  ['/admin/security', /(보안|네트워크)/],
]

// [route, the single menu that must be highlighted and named in the breadcrumb]
const NAV_ACTIVE = [
  ['/', '홈'],
  ['/agents', '내 에이전트'],
  ['/agents/builder', '에이전트 빌더'],
  ['/workspaces', '작업공간'],
  ['/workspaces/snapshots', '스냅샷'],
  ['/mcp/bundles', 'MCP 번들'],
  ['/admin/mcp', 'MCP 서버'],
  ['/admin/mcp-bundles', 'MCP 번들 관리'],
]

const problems = []
// The portal probes the session on boot, so a 401 before sign-in is expected.
let collecting = false
// The session-expiry check ends the session deliberately, so the 401 the browser
// logs for it is the expected outcome rather than a problem.
let expectingUnauthorized = false
const note = (route, kind, detail) => {
  if (!collecting && kind !== 'fatal') return
  problems.push(`${route} :: ${kind} :: ${detail}`)
}

// Benign noise that does not indicate a broken page.
const IGNORED = [/favicon/i, /Download the React DevTools/i]
const ignored = (text) => IGNORED.some((r) => r.test(text))

const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
let route = 'startup'
try {
  const page = await browser.newPage({ viewport: { width: 1600, height: 1000 } })
  page.on('console', (m) => {
    if (expectingUnauthorized && /401/.test(m.text())) return
    if (m.type() === 'error' && !ignored(m.text())) note(route, 'console', m.text().slice(0, 300))
  })
  page.on('pageerror', (e) => note(route, 'pageerror', String(e).slice(0, 300)))
  page.on('requestfailed', (r) => {
    if (!ignored(r.url())) note(route, 'requestfailed', `${r.url()} ${r.failure()?.errorText ?? ''}`)
  })
  page.on('response', (r) => {
    if (r.status() >= 500 && !ignored(r.url())) note(route, 'http5xx', `${r.status()} ${r.url()}`)
  })

  route = '/login'
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })
  collecting = true

  await mkdir(shotDir, { recursive: true })
  for (const [path, heading] of ROUTES) {
    route = path
    await page.goto(baseURL + path, { waitUntil: 'networkidle' })
    // Let polling pages settle so their first render is captured.
    await page.waitForTimeout(600)

    const body = await page.locator('body').innerText()
    if (/Something went wrong|Unexpected Application Error|Cannot read propert/i.test(body)) {
      note(path, 'errorboundary', body.slice(0, 200))
    }
    if (body.trim().length < 40) note(path, 'blank', `body text is ${body.trim().length} chars`)
    if (!heading.test(body)) note(path, 'heading', `expected ${heading} in page text`)

    // A visible empty state is fine; a page that renders nothing but the shell is not.
    const main = await page.locator('.page').count()
    if (main === 0) note(path, 'layout', 'no .page container rendered')

    await page.screenshot({ path: `${shotDir}/${path.replace(/\//g, '_') || '_root'}.png`, fullPage: true })
  }

  // Exactly one menu is highlighted, and it is the one for the open route. A
  // prefix match used to leave the previous menu active — /agents stayed lit on
  // /agents/builder, and /admin/mcp on /admin/mcp-bundles — and the breadcrumb
  // named that stale menu too.
  for (const [path, label] of NAV_ACTIVE) {
    route = `${path}:nav`
    await page.goto(baseURL + path, { waitUntil: 'networkidle' })
    const highlighted = await page.locator('.nav-link.active').allInnerTexts()
    if (highlighted.length !== 1) {
      note(route, 'nav', `${highlighted.length} menus highlighted: ${highlighted.map((t) => t.trim()).join(', ')}`)
    } else if (highlighted[0].trim() !== label) {
      note(route, 'nav', `highlighted ${highlighted[0].trim()} instead of ${label}`)
    }
    const crumb = (await page.locator('.breadcrumb strong').innerText()).trim()
    if (crumb !== label) note(route, 'breadcrumb', `named ${crumb} instead of ${label}`)
  }

  // A dropdown must not stay open over the page the user moves on to.
  route = '/dismiss'
  await page.goto(baseURL + '/agents', { waitUntil: 'networkidle' })
  await page.locator('.profile-button').click()
  await page.locator('.profile-menu').waitFor({ timeout: 5000 })
  await page.locator('.content-scroll').click({ position: { x: 5, y: 5 } })
  if ((await page.locator('.profile-menu').count()) !== 0) note(route, 'dropdown', 'a click outside did not close the profile menu')
  await page.locator('.notification-button').click()
  await page.locator('.notification-menu').waitFor({ timeout: 5000 })
  await page.getByRole('link', { name: '내 에이전트' }).click()
  await page.waitForTimeout(300)
  if ((await page.locator('.notification-menu').count()) !== 0) note(route, 'dropdown', 'navigating did not close the notification menu')

  // Interactive spot-checks that a read-only route walk cannot cover.
  route = '/catalog:drawer'
  await page.goto(baseURL + '/catalog', { waitUntil: 'networkidle' })
  await page.locator('.template-card').first().click()
  const drawer = page.getByRole('dialog', { name: '새 에이전트 만들기' })
  await drawer.waitFor({ timeout: 10000 })
  const nameInput = drawer.getByLabel(/에이전트 이름/)
  await nameInput.fill('UI Input Verification')
  if ((await nameInput.inputValue()) !== 'UI Input Verification') note(route, 'input', 'agent name not editable')
  const autoStart = drawer.getByText('생성 후 런타임 바로 시작')
  if ((await autoStart.count()) === 0) note(route, 'missing', 'auto-start toggle absent')
  await drawer.getByRole('button', { name: '닫기' }).click()

  route = '/agents:drawer'
  await page.goto(baseURL + '/agents', { waitUntil: 'networkidle' })
  await page.locator('.agent-cell').first().click()
  await page.locator('.drawer').waitFor({ timeout: 10000 })
  const logoText = await page.locator('.detail-hero .runtime-logo').innerText()
  if (!/^(OC|H|QP|A)$/.test(logoText.trim())) note(route, 'runtime-badge', `unexpected badge ${logoText}`)
  await page.screenshot({ path: `${shotDir}/_agent_drawer.png`, fullPage: true })

  // Every resource surface must offer edit/delete, not just create.
  const CRUD_SURFACES = [
    ['/agents', '.row-actions button[title="수정"]', '.row-actions button[title="삭제"]'],
    ['/workspaces', '.card-actions button[title="이름 수정"]', '.card-actions button[title="삭제"]'],
    ['/workspaces/snapshots', null, '.card-actions button[title="삭제"]'],
    ['/workflows', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/evaluation', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/admin/runtime-profiles', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/admin/runtime-images', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/admin/models', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/admin/mcp', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
    ['/admin/mcp-bundles', '.card-actions button[title="수정"]', '.card-actions button[title="삭제"]'],
  ]
  for (const [path, editSelector, deleteSelector] of CRUD_SURFACES) {
    route = `${path}:crud`
    await page.goto(baseURL + path, { waitUntil: 'networkidle' })
    await page.waitForTimeout(500)
    // Pages with no rows legitimately show an empty state instead.
    const rows = await page.locator('.row-actions, .card-actions').count()
    if (rows === 0) continue
    if (editSelector && (await page.locator(editSelector).count()) === 0) note(route, 'crud', 'no edit affordance')
    if (deleteSelector && (await page.locator(deleteSelector).count()) === 0) note(route, 'crud', 'no delete affordance')
  }

  // A delete must ask before it destroys anything.
  route = '/agents:filter'
  await page.goto(baseURL + '/agents', { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  const rowsBefore = await page.locator('tbody tr').count()
  await page.getByLabel('에이전트 검색').fill('zzz-no-such-agent')
  await page.waitForTimeout(300)
  if ((await page.locator('tbody tr').count()) !== 0) note(route, 'search', 'search did not narrow the list')
  await page.getByLabel('에이전트 검색').fill('')
  await page.waitForTimeout(300)
  if ((await page.locator('tbody tr').count()) !== rowsBefore) note(route, 'search', 'clearing search did not restore the list')

  route = '/agents:confirm'
  await page.goto(baseURL + '/agents', { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  await page.locator('.row-actions button[title="삭제"]').first().click()
  const confirm = page.getByRole('alertdialog')
  await confirm.waitFor({ timeout: 10000 })
  await confirm.getByRole('button', { name: '취소' }).click()
  if ((await page.getByRole('alertdialog').count()) !== 0) note(route, 'confirm', 'cancel did not dismiss the dialog')

  // The two screens whose concepts need explaining carry that explanation in the
  // page, and remember being dismissed.
  for (const [path, marker] of [['/workflows', '워크플로는 이렇게 사용합니다'], ['/tasks', '작업 대기열은 이렇게 사용합니다']]) {
    route = `${path}:guide`
    await page.goto(baseURL + path, { waitUntil: 'networkidle' })
    await page.waitForTimeout(400)
    if ((await page.getByText(marker).count()) === 0) note(route, 'guide', 'no usage guide on the page')
    if ((await page.locator('.guide-steps li').count()) < 4) note(route, 'guide', 'the guide has fewer steps than expected')
    await page.getByRole('button', { name: new RegExp(marker) }).click()
    await page.waitForTimeout(200)
    if ((await page.locator('.guide-steps').count()) !== 0) note(route, 'guide', 'collapsing the guide did not hide it')
    await page.reload({ waitUntil: 'networkidle' })
    await page.waitForTimeout(400)
    if ((await page.locator('.guide-steps').count()) !== 0) note(route, 'guide', 'a dismissed guide came back after a reload')
    await page.getByRole('button', { name: new RegExp(marker) }).click()
    await page.waitForTimeout(200)
    if ((await page.locator('.guide-steps').count()) === 0) note(route, 'guide', 'the guide could not be reopened')
  }

  // Every collaboration mode explains what it does, rather than being an English
  // word in a dropdown.
  route = '/workflows:modes'
  await page.goto(baseURL + '/workflows', { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: /워크플로 만들기/ }).first().click()
  await page.locator('.drawer').waitFor({ timeout: 10000 })
  const modeCards = page.locator('.mode-cards button')
  if ((await modeCards.count()) !== 6) note(route, 'modes', `${await modeCards.count()} mode cards, expected 6`)
  for (const label of ['순차 실행', '합의 표결', '감독 검토']) {
    if ((await page.locator('.mode-cards button', { hasText: label }).count()) === 0) note(route, 'modes', `no card for ${label}`)
  }
  await page.locator('.mode-cards button', { hasText: '합의 표결' }).click()
  if ((await page.locator('.mode-cards button.selected', { hasText: '합의 표결' }).count()) !== 1) note(route, 'modes', 'choosing a mode did not select it')
  await page.keyboard.press('Escape')

  // Retrying offers the choice the checkpoint makes possible, with the amount of
  // reusable work in view. Only meaningful when a retryable task exists.
  route = '/tasks:retry'
  await page.goto(baseURL + '/tasks', { waitUntil: 'networkidle' })
  await page.waitForTimeout(600)
  const retryButton = page.locator('.row-actions button[title="다시 실행"]').first()
  if (await retryButton.count()) {
    await retryButton.click()
    const dialog = page.getByRole('alertdialog', { name: '작업을 다시 실행' })
    await dialog.waitFor({ timeout: 10000 })
    await page.waitForTimeout(600)
    const text = await dialog.innerText()
    if (!/이어서 실행|처음부터/.test(text)) note(route, 'retry', `the retry dialog offered no choice: ${text.slice(0, 120)}`)
    if (!/단계/.test(text)) note(route, 'retry', 'the retry dialog does not say how much work would be reused')
    await dialog.getByRole('button', { name: '취소' }).click()
    if ((await page.getByRole('alertdialog').count()) !== 0) note(route, 'retry', 'cancel did not dismiss the retry dialog')
  }

  // Escape closes a modal. The shell answered Escape for its own overlays while
  // drawers and confirmations ignored it.
  route = '/escape'
  await page.goto(baseURL + '/catalog', { waitUntil: 'networkidle' })
  await page.locator('.template-card').first().click()
  await page.getByRole('dialog', { name: '새 에이전트 만들기' }).waitFor({ timeout: 10000 })
  await page.keyboard.press('Escape')
  await page.waitForTimeout(300)
  if ((await page.getByRole('dialog', { name: '새 에이전트 만들기' }).count()) !== 0) note(route, 'escape', 'Escape did not close the drawer')
  await page.goto(baseURL + '/agents', { waitUntil: 'networkidle' })
  await page.waitForTimeout(500)
  await page.locator('.row-actions button[title="삭제"]').first().click()
  await page.getByRole('alertdialog').waitFor({ timeout: 10000 })
  await page.keyboard.press('Escape')
  await page.waitForTimeout(300)
  if ((await page.getByRole('alertdialog').count()) !== 0) note(route, 'escape', 'Escape did not close the confirmation')

  route = '/palette'
  await page.keyboard.press('Escape')
  await page.keyboard.press('Control+K')
  const palette = page.getByRole('dialog', { name: '빠른 이동' })
  await palette.waitFor({ timeout: 10000 })
  // English keywords must still reach the Korean menu entries.
  await palette.getByRole('combobox').fill('agents')
  await page.waitForTimeout(200)
  if ((await palette.getByRole('option').count()) === 0) note(route, 'search', 'English keyword found no menu')
  await palette.getByRole('combobox').fill('작업공간')
  await page.waitForTimeout(200)
  if ((await palette.getByRole('option').count()) === 0) note(route, 'search', 'Korean label found no menu')
  await page.keyboard.press('ArrowDown')
  await page.keyboard.press('Enter')
  await page.waitForTimeout(500)
  if ((await page.getByRole('dialog', { name: '빠른 이동' }).count()) !== 0) note(route, 'keyboard', 'Enter did not navigate')

  // Last, because it signs the session out: an expired session has to return to
  // the sign-in page rather than leave an error banner on every screen.
  route = '/session-expiry'
  expectingUnauthorized = true
  await page.goto(baseURL + '/', { waitUntil: 'networkidle' })
  await page.context().clearCookies()
  // A different route, so the click actually loads something: staying put makes
  // no request and so cannot notice the session is gone.
  await page.getByRole('link', { name: '내 에이전트', exact: true }).click()
  await page.waitForTimeout(1500)
  if ((await page.getByRole('button', { name: '로그인', exact: true }).count()) !== 1) {
    note(route, 'session', 'an expired session did not return to the sign-in page')
  }

  await writeFile(`${shotDir}/problems.json`, JSON.stringify(problems, null, 2))
} catch (error) {
  note(route, 'fatal', String(error).slice(0, 400))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`FAILED with ${problems.length} problem(s):`)
  for (const p of problems) console.error('  - ' + p)
  process.exit(1)
}
console.log(`OK: ${ROUTES.length} routes + ${NAV_ACTIVE.length} menu-state checks + interactive checks passed with no console/network errors`)
