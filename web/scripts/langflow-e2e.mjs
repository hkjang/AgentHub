// Verifies the Langflow runtime and the flow execution backend.
//
// Two things here are new in kind rather than in degree, and both fail quietly if
// they are wrong. The runtime is the first one that does not boot from the shared
// base image and the first whose UI cannot be served under a path prefix. And a
// Goal can now say that its work is a saved flow rather than a reasoning loop,
// which is only true for a runtime that holds flows — so the refusals matter as
// much as the acceptances: a flow-backed Goal that saves and then fails at three
// in the morning is the outcome this run exists to prevent.
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
  const langflow = runtimes.find((item) => item.type === 'langflow')
  check('langflow 런타임 유형 제공', Boolean(langflow), JSON.stringify(runtimes.map((r) => r.type)))
  check('Langflow 포트 7860', langflow?.port === 7860, String(langflow?.port))
  check('흐름 실행 가능으로 표시', langflow?.flowExecution === true)
  check('전용 도메인 필요로 표시', langflow?.hostSessionOnly === true)
  check('MCP 도구는 전달되지 않는다고 표시', langflow?.mcpConfigured === false)
  check('터미널 없음으로 표시', langflow?.terminal === false)
  check('주의사항에 전용 도메인이 언급됨', (langflow?.watchouts ?? []).some((line) => /도메인|Domain/.test(line)),
    JSON.stringify(langflow?.watchouts))

  // --- an agent on that runtime ---------------------------------------------
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')
  const base = {
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
    modelEndpointId: sample.modelEndpointId ?? '',
  }
  const created = await post('/api/v1/agents', {
    ...base, name: `langflow-${stamp}`, runtimeType: 'langflow',
    description: 'langflow e2e 전용', systemPrompt: '흐름 실행 검증용입니다.',
  })
  const agent = created.body?.agent ?? created.body
  check('langflow Agent 생성', Boolean(agent?.id), `HTTP ${created.status} ${JSON.stringify(created.body?.message ?? '')}`)
  if (!agent?.id) throw new Error('cannot continue without a langflow agent')

  const agents = (await get('/api/v1/agents')).body?.items ?? []
  const control = agents.find((item) => item.runtimeType === 'opencode')

  try {
    // --- the Goal's runner ---------------------------------------------------
    const goalBase = {
      description: '흐름으로 처리한다', successCriteria: [], failureCriteria: [], constraints: '',
      maxSteps: 5, maxToolCalls: 10, maxDurationSeconds: 600, maxRetries: 1,
      stopAfterTask: false, completionStrategy: 'agent', concurrencyPolicy: 'queue', maxConcurrentRuns: 1,
      plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
      warmupSeconds: 0, keepWarmSeconds: 0, resumeFromCheckpoint: true, tokenBudget: 0,
      executionMode: 'task',
    }
    const noFlow = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, startOnDemand: true, runner: 'flow' })
    check('흐름 없이 흐름 실행 저장 거절', noFlow.status === 400 && noFlow.body?.error?.code === 'invalid_runner',
      `HTTP ${noFlow.status} ${JSON.stringify(noFlow.body?.error?.message ?? noFlow.body)}`)

    const noRuntime = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, startOnDemand: false, runner: 'flow', flowId: 'flow-1' })
    check('Runtime 시작 없이 흐름 실행 저장 거절', noRuntime.status === 400 && /Runtime/.test(noRuntime.body?.error?.message ?? ''),
      `HTTP ${noRuntime.status} ${JSON.stringify(noRuntime.body?.error?.message ?? '')}`)

    const badRunner = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, startOnDemand: true, runner: 'sorcery', flowId: 'flow-1' })
    check('알 수 없는 실행 방식 거절', badRunner.status === 400, `HTTP ${badRunner.status}`)

    const saved = await put(`/api/v1/agents/${agent.id}/goal`, {
      ...goalBase, startOnDemand: true, runner: 'flow', flowId: ' flow-abc ', flowOutputComponent: ' ChatOutput-1 ',
    })
    check('흐름 실행 목표 저장', saved.status === 200 && saved.body?.goal?.runner === 'flow',
      `HTTP ${saved.status} ${JSON.stringify(saved.body?.error?.message ?? saved.body?.goal?.runner)}`)
    check('흐름 식별자가 공백 없이 저장됨', saved.body?.goal?.flowId === 'flow-abc' && saved.body?.goal?.flowOutputComponent === 'ChatOutput-1',
      JSON.stringify({ flowId: saved.body?.goal?.flowId, component: saved.body?.goal?.flowOutputComponent }))
    const reread = await get(`/api/v1/agents/${agent.id}/goal`)
    check('저장한 실행 방식이 다시 읽힘', reread.body?.goal?.runner === 'flow' && reread.body?.goal?.flowId === 'flow-abc',
      JSON.stringify({ runner: reread.body?.goal?.runner, flowId: reread.body?.goal?.flowId }))

    // Back to the prose loop keeps the chosen flow, so switching twice in the
    // console does not lose it.
    const backToProse = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, startOnDemand: true, runner: 'prose', flowId: 'flow-abc' })
    check('추론 루프로 되돌려도 흐름 선택 유지', backToProse.status === 200 && backToProse.body?.goal?.flowId === 'flow-abc',
      `HTTP ${backToProse.status} ${JSON.stringify(backToProse.body?.goal?.flowId)}`)

    // A runtime with no flow engine cannot run one, and says so.
    if (control?.id) {
      const wrongRuntime = await put(`/api/v1/agents/${control.id}/goal`, { ...goalBase, startOnDemand: true, runner: 'flow', flowId: 'flow-abc' })
      check('흐름을 갖지 않는 런타임에서는 거절', wrongRuntime.status === 400 && /지원하지 않습니다/.test(wrongRuntime.body?.error?.message ?? ''),
        `HTTP ${wrongRuntime.status} ${JSON.stringify(wrongRuntime.body?.error?.message ?? '')}`)
    }

    // --- the flow list -------------------------------------------------------
    const flows = await get(`/api/v1/agents/${agent.id}/flows`)
    check('Runtime이 없으면 흐름 목록은 이유와 함께 거절', flows.status === 409 && flows.body?.error?.code === 'runtime_not_running',
      `HTTP ${flows.status} ${JSON.stringify(flows.body?.error?.code ?? flows.body)}`)
    if (control?.id) {
      const unsupported = await get(`/api/v1/agents/${control.id}/flows`)
      check('흐름이 없는 런타임의 목록 요청은 이유와 함께 거절', unsupported.status === 409 && unsupported.body?.error?.code === 'flows_unsupported',
        `HTTP ${unsupported.status} ${JSON.stringify(unsupported.body?.error?.code ?? unsupported.body)}`)
    }

    // --- settings injection for a runtime with no configuration file ---------
    const before = (await get('/api/v1/admin/runtime-settings')).body ?? {}
    const restore = { profiles: before.profiles ?? [] }
    try {
      const withConfig = await put('/api/v1/admin/runtime-settings', { profiles: [{ runtimeType: 'langflow', config: { theme: 'dark' } }] })
      check('설정 파일이 없는 런타임의 config 오버레이 거절', withConfig.status === 400,
        `HTTP ${withConfig.status} ${JSON.stringify(withConfig.body?.error?.message ?? '')}`)
      const platformOwned = await put('/api/v1/admin/runtime-settings', { profiles: [{ runtimeType: 'langflow', env: { LANGFLOW_AUTO_LOGIN: 'false' } }] })
      check('플랫폼이 정하는 Langflow 변수 거절', platformOwned.status === 400,
        `HTTP ${platformOwned.status} ${JSON.stringify(platformOwned.body?.error?.message ?? '')}`)
      const ok = await put('/api/v1/admin/runtime-settings', { profiles: [{ runtimeType: 'langflow', env: { LANGFLOW_LOG_LEVEL: 'info', TZ: 'Asia/Seoul' } }] })
      check('사이트가 정하는 Langflow 변수 허용', ok.status === 200, `HTTP ${ok.status} ${JSON.stringify(ok.body?.error?.message ?? '')}`)

      const suggestions = (await get('/api/v1/admin/runtime-settings?runtimeType=langflow')).body?.suggestions ?? []
      check('설정 파일 없는 런타임에는 파일 설정을 제안하지 않음', suggestions.length > 0 && suggestions.every((item) => item.target !== 'config'),
        `${suggestions.length} suggestions, ${suggestions.filter((item) => item.target === 'config').length} config`)
      check('확인된 Langflow 변수 제안 포함', suggestions.some((item) => item.key === 'LANGFLOW_LOG_LEVEL' && item.verified === true))
    } finally {
      await put('/api/v1/admin/runtime-settings', restore)
    }

    // --- opening the editor needs an origin of its own -----------------------
    // Only checkable when a Langflow runtime is actually Ready, which needs a
    // cluster; the run says which branch it took rather than passing silently.
    const ready = ((await get('/api/v1/runtimes')).body?.items ?? []).find((item) => {
      const owner = agents.find((candidate) => candidate.id === item.agentId)
      return owner?.runtimeType === 'langflow' && ['running', 'ready'].includes(String(item.status).toLowerCase())
    })
    if (!ready) {
      console.log('  --   Ready 상태의 Langflow Runtime이 없어 세션 열기 검사는 건너뜁니다')
    } else {
      const gateway = (await get('/api/v1/admin/settings/sessionGateway')).body?.value ?? {}
      try {
        // The gateway settings are cached for a few seconds on purpose — every
        // request reads them — so each change needs the cache to turn over before
        // the behaviour it causes can be observed.
        const settle = () => new Promise((resolve) => setTimeout(resolve, 12000))
        await put('/api/v1/admin/settings/sessionGateway', { value: { ...gateway, enabled: true, baseDomain: '' } })
        await settle()
        const refused = await post(`/api/v1/runtimes/${ready.id}/launch`, {})
        check('전용 도메인 없이 Langflow 세션 열기 거절', refused.status === 409 && refused.body?.error?.code === 'runtime_base_domain_required',
          `HTTP ${refused.status} ${JSON.stringify(refused.body?.error?.code ?? refused.body)}`)
        await put('/api/v1/admin/settings/sessionGateway', { value: { ...gateway, enabled: true, scheme: 'https', baseDomain: 'rt.e2e.internal', sessionHours: 8 } })
        await settle()
        const opened = await post(`/api/v1/runtimes/${ready.id}/launch`, {})
        check('전용 도메인이 있으면 자체 오리진으로 열림', opened.status === 201 && opened.body?.mode === 'host',
          `HTTP ${opened.status} ${opened.body?.mode ?? ''}`)
      } finally {
        await put('/api/v1/admin/settings/sessionGateway', { value: gateway })
      }
    }

    // --- the console ---------------------------------------------------------
    await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
    await page.getByText(`langflow-${stamp}`).first().waitFor({ timeout: 15000 })
    const row = page.locator('tr', { hasText: `langflow-${stamp}` }).first()
    await row.locator('button[title^="목표"]').click()
    const drawer = page.locator('.drawer-form')
    await drawer.waitFor({ timeout: 15000 })
    const drawerText = await drawer.innerText()
    check('목표 화면에 실행 방식이 있음', drawerText.includes('실행 방식'), drawerText.slice(0, 80))
    check('흐름 실행 선택지가 설명과 함께 제시됨', drawerText.includes('흐름 실행'))
    await page.keyboard.press('Escape')

    // The same drawer for a runtime without a flow engine must not offer it.
    if (control?.name) {
      await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
      const other = page.locator('tr', { hasText: control.name }).first()
      await other.locator('button[title^="목표"]').click()
      const otherForm = page.locator('.drawer-form')
      await otherForm.waitFor({ timeout: 15000 })
      check('흐름이 없는 런타임에는 실행 방식이 나오지 않음', !(await otherForm.innerText()).includes('실행 방식'))
      await page.keyboard.press('Escape')
    }
  } finally {
    const removed = await del(`/api/v1/agents/${agent.id}`)
    check(`정리: langflow-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nlangflow e2e passed')
