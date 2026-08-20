// Verifies the external application backend: a task run by something the platform
// does not run.
//
// This is the one execution backend where the work happens outside the platform
// entirely, so the checks are about the boundary. The credential must be stored
// and never handed back. The Goal must not save without an app, because the
// alternative is a task that queues and then fails. And the whole thing has to be
// choosable by a person who never touches an API.
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
  const put = (path, body) => call('PUT', path, body)
  const del = (path) => call('DELETE', path)

  const stamp = Date.now().toString(36)

  // --- the catalog entry ------------------------------------------------------
  const noSecret = await post('/api/v1/admin/external-apps', {
    name: `no-secret-${stamp}`, provider: 'dify', baseUrl: 'https://dify.internal', appKind: 'workflow', enabled: true,
  })
  check('API 키 없이 등록 거절', noSecret.status === 400, `HTTP ${noSecret.status}`)

  const badURL = await post('/api/v1/admin/external-apps', {
    name: `bad-url-${stamp}`, provider: 'dify', baseUrl: 'not-a-url', appKind: 'workflow', enabled: true, secret: 'app-x',
  })
  check('주소 형식 검사', badURL.status === 400, `HTTP ${badURL.status}`)

  const badKind = await post('/api/v1/admin/external-apps', {
    name: `bad-kind-${stamp}`, provider: 'dify', baseUrl: 'https://dify.internal', appKind: 'telepathy', enabled: true, secret: 'app-x',
  })
  check('앱 종류 검사', badKind.status === 400, `HTTP ${badKind.status}`)

  const created = await post('/api/v1/admin/external-apps', {
    name: `e2e-app-${stamp}`, provider: 'dify', baseUrl: 'https://dify.internal', appKind: 'workflow',
    description: 'e2e 전용', enabled: true, secret: 'app-secret-key',
  })
  const app = created.body
  check('외부 앱 등록', Boolean(app?.id) && app?.secretConfigured === true,
    `HTTP ${created.status} ${JSON.stringify(created.body?.error?.message ?? '')}`)
  if (!app?.id) throw new Error('cannot continue without an app')

  try {
    // The credential is stored, and the platform never hands it back.
    const listed = (await get('/api/v1/admin/external-apps')).body?.items ?? []
    const found = listed.find((item) => item.id === app.id)
    check('목록에 등록한 앱이 보임', Boolean(found))
    check('API 키는 돌려주지 않음', found && !('secret' in found) && !('secretValue' in found),
      JSON.stringify(Object.keys(found ?? {})))

    // Renaming must not drop the credential.
    const renamed = await post('/api/v1/admin/external-apps', { ...app, name: `e2e-app-${stamp}-renamed` })
    check('이름만 바꿔도 API 키 유지', renamed.body?.secretConfigured === true, JSON.stringify(renamed.body?.secretConfigured))

    // A Goal picks from a list every signed-in user can read — and that list
    // carries no credentials.
    const choosable = (await get('/api/v1/external-apps')).body?.items ?? []
    check('선택 목록에 앱이 있음', choosable.some((item) => item.id === app.id), `${choosable.length} apps`)

    // --- the Goal -------------------------------------------------------------
    const agents = (await get('/api/v1/agents')).body?.items ?? []
    const agent = agents.find((item) => item.workspaceId)
    if (!agent) throw new Error('no agent to configure')
    const goalBase = {
      description: '외부 앱으로 처리한다', successCriteria: [], failureCriteria: [], constraints: '',
      maxSteps: 5, maxToolCalls: 10, maxDurationSeconds: 300, maxRetries: 0,
      startOnDemand: false, stopAfterTask: false, completionStrategy: 'agent', concurrencyPolicy: 'queue',
      maxConcurrentRuns: 1, plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
      warmupSeconds: 0, keepWarmSeconds: 0, resumeFromCheckpoint: true, tokenBudget: 0, executionMode: 'task',
    }
    const before = (await get(`/api/v1/agents/${agent.id}/goal`)).body
    try {
      const noApp = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'dify' })
      check('앱을 고르지 않으면 거절', noApp.status === 400 && noApp.body?.error?.code === 'invalid_runner',
        `HTTP ${noApp.status} ${JSON.stringify(noApp.body?.error?.message ?? '')}`)

      const saved = await put(`/api/v1/agents/${agent.id}/goal`, {
        ...goalBase, runner: 'dify', externalAppId: app.id, externalInputKey: ' query ',
      })
      check('외부 앱 실행 목표 저장', saved.status === 200 && saved.body?.goal?.runner === 'dify' && saved.body?.goal?.externalAppId === app.id,
        `HTTP ${saved.status} ${JSON.stringify(saved.body?.error?.message ?? saved.body?.goal?.runner)}`)
      check('입력 변수 이름이 공백 없이 저장됨', saved.body?.goal?.externalInputKey === 'query',
        JSON.stringify(saved.body?.goal?.externalInputKey))

      // This backend starts nothing, so it must not demand a runtime the way the
      // in-Pod ones do.
      check('Runtime 없이도 저장됨', saved.body?.goal?.startOnDemand === false, JSON.stringify(saved.body?.goal?.startOnDemand))
    } finally {
      if (before?.goal) await put(`/api/v1/agents/${agent.id}/goal`, { ...before.goal, runner: 'prose', executionMode: before.executionMode ?? 'interactive' })
    }

    // --- the console ----------------------------------------------------------
    await page.goto(`${baseURL}/admin/external-apps`, { waitUntil: 'networkidle' })
    const adminText = await page.locator('main').innerText()
    check('관리자 화면에 외부 앱 목록이 있음', adminText.includes('외부 앱'))
    check('등록한 앱이 화면에 보임', adminText.includes(`e2e-app-${stamp}`))
  } finally {
    const removed = await del(`/api/v1/admin/external-apps/${app.id}`)
    check(`정리: 외부 앱 삭제`, removed.status === 204, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nexternal app e2e passed')
