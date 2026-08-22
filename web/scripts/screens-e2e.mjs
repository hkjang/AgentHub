// Opens every screen the console has and fails if any of them breaks.
//
// The suites next to this one each drive one feature deeply. None of them would
// notice a screen that throws on render — a renamed field, a null nobody
// guarded, a route left behind by a refactor — because a screen nobody tests is
// exactly the screen nobody opens until a customer does. This is the cheap net
// under all of them: sign in once, visit every route, and insist that each one
// renders a heading, some content, no uncaught error and no 5xx.
import { chromium } from 'playwright-core'
import { readFile } from 'node:fs/promises'
import { chromiumPath } from './browser.mjs'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'

// Every route in App.tsx that a person can reach from the menu or the palette.
//
// The claim in that sentence used to be a comment, and a comment is not a check:
// a screen added to App.tsx and forgotten here left the suite reporting that
// every screen renders while one of them was never opened. The routes are read
// out of App.tsx below and compared against this list.
const screens = [
  ['/', /님, 안녕하세요/],
  ['/agents', /에이전트|작업대/],
  ['/catalog', /카탈로그/],
  ['/agents/builder', /에이전트 빌더/],
  ['/tasks', /작업|일감/],
  ['/runs', /실행 기록|작업 일지/],
  ['/code-review', /코드 리뷰/],
  ['/workflows', /워크플로/],
  ['/workspaces', /작업공간|자료/],
  ['/workspaces/snapshots', /스냅샷/],
  ['/runtime', /런타임/],
  ['/mcp/catalog', /MCP/],
  ['/mcp/bundles', /MCP/],
  ['/evaluation', /사전검증/],
  ['/reviews', /검토|승인/],
  ['/developer', /시크릿|API/],
  ['/sessions', /세션/],
  ['/admin/overview', /운영 현황/],
  ['/admin/policy', /정책/],
  ['/admin/dlp', /내용 검사/],
  ['/admin/execution', /실행 제어/],
  ['/admin/operations', /로그|감사/],
  ['/admin/runtime-settings', /설정 주입/],
  ['/admin/runtime-profiles', /프로파일/],
  ['/admin/runtime-images', /이미지/],
  ['/admin/models', /모델/],
  ['/admin/external-apps', /외부 앱/],
  ['/admin/mcp', /MCP/],
  ['/admin/mcp-bundles', /MCP/],
  ['/admin/users', /사용자/],
  ['/admin/quotas', /Quota/],
  ['/admin/security', /보안/],
  ['/admin/settings', /시스템 설정/]
]

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}

// The list above says it is every route. This is what makes that true.
//
// Routes nested under the shell are what a person reaches; the login route and
// the catch-all are not screens. A route added to App.tsx and forgotten here
// left this suite reporting that every screen renders while one was never
// opened — which is exactly the kind of claim this codebase keeps turning into
// a check.
const appSource = await readFile(new URL('../src/App.tsx', import.meta.url), 'utf8')
// A redirect is not a screen, and neither is a parameterised path this suite has
// no id for; the index route is the dashboard at '/'.
const declared = [...appSource.matchAll(/<Route (index|path="([^"]*)")[^>]*element=\{<([A-Za-z]+)/g)]
  .filter((m) => m[3] !== 'Navigate')
  .map((m) => (m[1] === 'index' ? '/' : `/${m[2]}`))
  .filter((path) => !path.includes(':') && !path.includes('*'))
const covered = new Set(screens.map(([route]) => route))
const missing = declared.filter((path) => !covered.has(path))
const stale = [...covered].filter((path) => !declared.includes(path))
check('App.tsx의 모든 화면이 목록에 있음', missing.length === 0, missing.join(', ') || `${declared.length}개`)
check('목록에 사라진 화면이 없음', stale.length === 0, stale.join(', '))

const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true, args: ['--no-sandbox'] })
try {
  const page = await (await browser.newContext()).newPage()
  let current = ''
  const noticed = []
  page.on('pageerror', (error) => noticed.push(`${current}: ${String(error).slice(0, 140)}`))
  // A failed resource is reported by the screen itself; an uncaught script error
  // and a 5xx are not, which is why only those two fail the run.
  page.on('console', (message) => {
    if (message.type() === 'error' && !/Failed to load resource/.test(message.text())) {
      noticed.push(`${current}: console ${message.text().slice(0, 140)}`)
    }
  })
  page.on('response', (response) => {
    if (response.status() >= 500) noticed.push(`${current}: ${response.status()} ${new URL(response.url()).pathname}`)
  })

  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })

  for (const [route, expected] of screens) {
    current = route
    await page.goto(baseURL + route, { waitUntil: 'networkidle' })
    const heading = await page.locator('h1, h2').first().innerText().catch(() => '')
    const body = (await page.locator('.page').first().innerText().catch(() => '')).trim()
    check(route, expected.test(heading) && body.length > 40, `${heading.replace(/\n/g, ' ').slice(0, 30)} · ${body.length}자`)
  }
  check('어느 화면에서도 스크립트 오류가 없음', noticed.length === 0, noticed.join(' | ').slice(0, 300))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nscreens e2e passed')
