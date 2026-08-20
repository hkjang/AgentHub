// Verifies the reading mode a person chooses in their own profile menu.
//
// The console can be read two ways: the names the documentation and the API use,
// or the workshop words that carry the relationships with them — 작업대, 공구,
// 일감, 작업 일지. The mode is a preference rather than a decision the product
// makes for everybody, so what this checks is that it behaves like one: it
// belongs to the person, it survives a reload, and it can be put back.
//
// The last check is the one that matters most. A metaphor that renames outcomes
// starts lying: a failed run has to say 실패 in both modes. The vocabulary is
// allowed to rename what a thing *is*, never how it *went*.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}

const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
try {
  const context = await browser.newContext()
  const page = await context.newPage()
  const signIn = async () => {
    await page.goto(baseURL, { waitUntil: 'networkidle' })
    await page.getByLabel('아이디').fill(username)
    await page.getByLabel('비밀번호').fill(password)
    await page.getByRole('button', { name: '로그인', exact: true }).click()
    await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })
  }
  await signIn()

  const nav = page.locator('nav[aria-label="주 메뉴"]')
  const openProfile = async () => {
    await page.locator('.profile-button').click()
    await page.locator('.mode-switch').waitFor({ timeout: 5000 })
  }

  // --- the switch is where a person would look for it ------------------------
  await openProfile()
  const menu = page.locator('.profile-menu')
  check('프로필 메뉴에 보기 방식이 있음', /보기 방식/.test(await menu.innerText()))
  check('두 가지 모드를 제시', /일반 모드/.test(await menu.innerText()) && /공방 모드/.test(await menu.innerText()))
  check('현재 모드가 표시됨', await page.locator('.mode-option[aria-pressed="true"]').count() === 1)
  check('처음에는 일반 모드', /일반 모드/.test(await page.locator('.mode-option[aria-pressed="true"]').innerText()))

  // --- standard mode reads the way the documentation does --------------------
  check('일반 모드: 내 에이전트', (await nav.innerText()).includes('내 에이전트'))
  check('일반 모드: 작업 대기열', (await nav.innerText()).includes('작업 대기열'))
  check('일반 모드: MCP 카탈로그', (await nav.innerText()).includes('MCP 카탈로그'))

  // Standard mode is not a mode the vocabulary is allowed to touch: it is what
  // the console said before any of this existed, and what the documentation and
  // the rest of the test suite still say. Introducing the modes changed two of
  // these titles by accident, and the page suite found it rather than this one.
  await page.keyboard.press('Escape')
  for (const [route, title] of [
    ['/agents', '내 에이전트'],
    ['/runtime', '내 런타임'],
    ['/sessions', '런타임 세션'],
    ['/tasks', '작업 대기열'],
  ]) {
    await page.goto(`${baseURL}${route}`, { waitUntil: 'networkidle' })
    const heading = await page.locator('.page-header h1').innerText()
    check(`일반 모드 제목 그대로: ${route}`, heading.trim() === title, `"${heading.trim()}"`)
  }
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await openProfile()

  // --- switching changes the vocabulary --------------------------------------
  await page.locator('.mode-option', { hasText: '공방 모드' }).click()
  await page.keyboard.press('Escape')
  const workshopNav = await nav.innerText()
  check('공방 모드: 내 작업대', workshopNav.includes('내 작업대'))
  check('공방 모드: 일감 대기열', workshopNav.includes('일감 대기열'))
  check('공방 모드: 공구 카탈로그', workshopNav.includes('공구 카탈로그'))
  check('공방 모드에서는 이전 이름이 사라짐', !workshopNav.includes('내 에이전트') && !workshopNav.includes('작업 대기열'))

  // The page itself, not just the menu.
  await page.goto(`${baseURL}/tasks`, { waitUntil: 'networkidle' })
  check('페이지 제목도 바뀜', (await page.locator('.page-header h1').innerText()).includes('일감'))

  // --- and it stays chosen ---------------------------------------------------
  await page.reload({ waitUntil: 'networkidle' })
  check('새로고침해도 유지됨', (await page.locator('.page-header h1').innerText()).includes('일감'))

  // --- the check that keeps the metaphor honest ------------------------------
  //
  // Statuses are outcomes, not names. A run that failed says 실패 whichever way
  // the reader has the console set.
  await page.goto(`${baseURL}/tasks`, { waitUntil: 'networkidle' })
  const legend = await page.locator('main').innerText()
  const outcomes = ['실패', '완료', '대기']
  const present = outcomes.filter((word) => legend.includes(word))
  check('상태 표현이 공방 모드에서도 그대로', present.length > 0, `보이는 상태: ${present.join(', ') || '없음'}`)

  // --- searching still finds a screen by the other mode's name ---------------
  await page.keyboard.press('Meta+k').catch(() => undefined)
  const palette = page.locator('.command-panel')
  if (await palette.count() === 0) {
    await page.locator('.quick-button').click()
    await palette.waitFor({ timeout: 5000 })
  }
  await page.locator('.command-input input').fill('에이전트')
  check('반대쪽 이름으로도 검색됨', (await palette.innerText()).length > 0)
  await page.keyboard.press('Escape')

  // --- and it can be put back ------------------------------------------------
  await openProfile()
  await page.locator('.mode-option', { hasText: '일반 모드' }).click()
  await page.keyboard.press('Escape')
  check('일반 모드로 되돌릴 수 있음', (await nav.innerText()).includes('내 에이전트'))

  // --- it belongs to the person, not the browser -----------------------------
  await openProfile()
  await page.locator('.mode-option', { hasText: '공방 모드' }).click()
  await page.keyboard.press('Escape')
  await page.locator('.profile-button').click()
  await page.getByRole('button', { name: '로그아웃' }).click()
  await page.getByLabel('아이디').waitFor({ timeout: 15000 })
  const stored = await page.evaluate(() => Object.keys(window.localStorage).filter((key) => key.startsWith('agenthub.viewmode.')))
  check('선택이 사용자별로 저장됨', stored.length === 1 && stored[0] !== 'agenthub.viewmode.', JSON.stringify(stored))
  await signIn()
  check('다시 로그인하면 고른 모드로 돌아옴', (await nav.innerText()).includes('내 작업대'))

  // Leave the account as it was found.
  await openProfile()
  await page.locator('.mode-option', { hasText: '일반 모드' }).click()
  await page.keyboard.press('Escape')
  check('정리: 일반 모드로 되돌림', (await nav.innerText()).includes('내 에이전트'))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nview mode e2e passed')
