// Verifies that a worker grows its concurrency to meet a backlog.
//
// A fixed worker drains a burst one or two tasks at a time. This run queues more
// tasks than the floor allows, watches the worker report a raised limit, checks
// that more tasks were genuinely in flight at once than the floor permits, and
// that the limit comes back down once the queue is empty.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { execFileSync } from 'node:child_process'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const namespace = process.env.AGENTHUB_NAMESPACE ?? 'agent-platform-system'
const floor = Number(process.env.AGENTHUB_WORKER_FLOOR ?? 2)
const burst = Number(process.env.AGENTHUB_SCALE_BURST ?? 8)

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}
const kubectl = (...args) => {
  try { return execFileSync('kubectl', args, { encoding: 'utf8', maxBuffer: 64 << 20 }) } catch { return '' }
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

  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub'))
  if (!stub) throw new Error('stub model endpoint not found — deploy model-stub first')
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')

  const stamp = Date.now().toString(36)
  const created = await post('/api/v1/agents', {
    name: `scale-${stamp}`, description: '자동 확장 e2e 전용', runtimeType: 'opencode',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    // The stub answers instantly, so a burst would drain before any queue
    // formed. Asking it to take its time is what makes the backlog observable.
    systemPrompt: '확장 검증용 에이전트입니다.\n지연초: 8', modelEndpointId: stub.id,
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)
  // Several tasks for one agent must be allowed to run at once, or the queue
  // would be serialised by the agent's own policy rather than by the worker.
  await put(`/api/v1/agents/${agent.id}/goal`, {
    description: '맡은 작업을 수행한다.', successCriteria: ['결과를 보고한다'],
    maxSteps: 3, maxRetries: 0, completionStrategy: 'agent', plannerMode: 'native',
    approvalRequired: false, maxDelegationDepth: 0,
    concurrencyPolicy: 'parallel', maxConcurrentRuns: burst,
  })

  const idle = (await get('/api/v1/queue')).body
  check('큐 조회 제공', typeof idle?.ready === 'number' && typeof idle?.running === 'number' && typeof idle?.workers === 'number',
    JSON.stringify(idle).slice(0, 120))

  const since = new Date(Date.now() - 60000).toISOString()
  const ids = []
  for (let i = 0; i < burst; i++) {
    const task = await post('/api/v1/tasks', { agentId: agent.id, title: `확장 부하 ${stamp}-${i}`, input: '간단히 처리하라.' })
    const id = (task.body?.task ?? task.body)?.id
    if (id) ids.push(id)
  }
  check('부하 작업 생성', ids.length === burst, `${ids.length}/${burst}`)

  // Watch both what the worker says and what the queue actually shows.
  let peakRunning = 0
  let raised = 0
  const deadline = Date.now() + 180000
  while (Date.now() < deadline) {
    const snapshot = (await get('/api/v1/queue')).body
    peakRunning = Math.max(peakRunning, snapshot?.running ?? 0)
    const log = kubectl('-n', namespace, 'logs', 'deploy/agenthub-worker', '--since-time', since, '--tail', '400')
    for (const line of log.split('\n')) {
      if (!line.includes('worker concurrency adjusted')) continue
      try {
        const value = JSON.parse(line).concurrency
        if (typeof value === 'number') raised = Math.max(raised, value)
      } catch { /* not a JSON log line */ }
    }
    const remaining = (await get('/api/v1/tasks')).body?.items?.filter(
      (task) => ids.includes(task.id) && !['completed', 'failed', 'dead_letter', 'cancelled'].includes(task.status)) ?? []
    if (remaining.length === 0 && raised > 0) break
    await page.waitForTimeout(3000)
  }

  check('백로그에 맞춰 동시 실행 상향', raised > floor, `reported concurrency=${raised} floor=${floor}`)
  check('바닥값보다 많은 작업이 동시에 진행', peakRunning > floor, `peak running=${peakRunning} floor=${floor}`)

  const done = ((await get('/api/v1/tasks')).body?.items ?? []).filter((task) => ids.includes(task.id))
  check('부하 작업 전부 완료', done.length === burst && done.every((task) => task.status === 'completed'),
    done.map((task) => task.status).join(','))

  // And it must come back down: a ceiling held forever is just a bigger floor.
  const cooled = await (async () => {
    const until = Date.now() + 180000
    while (Date.now() < until) {
      const log = kubectl('-n', namespace, 'logs', 'deploy/agenthub-worker', '--since-time', since, '--tail', '400')
      const lines = log.split('\n').filter((line) => line.includes('worker concurrency adjusted'))
      const last = lines.at(-1)
      if (last) {
        try {
          const value = JSON.parse(last)
          if (value.concurrency === floor && value.change < 0) return value
        } catch { /* not a JSON log line */ }
      }
      await page.waitForTimeout(5000)
    }
    return undefined
  })()
  check('큐가 비면 다시 축소', Boolean(cooled), cooled ? `concurrency=${cooled.concurrency}` : 'never scaled back down')

  const removed = await del(`/api/v1/agents/${agent.id}`)
  check(`정리: ${agent.name} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  problems.forEach((problem) => console.error(` - ${problem}`))
  process.exit(1)
}
console.log('\nworker auto scaling e2e passed')
