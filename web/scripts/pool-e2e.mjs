// Verifies the runtime warm pool against a live cluster.
//
// The pool exists so a scheduled task does not pay for a cold Pod, and so a
// person's workspace is never stopped by it. This run checks both: a cron
// trigger due inside the warm-up window brings the runtime up before it fires,
// the hold is visible and released on the schedule the operator configured, and
// a runtime a person takes over stops being the pool's business.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { execFileSync } from 'node:child_process'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const namespace = process.env.AGENTHUB_RUNTIME_NAMESPACE ?? 'agent-runtime-dev'

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}
const kubectl = (...args) => {
  try { return execFileSync('kubectl', args, { encoding: 'utf8', maxBuffer: 32 << 20 }) } catch { return '' }
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

  const waitFor = async (predicate, timeoutMs = 180000) => {
    const deadline = Date.now() + timeoutMs
    while (Date.now() < deadline) {
      const hit = await predicate()
      if (hit) return hit
      await page.waitForTimeout(3000)
    }
    return undefined
  }
  const agentNow = async (id) => ((await get('/api/v1/agents')).body?.items ?? []).find((item) => item.id === id)

  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub')) ?? models[0]
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample || !stub) throw new Error('need an existing agent and a model endpoint to copy references from')

  const stamp = Date.now().toString(36)
  const created = await post('/api/v1/agents', {
    name: `pool-${stamp}`, description: '워밍 풀 e2e 전용', runtimeType: 'opencode',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: '예열 검증용 에이전트입니다.', modelEndpointId: stub.id,
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)

  // Validation first: the settings bound a running Pod, so they are bounded.
  const base = {
    description: '예열 검증', successCriteria: ['보고한다'], maxSteps: 3, maxRetries: 0,
    completionStrategy: 'agent', plannerMode: 'native', approvalRequired: false, maxDelegationDepth: 0,
    startOnDemand: true, stopAfterTask: true,
  }
  check('과도한 예열 시간 거절', (await put(`/api/v1/agents/${agent.id}/goal`, { ...base, warmupSeconds: 7200 })).status === 400)
  check('음수 예열 시간 거절', (await put(`/api/v1/agents/${agent.id}/goal`, { ...base, warmupSeconds: -1 })).status === 400)
  check('중지 설정 없는 유지 시간 거절',
    (await put(`/api/v1/agents/${agent.id}/goal`, { ...base, stopAfterTask: false, keepWarmSeconds: 120 })).status === 400)

  // Warm up to 10 minutes ahead, and hold for a minute after the task.
  const saved = await put(`/api/v1/agents/${agent.id}/goal`, { ...base, warmupSeconds: 600, keepWarmSeconds: 60 })
  check('예열 설정 저장', saved.status === 200 && saved.body?.goal?.warmupSeconds === 600,
    `HTTP ${saved.status} warmup=${saved.body?.goal?.warmupSeconds} keep=${saved.body?.goal?.keepWarmSeconds}`)

  check('예열 전에는 Runtime 없음', !(await agentNow(agent.id))?.runtime, JSON.stringify((await agentNow(agent.id))?.runtime ?? null))

  // A cron trigger a few minutes out is inside the warm-up window, so the pool
  // should bring the runtime up now rather than at fire time.
  const fire = new Date(Date.now() + 5 * 60 * 1000)
  const schedule = `${fire.getUTCMinutes()} ${fire.getUTCHours()} * * *`
  const trigger = await post(`/api/v1/agents/${agent.id}/triggers`, {
    name: '예열 대상 스케줄', type: 'cron', enabled: true, schedule, timezone: 'UTC',
    taskTitle: '예열 후 실행', taskInput: '간단히 처리하라.', priority: 'normal',
  })
  check('예약 Trigger 저장', trigger.status < 300, `HTTP ${trigger.status} schedule=${schedule}`)

  const warmed = await waitFor(async () => {
    const current = await agentNow(agent.id)
    return current?.runtime?.warmUntil ? current : undefined
  })
  check('예약 시각 전에 Runtime 예열', Boolean(warmed), warmed ? `warmUntil=${warmed.runtime.warmUntil}` : 'not warmed')
  if (warmed) {
    check('예열 대상이 실행 상태로 전환', warmed.runtime.desiredState === 'running', warmed.runtime.desiredState)
    // The hold has to outlast the fire time, or the pool would stop the runtime
    // just before the task that needed it.
    check('유지 기한이 예약 시각 이후', new Date(warmed.runtime.warmUntil) > fire,
      `warmUntil=${warmed.runtime.warmUntil} fireAt=${fire.toISOString()}`)
  }

  const pool = (await get('/api/v1/runtime-pool')).body?.items ?? []
  check('풀 조회에 노출', pool.some((item) => item.agentId === agent.id), `items=${pool.length}`)

  const podReady = await waitFor(() => {
    const crd = warmed?.runtime?.crdName
    if (!crd) return false
    const states = kubectl('-n', namespace, 'get', 'pod', `${crd}-0`, '-o', 'jsonpath={.status.containerStatuses[*].ready}').trim()
    return states.length > 0 && !states.includes('false')
  }, 240000)
  check('예열 Pod 준비 완료', Boolean(podReady), warmed?.runtime?.crdName ?? 'no runtime')

  // A person taking the runtime over ends the pool's claim: it must not stop a
  // workspace somebody is working in.
  const runtimeId = warmed?.runtime?.id
  if (runtimeId) {
    check('사용자 재시작 요청', (await post(`/api/v1/runtimes/${runtimeId}/restart`)).status === 202)
    const takenOver = await waitFor(async () => {
      const current = await agentNow(agent.id)
      return current && !current.runtime?.warmUntil ? current : undefined
    }, 60000)
    check('사용자 인수 후 풀 소유권 해제', Boolean(takenOver), takenOver ? 'released' : 'still held')
    check('풀 조회에서도 사라짐',
      !((await get('/api/v1/runtime-pool')).body?.items ?? []).some((item) => item.agentId === agent.id))
    // And with the claim gone the pool must leave it running.
    await page.waitForTimeout(20000)
    const after = await agentNow(agent.id)
    check('인수한 Runtime은 계속 실행', after?.runtime?.desiredState === 'running', after?.runtime?.desiredState)
    await post(`/api/v1/runtimes/${runtimeId}/stop`)
  }

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
console.log('\nruntime warm pool e2e passed')
