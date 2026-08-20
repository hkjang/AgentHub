// Verifies the HolmesGPT runtime and the investigation backend.
//
// What is worth a run of its own here is not that a task can be handed to an
// investigator, but what comes back with the answer. An investigation whose
// evidence cannot be checked is an opinion, so this checks that the runtime
// declares the backend, that the settings deciding how far it may go are offered
// where somebody will see them, and that the two combinations which would
// quietly defeat a safeguard are refused.
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

  const runtimes = (await get('/api/v1/runtime-types')).body?.items ?? []
  const holmes = runtimes.find((item) => item.type === 'holmes')
  check('holmes 런타임 유형 제공', Boolean(holmes), JSON.stringify(runtimes.map((r) => r.type)))
  check('조사 실행만 지원', JSON.stringify(holmes?.runners ?? []) === '["investigate"]', JSON.stringify(holmes?.runners))
  check('터미널로 사람이 이어서 물어볼 수 있음', holmes?.terminal === true)
  check('MCP 도구가 설정으로 전달된다고 표시', holmes?.mcpConfigured === true)
  check('프록시로만 공개된다고 표시', holmes?.proxiedUi === true)
  check('하위 경로로도 열 수 있음', holmes?.hostSessionOnly !== true)
  // The Kubernetes toolset cannot work without cluster credentials the platform
  // does not inject, and a runtime that says so up front saves somebody an hour.
  check('Kubernetes 툴셋 한계를 미리 밝힘',
    (holmes?.watchouts ?? []).some((item) => /Kubernetes 툴셋/.test(item)), JSON.stringify(holmes?.watchouts))
  // No other runtime gained this backend by accident.
  const others = runtimes.filter((item) => item.type !== 'holmes' && (item.runners ?? []).includes('investigate'))
  check('다른 런타임에는 조사 실행이 없음', others.length === 0, JSON.stringify(others.map((item) => item.type)))

  const templates = (await get('/api/v1/templates')).body?.items ?? []
  const template = templates.find((item) => item.runtimeType === 'holmes')
  check('카탈로그에 HolmesGPT 템플릿이 게시됨', Boolean(template),
    JSON.stringify(templates.map((item) => item.runtimeType)))
  if (!template) throw new Error('cannot continue without the holmes template')

  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace settings from')
  const created = await post('/api/v1/agents', {
    name: `holmes-${stamp}`, templateId: template.id, runtimeType: 'holmes',
    workspaceId: sample.workspaceId, runtimeProfileId: sample.runtimeProfileId ?? '',
    modelEndpointId: sample.modelEndpointId ?? '', description: 'investigate e2e 전용',
  })
  const agent = created.body?.agent ?? created.body
  check('HolmesGPT Agent 생성', Boolean(agent?.id) && agent?.runtimeType === 'holmes',
    `HTTP ${created.status} ${JSON.stringify(created.body?.error?.message ?? '')}`)
  if (!agent?.id) throw new Error('cannot continue without an agent')

  const control = ((await get('/api/v1/agents')).body?.items ?? []).find((item) => item.runtimeType === 'opencode')

  try {
    const goalBase = {
      description: '왜 서비스가 실패하는지 밝힌다', successCriteria: [], failureCriteria: [], constraints: '',
      maxSteps: 8, maxToolCalls: 30, maxDurationSeconds: 900, maxRetries: 1,
      startOnDemand: true, stopAfterTask: false, completionStrategy: 'agent', concurrencyPolicy: 'queue',
      maxConcurrentRuns: 1, plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
      warmupSeconds: 0, keepWarmSeconds: 0, resumeFromCheckpoint: true, tokenBudget: 0, executionMode: 'task',
    }
    const saved = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'investigate' })
    check('조사 실행 목표 저장', saved.status === 200 && saved.body?.goal?.runner === 'investigate',
      `HTTP ${saved.status} ${JSON.stringify(saved.body?.error?.message ?? '')}`)
    // Reading is the default; running commands is not.
    check('셸 실행은 기본으로 꺼짐', saved.body?.goal?.cliApprovalMode === 'default',
      JSON.stringify(saved.body?.goal?.cliApprovalMode))

    const shell = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'investigate', cliApprovalMode: 'auto' })
    check('셸 실행을 명시적으로 켤 수 있음', shell.status === 200, `HTTP ${shell.status}`)

    const conflict = await put(`/api/v1/agents/${agent.id}/goal`, {
      ...goalBase, runner: 'investigate', cliApprovalMode: 'auto', approvalRequired: true,
    })
    check('사람 승인 + 셸 자동 허용 거절', conflict.status === 400,
      `HTTP ${conflict.status} ${JSON.stringify(conflict.body?.error?.message ?? '')}`)

    const noRuntime = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'investigate', startOnDemand: false })
    check('Runtime 시작 없이 조사 실행 저장 거절', noRuntime.status === 400, `HTTP ${noRuntime.status}`)

    if (control?.id) {
      const wrongRuntime = await put(`/api/v1/agents/${control.id}/goal`, { ...goalBase, runner: 'investigate' })
      check('조사 에이전트가 없는 런타임에서는 거절',
        wrongRuntime.status === 400 && /지원하지 않습니다/.test(wrongRuntime.body?.error?.message ?? ''),
        `HTTP ${wrongRuntime.status} ${JSON.stringify(wrongRuntime.body?.error?.message ?? '')}`)
    }
    const wrongRunner = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp' })
    check('HolmesGPT 에서 ACP 실행은 거절', wrongRunner.status === 400, `HTTP ${wrongRunner.status}`)

    // --- the console ---------------------------------------------------------
    await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
    await page.getByText(`holmes-${stamp}`).first().waitFor({ timeout: 15000 })
    await page.locator('tr', { hasText: `holmes-${stamp}` }).first().locator('button[title^="목표"]').click()
    const drawer = page.locator('.drawer-form')
    await drawer.waitFor({ timeout: 15000 })
    check('목표 화면에 조사 실행 선택지가 있음', (await drawer.innerText()).includes('조사 실행'))
    await drawer.locator('select:has(option[value="investigate"])').first().selectOption('investigate')
    const chosen = await drawer.innerText()
    check('근거가 기록에 남는다고 설명', /근거/.test(chosen))
    check('셸 실행 선택지를 제시', /셸 실행 허용/.test(chosen))
    check('조회만 하는 것이 기본이라고 설명', /셸 명령은 거절/.test(chosen))
    await page.keyboard.press('Escape')
  } finally {
    const removed = await del(`/api/v1/agents/${agent.id}`)
    check(`정리: holmes-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\ninvestigate e2e passed')
