// Verifies how the platform presents and hands over to the runtime agents.
//
// The three adapters differ in ways that decide whether somebody picks the right
// one, and autonomous execution cannot do what those runtimes do — it is a prose
// loop. Both facts have to reach the person using the platform, and both used to
// live only in the heads of whoever wrote the adapters.
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
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })

  const call = (method, path, body) =>
    page.evaluate(async ([method, path, body]) => {
      const csrf = document.cookie.split('; ').find((c) => c.startsWith('agenthub_csrf='))
      const headers = { 'Content-Type': 'application/json' }
      if (csrf) headers['X-CSRF-Token'] = decodeURIComponent(csrf.split('=').slice(1).join('='))
      const response = await fetch(path, { method, credentials: 'include', headers, body: body === null ? undefined : JSON.stringify(body) })
      const text = await response.text()
      let parsed = null
      try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text } }
      return { status: response.status, body: parsed }
    }, [method, path, body ?? null])
  const get = (path) => call('GET', path)
  const post = (path, body) => call('POST', path, body)

  // The platform describes its own adapters, so the console cannot advertise a
  // runtime this build does not have — or describe one it does have wrongly.
  const runtimes = (await get('/api/v1/runtime-types')).body?.items ?? []
  check('런타임 유형을 플랫폼이 설명함', runtimes.length >= 4, `${runtimes.length} runtimes`)
  const byType = Object.fromEntries(runtimes.map((item) => [item.type, item]))
  for (const type of ['opencode', 'hermes', 'qwenpaw', 'custom']) {
    const item = byType[type]
    check(`${type} 설명 제공`, Boolean(item?.label && item?.summary && item?.bestFor && item?.workspace),
      JSON.stringify({ label: item?.label, bestFor: item?.bestFor?.slice(0, 20) }))
    check(`${type} 장단점 모두 명시`, (item?.strengths?.length ?? 0) > 0 && (item?.watchouts?.length ?? 0) > 0)
  }
  // The facts have to be the ones the operator actually deploys.
  check('OpenCode 포트 4096', byType.opencode?.port === 4096, String(byType.opencode?.port))
  check('Hermes 포트 8642', byType.hermes?.port === 8642, String(byType.hermes?.port))
  check('프록시로만 공개되는 런타임 표시', byType.hermes?.proxiedUi === true && byType.qwenpaw?.proxiedUi === true && byType.opencode?.proxiedUi !== true)
  check('MCP 도구 전달 여부 표시', byType.opencode?.mcpConfigured === true && byType.qwenpaw?.mcpConfigured === false)
  check('터미널 유무 표시', byType.opencode?.terminal === true && byType.qwenpaw?.terminal === false)

  // Only a handed-off task can be closed by hand: everything else keeps its
  // status, or the state would mean nothing.
  const tasks = (await get('/api/v1/tasks')).body?.items ?? []
  const notHandedOff = tasks.find((task) => task.status !== 'handoff')
  if (notHandedOff) {
    const refused = await post(`/api/v1/tasks/${notHandedOff.id}/resolve`, { status: 'completed' })
    check('인계되지 않은 작업은 손으로 완료할 수 없음', refused.status === 409 && refused.body?.error?.code === 'not_handed_off',
      `HTTP ${refused.status} ${refused.body?.error?.code ?? ''}`)
  }
  const badStatus = tasks[0] && await post(`/api/v1/tasks/${tasks[0].id}/resolve`, { status: 'running' })
  if (badStatus) check('완료·취소 외의 상태로는 마무리할 수 없음', badStatus.status === 400, `HTTP ${badStatus.status}`)

  // The catalog compares the runtimes where the choice is made.
  await page.goto(`${baseURL}/catalog`, { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: /런타임 유형 비교/ }).click()
  await page.locator('.runtime-compare-grid').waitFor({ timeout: 10000 })
  const cards = await page.locator('.runtime-compare-grid article').count()
  check('카탈로그에서 런타임을 비교할 수 있음', cards === 3, `${cards} cards`)
  const comparison = await page.locator('.runtime-compare-grid').innerText()
  check('비교에 작업공간 경로가 포함됨', comparison.includes('/workspace'))
  check('비교에 자동 실행의 한계가 적혀 있음', /자동 실행|이어받/.test(comparison), comparison.slice(0, 80))

  // And the task queue explains the handover, which is where a person meets it.
  await page.goto(`${baseURL}/tasks`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: '작업 대기열' }).waitFor({ timeout: 10000 })
  const guide = await page.locator('.guide-panel').innerText()
  check('작업 대기열이 런타임 인계를 안내함', /런타임에서 이어받/.test(guide))
  check('안내가 자동 실행의 한계를 말함', /파일 편집|명령 실행/.test(guide), guide.slice(0, 60))
  const legend = await page.locator('.guide-panel').innerText()
  check('상태 설명에 런타임 인계가 있음', /런타임 인계/.test(legend))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nruntime coupling e2e passed')
