// Exercises event triggers end to end: a task that finishes publishes an event,
// the dispatcher wakes a subscribed agent, the payload filter keeps unrelated
// agents asleep, and a trigger never fires on the event its own task produced.
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

  const tasksNow = async () => (await get('/api/v1/tasks')).body?.items ?? []
  const settle = async (taskId, wanted, timeoutMs = 120000) => {
    const deadline = Date.now() + timeoutMs
    let last = null
    while (Date.now() < deadline) {
      last = (await get(`/api/v1/tasks/${taskId}`)).body?.task
      if (wanted.includes(last?.status)) return last
      await page.waitForTimeout(2000)
    }
    return last
  }
  const waitFor = async (predicate, timeoutMs = 60000) => {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      const hit = predicate(await tasksNow())
      if (hit) return hit
      await page.waitForTimeout(2000)
    }
    return undefined
  }

  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub'))
  if (!stub) throw new Error('stub model endpoint not found — deploy model-stub first')
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')

  const stamp = Date.now().toString(36)
  const provision = async (name) => {
    const result = await post('/api/v1/agents', {
      name, description: '이벤트 트리거 e2e 전용', runtimeType: 'opencode',
      runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
      systemPrompt: '이벤트에 반응하는 에이전트입니다.', modelEndpointId: stub.id,
      securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
    })
    const agent = result.body?.agent ?? result.body
    if (!agent?.id) throw new Error(`agent ${name} not created: HTTP ${result.status} ${JSON.stringify(result.body)}`)
    const goal = await put(`/api/v1/agents/${agent.id}/goal`, {
      description: '맡은 작업을 수행한다.', successCriteria: ['결과를 보고한다'],
      maxSteps: 3, maxRetries: 0, completionStrategy: 'agent', plannerMode: 'native',
      approvalRequired: false, maxDelegationDepth: 0,
    })
    if (goal.status !== 200) throw new Error(`goal for ${name} not saved: HTTP ${goal.status}`)
    return agent
  }
  const source = await provision(`evt-source-${stamp}`)
  const watcher = await provision(`evt-watcher-${stamp}`)
  const bystander = await provision(`evt-bystander-${stamp}`)
  console.log(`source=${source.name} watcher=${watcher.name} bystander=${bystander.name}`)

  const events = await get('/api/v1/events')
  check('구독 가능한 이벤트 목록 제공', (events.body?.types ?? []).includes('task.completed'), `types=${(events.body?.types ?? []).length}`)

  // A bad event type must be refused at save time, not silently never fire.
  const rejected = await post(`/api/v1/agents/${watcher.id}/triggers`, {
    name: '잘못된 이벤트', type: 'event', enabled: true, eventType: 'task.exploded', priority: 'normal',
  })
  check('알 수 없는 이벤트 종류 거절', rejected.status === 400, `HTTP ${rejected.status}`)

  const armed = await post(`/api/v1/agents/${watcher.id}/triggers`, {
    name: '작업 완료 감시', type: 'event', enabled: true, eventType: 'task.completed',
    eventFilter: { agentId: source.id }, taskTitle: '완료 이벤트 후속 처리',
    taskInput: '앞선 작업의 결과를 확인하라.', priority: 'normal',
  })
  check('이벤트 Trigger 저장', armed.status < 300, `HTTP ${armed.status}`)
  const trigger = armed.body?.trigger ?? armed.body
  check('이벤트 종류 저장', trigger?.eventType === 'task.completed', `eventType=${trigger?.eventType}`)

  // The bystander watches the same event type but an agent that never runs, so
  // it must stay asleep no matter what else finishes.
  const other = await post(`/api/v1/agents/${bystander.id}/triggers`, {
    name: '다른 에이전트 감시', type: 'event', enabled: true, eventType: 'task.completed',
    eventFilter: { agentId: '00000000-0000-0000-0000-000000000000' },
    taskTitle: '반응하면 안 되는 작업', priority: 'normal',
  })
  check('필터가 다른 Trigger 저장', other.status < 300, `HTTP ${other.status}`)

  const before = (await tasksNow()).length
  const seeded = await post('/api/v1/tasks', { agentId: source.id, title: `이벤트 발행용 작업 ${stamp}`, input: '간단히 처리하라.' })
  const seededId = (seeded.body?.task ?? seeded.body)?.id
  const done = await settle(seededId, ['completed', 'failed', 'dead_letter'])
  check('발행용 작업 완료', done?.status === 'completed', `status=${done?.status} error=${done?.lastError ?? ''}`)

  const feed = (await get('/api/v1/events?type=task.completed')).body?.items ?? []
  const published = feed.find((event) => event.subjectId === seededId)
  check('완료 이벤트 발행', Boolean(published), published ? `dispatched=${Boolean(published.dispatchedAt)}` : `items=${feed.length}`)

  const reacted = await waitFor((tasks) => tasks.find((task) => task.agentId === watcher.id && task.source === 'event'))
  check('구독 에이전트가 이벤트로 기동', Boolean(reacted), reacted ? `${reacted.title} (${reacted.status})` : 'no task')
  check('이벤트 작업이 Trigger에 연결됨', reacted?.triggerId === trigger?.id, `triggerId=${reacted?.triggerId}`)
  if (reacted) {
    const detail = (await get(`/api/v1/tasks/${reacted.id}`)).body?.task
    check('이벤트 내용이 작업 입력에 포함', (detail?.input ?? '').includes('task.completed') && (detail.input ?? '').includes(seededId),
      (detail?.input ?? '').slice(0, 60).replace(/\n/g, ' '))
    const finished = await settle(reacted.id, ['completed', 'failed', 'dead_letter'])
    check('이벤트로 기동된 작업 처리', finished?.status === 'completed', `status=${finished?.status} error=${finished?.lastError ?? ''}`)

    // The watcher's own completion published another event. Its trigger must not
    // fire on it, or the pair would run forever.
    await page.waitForTimeout(12000)
    const loop = (await tasksNow()).filter((task) => task.agentId === watcher.id && task.source === 'event')
    check('자기 이벤트로 재기동하지 않음', loop.length === 1, `event tasks=${loop.length}`)
  }
  const bystanderTasks = (await tasksNow()).filter((task) => task.agentId === bystander.id)
  check('필터가 맞지 않는 Trigger는 반응하지 않음', bystanderTasks.length === 0, `tasks=${bystanderTasks.length}`)
  check('작업 대기열이 예상만큼만 늘어남', (await tasksNow()).length >= before + 2)

  for (const agent of [bystander, watcher, source]) {
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
console.log('\nevent trigger e2e passed')
