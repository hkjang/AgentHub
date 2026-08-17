// Exercises the autonomous control plane end to end through the real HTTP API:
// planning, memory, the approval gate (both decisions) and delegation with its
// depth and cycle guards. Everything runs against a live AgentHub, so a pass
// means the worker, the store and the API all agree.
import { chromium } from 'playwright-core'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = process.env.CHROMIUM_PATH ?? '/snap/bin/chromium'
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

  // Every call goes through the page so the session cookie and CSRF token are
  // exactly the ones the portal itself would send.
  const call = (method, path, body) =>
    page.evaluate(async ([method, path, body]) => {
      const csrf = document.cookie.split('; ').find((c) => c.startsWith('agenthub_csrf='))
      const headers = { 'Content-Type': 'application/json' }
      if (csrf) headers['X-CSRF-Token'] = decodeURIComponent(csrf.split('=').slice(1).join('='))
      const response = await fetch(path, {
        method, credentials: 'include', headers,
        body: body === null ? undefined : JSON.stringify(body),
      })
      const text = await response.text()
      let parsed = null
      try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text } }
      return { status: response.status, body: parsed }
    }, [method, path, body ?? null])

  const get = (path) => call('GET', path)
  const post = (path, body) => call('POST', path, body)
  const put = (path, body) => call('PUT', path, body)
  const del = (path) => call('DELETE', path)

  const settle = async (taskId, wanted, timeoutMs = 120000) => {
    const deadline = Date.now() + timeoutMs
    let last = null
    while (Date.now() < deadline) {
      const result = await get(`/api/v1/tasks/${taskId}`)
      last = result.body?.task ?? result.body
      if (wanted.includes(last?.status)) return last
      await page.waitForTimeout(2000)
    }
    return last
  }

  // The control plane is what is under test, not the model, so both agents are
  // provisioned here against the deterministic stub gateway. Picking whatever
  // agents happen to exist would make the run depend on their model backend.
  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub'))
  if (!stub) throw new Error('stub model endpoint not found — deploy model-stub first')
  const existing = (await get('/api/v1/agents')).body?.items ?? []
  const sample = existing.find((agent) => agent.workspaceId) ?? existing[0]
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')

  const stamp = Date.now().toString(36)
  const provision = async (name, prompt) => {
    const result = await post('/api/v1/agents', {
      name, description: '제어 플레인 e2e 전용', runtimeType: 'opencode',
      runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
      systemPrompt: prompt, modelEndpointId: stub.id,
      securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
    })
    const agent = result.body?.agent ?? result.body
    if (!agent?.id) throw new Error(`agent ${name} not created: HTTP ${result.status} ${JSON.stringify(result.body)}`)
    return agent
  }
  const actor = await provision(`ctl-actor-${stamp}`, '운영 조치를 담당하는 에이전트입니다.')
  const helper = await provision(`ctl-helper-${stamp}`, '인프라 점검을 담당하는 에이전트입니다.')
  console.log(`actor=${actor.name} helper=${helper.name}`)

  // --- planning, memory and the approval gate -----------------------------
  const goal = {
    description: '운영 이슈를 점검하고 필요한 조치를 승인받아 수행한다.',
    successCriteria: ['조치 결과를 보고한다'],
    maxSteps: 4, maxRetries: 0, completionStrategy: 'agent',
    plannerMode: 'platform', approvalRequired: true, maxDelegationDepth: 1,
    startOnDemand: false, stopAfterTask: false,
  }
  check('목표 저장(플래너/승인/위임 설정)', (await put(`/api/v1/agents/${actor.id}/goal`, goal)).status === 200)

  const created = await post('/api/v1/tasks', {
    agentId: actor.id, title: '결제 지연 점검 및 승인 조치', input: '승인 테스트: 결제 지연 원인을 점검하라.', priority: 'high',
  })
  check('작업 생성', created.status < 300, `HTTP ${created.status}`)
  const taskId = (created.body?.task ?? created.body)?.id
  if (!taskId) throw new Error(`task id missing: ${JSON.stringify(created.body)}`)

  const parked = await settle(taskId, ['waiting_approval', 'failed', 'dead_letter', 'completed'])
  check('승인 대기 상태로 정지', parked?.status === 'waiting_approval', `status=${parked?.status} error=${parked?.lastError ?? ''}`)

  const runId = parked?.currentRunId
  const detail = runId ? (await get(`/api/v1/runs/${runId}`)).body : null
  check('실행 계획 저장', Array.isArray(detail?.plan?.steps) && detail.plan.steps.length > 0, `mode=${detail?.plan?.mode}`)
  const events = detail?.events ?? []
  check('승인 요청 이벤트 기록', events.some((e) => e.type === 'approval.requested'))
  check('기억 기록 이벤트', events.some((e) => e.type === 'memory.written'))

  const memories = (await get(`/api/v1/agents/${actor.id}/memories`)).body?.items ?? []
  const remembered = memories.find((m) => m.key === '담당팀')
  check('에이전트 기억 영속화', Boolean(remembered), remembered ? remembered.value.trim() : `items=${memories.length}`)

  const approvals = (await get('/api/v1/approvals')).body?.items ?? []
  const pending = approvals.find((a) => a.resourceId === taskId && a.status === 'pending')
  check('승인 항목 생성', Boolean(pending), pending ? pending.summary : `pending=${approvals.length}`)

  if (pending) {
    check('승인 처리', (await post(`/api/v1/approvals/${pending.id}/approve`, { reason: '운영팀 확인 완료' })).status === 200)
    const resumed = await settle(taskId, ['completed', 'failed', 'dead_letter'])
    check('승인 후 작업 완료', resumed?.status === 'completed', `status=${resumed?.status} error=${resumed?.lastError ?? ''}`)
    check('승인 대기는 재시도로 계산되지 않음', (resumed?.attempts ?? 9) <= 1, `attempts=${resumed?.attempts}`)
    const finished = resumed?.currentRunId ? (await get(`/api/v1/runs/${resumed.currentRunId}`)).body : null
    check('산출물 저장', (finished?.artifacts ?? []).length > 0, `artifacts=${(finished?.artifacts ?? []).length}`)
  }

  // --- rejection ends the task without a retry -----------------------------
  const rejectTask = await post('/api/v1/tasks', {
    agentId: actor.id, title: '거절 경로 점검', input: '승인 테스트: 거절 시 동작을 확인한다.',
  })
  const rejectId = (rejectTask.body?.task ?? rejectTask.body)?.id
  const rejectParked = await settle(rejectId, ['waiting_approval', 'failed', 'completed'])
  check('거절 시나리오 승인 대기', rejectParked?.status === 'waiting_approval', `status=${rejectParked?.status}`)
  const rejectApproval = ((await get('/api/v1/approvals')).body?.items ?? []).find((a) => a.resourceId === rejectId && a.status === 'pending')
  if (rejectApproval) {
    check('거절 처리', (await post(`/api/v1/approvals/${rejectApproval.id}/reject`, { reason: '변경 동결 기간' })).status === 200)
    const rejected = await settle(rejectId, ['failed', 'completed', 'dead_letter'], 40000)
    check('거절된 작업 종료', rejected?.status === 'failed', `status=${rejected?.status} error=${rejected?.lastError ?? ''}`)
  }

  // --- delegation ----------------------------------------------------------
  check('위임 대상 목표 저장', (await put(`/api/v1/agents/${helper.id}/goal`, {
    description: '요청받은 인프라 점검을 수행한다.', successCriteria: ['점검 결과를 보고한다'],
    maxSteps: 3, maxRetries: 0, completionStrategy: 'agent', plannerMode: 'native',
    approvalRequired: false, maxDelegationDepth: 0,
  })).status === 200)

  const delegating = await post('/api/v1/tasks', {
    agentId: actor.id, title: '위임 경로 점검', input: `위임 테스트: 인프라 점검이 필요하다.\n대상: ${helper.name}`,
  })
  const delegateId = (delegating.body?.task ?? delegating.body)?.id
  const delegateDone = await settle(delegateId, ['completed', 'failed', 'dead_letter'])
  check('위임한 작업 완료', delegateDone?.status === 'completed', `status=${delegateDone?.status} error=${delegateDone?.lastError ?? ''}`)

  const allTasks = (await get('/api/v1/tasks')).body?.items ?? []
  const child = allTasks.find((task) => task.parentTaskId === delegateId)
  check('자식 작업 생성', Boolean(child), child ? `${child.title} → ${child.agentName}` : 'none')
  check('위임 깊이 증가', child?.delegationDepth === 1, `depth=${child?.delegationDepth}`)
  check('위임 출처 표시', child?.source === 'agent', `source=${child?.source}`)
  if (child) {
    const childDone = await settle(child.id, ['completed', 'failed', 'dead_letter'])
    check('위임받은 작업 처리', ['completed', 'failed'].includes(childDone?.status), `status=${childDone?.status}`)
    // The helper's depth budget is 0, so it must not delegate any further.
    const grandchild = ((await get('/api/v1/tasks')).body?.items ?? []).find((task) => task.parentTaskId === child.id)
    check('깊이 상한 초과 위임 차단', !grandchild, grandchild ? grandchild.title : '')
  }

  // --- notifications and cleanup -------------------------------------------
  const notifications = (await get('/api/v1/notifications')).body?.items ?? []
  check('완료 알림 발송', notifications.some((n) => n.type === 'task'), `items=${notifications.length}`)
  check('승인 대기 알림 발송', notifications.some((n) => n.type === 'approval'))

  if (remembered) {
    check('기억 삭제', (await del(`/api/v1/memories/${remembered.id}`)).status === 204)
    const left = (await get(`/api/v1/agents/${actor.id}/memories`)).body?.items ?? []
    check('삭제 후 기억 목록 반영', !left.some((m) => m.id === remembered.id))
  }

  // Leave the environment as it was found, so the suite can be run repeatedly.
  for (const agent of [helper, actor]) {
    const removed = await del(`/api/v1/agents/${agent.id}`)
    check(`정리: ${agent.name} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  problems.forEach((problem) => console.error(` - ${problem}`))
  process.exit(1)
}
console.log('\ncontrol plane e2e passed')
