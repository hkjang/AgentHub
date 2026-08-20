// Verifies the Qwen Code runtime and the headless CLI execution backend.
//
// Two things here are worth a run of their own. The catalog is where people
// start, so a runtime the platform supports but does not offer there does not
// exist for most of them — this checks the template is published and that an
// Agent created from it saves. And the `cli` runner is the first execution
// backend that changes files without a person watching, so the settings that
// decide how much it may change are checked as carefully as the ones that let it
// run at all: an approval mode that saves and then surprises somebody at three in
// the morning is the outcome this exists to prevent.
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

  // --- the adapter, as the platform describes it -----------------------------
  const runtimes = (await get('/api/v1/runtime-types')).body?.items ?? []
  const qwencode = runtimes.find((item) => item.type === 'qwencode')
  check('qwencode 런타임 유형 제공', Boolean(qwencode), JSON.stringify(runtimes.map((r) => r.type)))
  check('터미널 포트 7681', qwencode?.port === 7681, String(qwencode?.port))
  check('자체 에이전트 실행 가능으로 표시', qwencode?.cliExecution === true)
  check('터미널 있음으로 표시', qwencode?.terminal === true)
  check('MCP 도구가 설정에 등록된다고 표시', qwencode?.mcpConfigured === true)
  check('프록시로만 공개된다고 표시', qwencode?.proxiedUi === true)
  check('하위 경로로도 열 수 있음', qwencode?.hostSessionOnly !== true)

  // --- the catalog, which is where people start ------------------------------
  const templates = (await get('/api/v1/templates')).body?.items ?? []
  const byRuntime = Object.fromEntries(templates.map((item) => [item.runtimeType, item]))
  // Every runtime the platform supports and a person could choose has to be in
  // the catalog: that is where people start, and one that is missing there does
  // not exist for anybody who does not already know it is possible.
  const choosable = runtimes.filter((item) => item.type !== 'custom').map((item) => item.type)
  for (const type of choosable) {
    check(`카탈로그에 ${type} 템플릿이 게시됨`, Boolean(byRuntime[type]),
      JSON.stringify(templates.map((item) => `${item.runtimeType}:${item.name}`)))
  }
  await page.goto(`${baseURL}/catalog`, { waitUntil: 'networkidle' })
  const catalogText = await page.locator('main').innerText()
  for (const type of choosable) {
    const label = runtimes.find((item) => item.type === type)?.label ?? type
    check(`카탈로그 화면에 ${label} 카드가 보임`, catalogText.includes(label))
  }

  const template = byRuntime.qwencode
  if (!template) throw new Error('cannot continue without the qwencode template')

  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace settings from')
  const created = await post('/api/v1/agents', {
    name: `qwencode-${stamp}`, templateId: template.id, runtimeType: 'qwencode',
    workspaceId: sample.workspaceId, runtimeProfileId: sample.runtimeProfileId ?? '',
    modelEndpointId: sample.modelEndpointId ?? '', description: 'qwencode e2e 전용',
  })
  const agent = created.body?.agent ?? created.body
  check('템플릿에서 Qwen Code Agent 생성', Boolean(agent?.id) && agent?.runtimeType === 'qwencode',
    `HTTP ${created.status} ${JSON.stringify(created.body?.message ?? created.body?.error?.message ?? '')}`)
  if (!agent?.id) throw new Error('cannot continue without a qwencode agent')

  const control = ((await get('/api/v1/agents')).body?.items ?? []).find((item) => item.runtimeType === 'opencode')

  try {
    // --- the CLI runner ------------------------------------------------------
    const goalBase = {
      description: '작업공간의 코드를 정리한다', successCriteria: [], failureCriteria: [], constraints: '',
      maxSteps: 8, maxToolCalls: 30, maxDurationSeconds: 900, maxRetries: 1,
      startOnDemand: true, stopAfterTask: false, completionStrategy: 'agent', concurrencyPolicy: 'queue',
      maxConcurrentRuns: 1, plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
      warmupSeconds: 0, keepWarmSeconds: 0, resumeFromCheckpoint: true, tokenBudget: 0, executionMode: 'task',
    }
    const saved = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', cliApprovalMode: 'auto-edit' })
    check('에이전트 실행 목표 저장', saved.status === 200 && saved.body?.goal?.runner === 'cli' && saved.body?.goal?.cliApprovalMode === 'auto-edit',
      `HTTP ${saved.status} ${JSON.stringify(saved.body?.error?.message ?? saved.body?.goal?.runner)}`)

    const defaulted = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli' })
    check('승인 모드를 비우면 확인하는 모드로 저장', defaulted.body?.goal?.cliApprovalMode === 'default',
      JSON.stringify(defaulted.body?.goal?.cliApprovalMode))

    const badMode = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', cliApprovalMode: 'reckless' })
    check('알 수 없는 승인 모드 거절', badMode.status === 400, `HTTP ${badMode.status}`)

    // The approval gate parks a task and waits for a person; an agent told to
    // approve everything itself would sail straight past it.
    const conflict = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', cliApprovalMode: 'yolo', approvalRequired: true })
    check('사람 승인 + yolo 조합 거절', conflict.status === 400 && /yolo/.test(conflict.body?.error?.message ?? ''),
      `HTTP ${conflict.status} ${JSON.stringify(conflict.body?.error?.message ?? '')}`)
    const yoloAlone = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', cliApprovalMode: 'yolo' })
    check('승인 요구가 없으면 yolo 저장 가능', yoloAlone.status === 200, `HTTP ${yoloAlone.status}`)

    const noRuntime = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', startOnDemand: false })
    check('Runtime 시작 없이 에이전트 실행 저장 거절', noRuntime.status === 400, `HTTP ${noRuntime.status}`)

    if (control?.id) {
      const wrongRuntime = await put(`/api/v1/agents/${control.id}/goal`, { ...goalBase, runner: 'cli' })
      check('자체 에이전트가 없는 런타임에서는 거절', wrongRuntime.status === 400 && /지원하지 않습니다/.test(wrongRuntime.body?.error?.message ?? ''),
        `HTTP ${wrongRuntime.status} ${JSON.stringify(wrongRuntime.body?.error?.message ?? '')}`)
      // And a Qwen Code agent cannot run a Langflow flow either.
      const wrongRunner = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'flow', flowId: 'flow-1' })
      check('흐름 실행은 Qwen Code에서 거절', wrongRunner.status === 400, `HTTP ${wrongRunner.status}`)
    }

    // --- settings injection --------------------------------------------------
    const before = (await get('/api/v1/admin/runtime-settings')).body ?? {}
    const restore = { profiles: before.profiles ?? [] }
    try {
      const reserved = await put('/api/v1/admin/runtime-settings', { profiles: [{ runtimeType: 'qwencode', config: { mcpServers: {} } }] })
      check('플랫폼이 만드는 mcpServers 덮어쓰기 거절', reserved.status === 400,
        `HTTP ${reserved.status} ${JSON.stringify(reserved.body?.error?.message ?? '')}`)
      const ok = await put('/api/v1/admin/runtime-settings', { profiles: [{ runtimeType: 'qwencode', config: { tools: { approvalMode: 'auto-edit' } }, env: { TZ: 'Asia/Seoul' } }] })
      check('사이트가 정하는 설정 허용', ok.status === 200, `HTTP ${ok.status} ${JSON.stringify(ok.body?.error?.message ?? '')}`)
      const suggestions = (await get('/api/v1/admin/runtime-settings?runtimeType=qwencode')).body?.suggestions ?? []
      check('확인된 승인 모드 키를 제안함', suggestions.some((item) => item.key === 'tools.approvalMode' && item.verified === true),
        `${suggestions.length} suggestions`)
      check('키를 모르는 제안은 이 런타임에 나오지 않음', !suggestions.some((item) => item.verified === false && item.label.includes('YOLO')))
    } finally {
      await put('/api/v1/admin/runtime-settings', restore)
    }

    // --- the console ---------------------------------------------------------
    await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
    await page.getByText(`qwencode-${stamp}`).first().waitFor({ timeout: 15000 })
    const row = page.locator('tr', { hasText: `qwencode-${stamp}` }).first()
    await row.locator('button[title^="목표"]').click()
    const drawer = page.locator('.drawer-form')
    await drawer.waitFor({ timeout: 15000 })
    const drawerText = await drawer.innerText()
    check('목표 화면에 실행 방식이 있음', drawerText.includes('실행 방식'))
    check('에이전트 실행 선택지가 제시됨', drawerText.includes('에이전트 실행'))
    check('승인 모드를 고를 수 있음', drawerText.includes('승인 모드'))
    await page.keyboard.press('Escape')
  } finally {
    const removed = await del(`/api/v1/agents/${agent.id}`)
    check(`정리: qwencode-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nqwencode e2e passed')
