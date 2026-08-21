// Verifies the ACP execution backend — driving a runtime's own agent over the
// Agent Client Protocol instead of parsing what it printed.
//
// The thing worth a run of its own is not the plumbing but the answer the
// platform gives on somebody's behalf. Under this backend the agent asks before
// every tool it uses and the platform replies, so the settings that decide what
// it replies are the settings that decide what an unattended task is allowed to
// change. This checks that they are offered where a person will see them, that
// the combinations that would quietly defeat a safeguard are refused, and that a
// runtime with no agent to talk to cannot be configured to talk to one.
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

  // --- which runtimes say they speak it --------------------------------------
  //
  // One list, from the platform, answering one question: can this runtime be
  // handed a task this way. The console reads the same list, so a backend added
  // to a runtime shows up in the form without the form being edited.
  const runtimes = (await get('/api/v1/runtime-types')).body?.items ?? []
  const runnersOf = (type) => runtimes.find((item) => item.type === type)?.runners ?? []
  check('qwencode 가 ACP 실행을 지원한다고 표시', runnersOf('qwencode').includes('acp'), JSON.stringify(runnersOf('qwencode')))
  // Goose is the runtime that exists because of this backend: it speaks the
  // protocol natively and has no other way to be handed a task.
  check('goose 는 ACP 실행만 지원', JSON.stringify(runnersOf('goose')) === '["acp"]', JSON.stringify(runnersOf('goose')))
  check('browsercode 도 ACP 실행만 지원', JSON.stringify(runnersOf('browsercode')) === '["acp"]', JSON.stringify(runnersOf('browsercode')))
  // Which runtimes cannot be judged by tool kind is a fact the platform states,
  // not a list the console keeps: the warning in the goal drawer reads it.
  const coarse = runtimes.filter((item) => item.coarseToolKinds).map((item) => item.type).sort()
  check('도구 종류를 알려주지 않는 런타임을 플랫폼이 표시', JSON.stringify(coarse) === '["browsercode","goose"]', JSON.stringify(coarse))
  check('Qwen Code 는 도구 종류를 알려줌', !runtimes.find((item) => item.type === 'qwencode')?.coarseToolKinds)
  check('jupyter 도 같은 에이전트를 가지므로 지원', runnersOf('jupyter').includes('acp'), JSON.stringify(runnersOf('jupyter')))
  check('langflow 는 ACP 를 지원하지 않음', !runnersOf('langflow').includes('acp'), JSON.stringify(runnersOf('langflow')))
  check('opencode 는 자동 실행 백엔드가 없음', runnersOf('opencode').length === 0, JSON.stringify(runnersOf('opencode')))
  // The command an agent is started with is the platform's business, not the
  // console's: a person chooses a runtime, not a command line.
  check('시작 명령은 API 로 노출하지 않음',
    !JSON.stringify(runtimes).includes('agenthub-qwencode-run'))

  const templates = (await get('/api/v1/templates')).body?.items ?? []
  check('카탈로그에 Goose 템플릿이 게시됨', templates.some((item) => item.runtimeType === 'goose'),
    JSON.stringify(templates.map((item) => item.runtimeType)))
  const template = templates.find((item) => item.runtimeType === 'qwencode')
  if (!template) throw new Error('cannot continue without the qwencode template')
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace settings from')

  const created = await post('/api/v1/agents', {
    name: `acp-${stamp}`, templateId: template.id, runtimeType: 'qwencode',
    workspaceId: sample.workspaceId, runtimeProfileId: sample.runtimeProfileId ?? '',
    modelEndpointId: sample.modelEndpointId ?? '', description: 'acp e2e 전용',
  })
  const agent = created.body?.agent ?? created.body
  check('ACP 대상 Agent 생성', Boolean(agent?.id), `HTTP ${created.status}`)
  if (!agent?.id) throw new Error('cannot continue without an agent')

  const control = ((await get('/api/v1/agents')).body?.items ?? []).find((item) => item.runtimeType === 'opencode')

  try {
    const goalBase = {
      description: '작업공간의 코드를 살펴본다', successCriteria: [], failureCriteria: [], constraints: '',
      maxSteps: 8, maxToolCalls: 30, maxDurationSeconds: 900, maxRetries: 1,
      startOnDemand: true, stopAfterTask: false, completionStrategy: 'agent', concurrencyPolicy: 'queue',
      maxConcurrentRuns: 1, plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
      warmupSeconds: 0, keepWarmSeconds: 0, resumeFromCheckpoint: true, tokenBudget: 0, executionMode: 'task',
    }

    const saved = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp', approvalMode: 'auto-edit' })
    check('ACP 실행 목표 저장', saved.status === 200 && saved.body?.goal?.runner === 'acp' && saved.body?.goal?.approvalMode === 'auto-edit',
      `HTTP ${saved.status} ${JSON.stringify(saved.body?.error?.message ?? saved.body?.goal?.runner)}`)

    // The same approval mode as the headless runner, on purpose: it is the same
    // question, and a second setting would only be a second place to get it wrong.
    const defaulted = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp' })
    check('승인 모드를 비우면 가장 엄격한 쪽으로 저장', defaulted.body?.goal?.approvalMode === 'default',
      JSON.stringify(defaulted.body?.goal?.approvalMode))

    // Under ACP the platform is the one answering the agent's permission
    // requests, so yolo is the platform saying yes to all of them — which cannot
    // coexist with a Goal that says a person must approve.
    // No longer refused: under ACP the platform can put the question to a person,
    // so a permissive mode and human approval are no longer in conflict.
    const escalating = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp', approvalMode: 'yolo', approvalRequired: true })
    check('사람 승인 + 관대한 모드 조합 허용', escalating.status === 200,
      `HTTP ${escalating.status} ${JSON.stringify(escalating.body?.error?.message ?? '')}`)
    // The headless runner still cannot ask, so there it stays refused.
    const headless = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'cli', approvalMode: 'yolo', approvalRequired: true })
    check('헤드리스 실행에서는 같은 조합을 여전히 거절', headless.status === 400 && /yolo/.test(headless.body?.error?.message ?? ''),
      `HTTP ${headless.status}`)

    const badMode = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp', approvalMode: 'reckless' })
    check('알 수 없는 승인 모드 거절', badMode.status === 400, `HTTP ${badMode.status}`)

    const noRuntime = await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp', startOnDemand: false })
    check('Runtime 시작 없이 ACP 실행 저장 거절', noRuntime.status === 400, `HTTP ${noRuntime.status}`)

    if (control?.id) {
      const wrongRuntime = await put(`/api/v1/agents/${control.id}/goal`, { ...goalBase, runner: 'acp' })
      const message = wrongRuntime.body?.error?.message ?? ''
      check('ACP 에이전트가 없는 런타임에서는 거절', wrongRuntime.status === 400 && /지원하지 않습니다/.test(message),
        `HTTP ${wrongRuntime.status} ${JSON.stringify(message)}`)
      // A refusal that does not say what is possible instead makes somebody guess.
      check('거절 메시지가 대신 쓸 수 있는 방식을 알려줌', /추론 루프/.test(message), JSON.stringify(message))
    }

    // --- the console -----------------------------------------------------------
    await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
    await page.getByText(`acp-${stamp}`).first().waitFor({ timeout: 15000 })
    const row = page.locator('tr', { hasText: `acp-${stamp}` }).first()
    await row.locator('button[title^="목표"]').click()
    const drawer = page.locator('.drawer-form')
    await drawer.waitFor({ timeout: 15000 })
    check('목표 화면에 ACP 실행 선택지가 있음', (await drawer.innerText()).includes('ACP 실행'))

    const runner = drawer.locator('select:has(option[value="acp"])').first()
    await runner.selectOption('acp')
    const afterSelect = await drawer.innerText()
    // What a person needs to know before saving: who answers the agent's
    // requests, and what will and will not be counted afterwards.
    check('플랫폼이 도구 요청에 답한다고 설명', /플랫폼이 답|플랫폼에 묻고/.test(afterSelect))
    check('토큰 집계 조건을 미리 밝힘', /토큰 사용량은 에이전트가 알려줄 때만 집계/.test(afterSelect))
    check('승인 모드를 여기서도 고를 수 있음', afterSelect.includes('승인 모드'))
    // And the console says who will answer when the Goal asks for a person.
    await put(`/api/v1/agents/${agent.id}/goal`, { ...goalBase, runner: 'acp', approvalRequired: true })
    await page.reload({ waitUntil: 'networkidle' })
    await page.locator('tr', { hasText: `acp-${stamp}` }).first().locator('button[title^="목표"]').click()
    await drawer.waitFor({ timeout: 15000 })
    check('승인을 요구하면 사람이 답한다고 설명', /사람에게 전달/.test(await drawer.innerText()))

    const approval = drawer.locator('select:has(option[value="auto-edit"])').first()
    await approval.selectOption('default')
    check('엄격한 모드에서는 바꾸는 요청을 거절한다고 설명', /모두 거절/.test(await drawer.innerText()))
    await approval.selectOption('auto-edit')
    check('auto-edit 이 무엇을 허용하는지 설명', /작업공간 파일 편집/.test(await drawer.innerText()))
    await page.keyboard.press('Escape')
    // The same Goal, saved against the runtime that only does this: a backend
    // that worked for one runtime and not the other would be a branch nobody
    // wrote down.
    const gooseTemplate = templates.find((item) => item.runtimeType === 'goose')
    if (gooseTemplate) {
      const gooseCreated = await post('/api/v1/agents', {
        name: `goose-${stamp}`, templateId: gooseTemplate.id, runtimeType: 'goose',
        workspaceId: sample.workspaceId, runtimeProfileId: sample.runtimeProfileId ?? '',
        modelEndpointId: sample.modelEndpointId ?? '', description: 'acp e2e 전용',
      })
      const gooseAgent = gooseCreated.body?.agent ?? gooseCreated.body
      if (gooseAgent?.id) {
        const gooseGoal = await put(`/api/v1/agents/${gooseAgent.id}/goal`, { ...goalBase, runner: 'acp', approvalMode: 'auto' })
        check('Goose 에이전트에 ACP 목표 저장', gooseGoal.status === 200 && gooseGoal.body?.goal?.runner === 'acp',
          `HTTP ${gooseGoal.status} ${JSON.stringify(gooseGoal.body?.error?.message ?? '')}`)
        // The console has to say why a strict mode does nothing on this runtime,
        // because the reason is the agent's and not something a person could guess.
        await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
        await page.getByText(`goose-${stamp}`).first().waitFor({ timeout: 15000 })
        await page.locator('tr', { hasText: `goose-${stamp}` }).first().locator('button[title^="목표"]').click()
        const gooseDrawer = page.locator('.drawer-form')
        await gooseDrawer.waitFor({ timeout: 15000 })
        await gooseDrawer.locator('select:has(option[value="acp"])').first().selectOption('acp')
        await gooseDrawer.locator('select:has(option[value="auto-edit"])').first().selectOption('default')
        check('엄격한 모드가 Goose에서 소용없다고 경고', /도구의 종류를/.test(await gooseDrawer.innerText()))
        await gooseDrawer.locator('select:has(option[value="auto-edit"])').first().selectOption('auto')
        check('auto 를 고르면 경고가 사라짐', !/도구의 종류를/.test(await gooseDrawer.innerText()))
        await page.keyboard.press('Escape')

        const gooseFlow = await put(`/api/v1/agents/${gooseAgent.id}/goal`, { ...goalBase, runner: 'cli' })
        check('Goose 는 헤드리스 실행을 지원하지 않음', gooseFlow.status === 400, `HTTP ${gooseFlow.status}`)
        const gooseRemoved = await del(`/api/v1/agents/${gooseAgent.id}`)
        check(`정리: goose-${stamp} 삭제`, gooseRemoved.status === 204 || gooseRemoved.status === 200, `HTTP ${gooseRemoved.status}`)
      } else {
        check('Goose Agent 생성', false,
          `HTTP ${gooseCreated.status} ${JSON.stringify(gooseCreated.body?.error?.message ?? gooseCreated.body ?? '')}`)
      }
    }
  } finally {
    const removed = await del(`/api/v1/agents/${agent.id}`)
    check(`정리: acp-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nacp e2e passed')
