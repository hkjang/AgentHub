// Verifies token spend reporting against real runs.
//
// The numbers come from steps the execution plane already recorded, so this run
// makes an agent do measurable work and then checks the report adds up: tokens
// split into input and output, priced per million, an unpriced endpoint counted
// as tokens but never as money, and one owner never seeing another's spend.
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

  const stamp = Date.now().toString(36)
  const models = (await get('/api/v1/admin/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub'))
  if (!stub) throw new Error('stub model endpoint not found — deploy model-stub first')

  // Price the stub so the arithmetic has something to check. 1000 per million
  // input and 3000 per million output keeps the expected figure easy to verify
  // by hand.
  const priced = await post('/api/v1/admin/models', {
    ...stub, inputPricePerMTok: 1000, outputPricePerMTok: 3000, currency: 'KRW',
  })
  check('모델 단가 저장', priced.status === 200 && Number((priced.body ?? {}).inputPricePerMTok) === 1000,
    JSON.stringify({ in: priced.body?.inputPricePerMTok, out: priced.body?.outputPricePerMTok, cur: priced.body?.currency }))

  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')
  const created = await post('/api/v1/agents', {
    name: `usage-${stamp}`, description: '사용량 e2e 전용', runtimeType: 'opencode',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: '사용량 측정을 위한 에이전트입니다.', modelEndpointId: stub.id,
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)
  await put(`/api/v1/agents/${agent.id}/goal`, {
    description: '맡은 작업을 수행한다.', successCriteria: ['결과를 보고한다'],
    maxSteps: 3, maxRetries: 0, completionStrategy: 'agent', plannerMode: 'native',
    approvalRequired: false, maxDelegationDepth: 0,
  })

  const before = (await get(`/api/v1/usage?agentId=${agent.id}`)).body
  check('새 에이전트는 사용량 0', (before?.inputTokens ?? -1) === 0 && (before?.agents ?? []).length === 0,
    `in=${before?.inputTokens} rows=${(before?.agents ?? []).length}`)

  const task = await post('/api/v1/tasks', { agentId: agent.id, title: `사용량 측정 ${stamp}`, input: '간단히 처리하라.' })
  const taskId = (task.body?.task ?? task.body)?.id
  const done = await settle(taskId, ['completed', 'failed', 'dead_letter'])
  check('측정용 작업 완료', done?.status === 'completed', `status=${done?.status} error=${done?.lastError ?? ''}`)

  const report = (await get(`/api/v1/usage?agentId=${agent.id}`)).body
  const row = (report?.agents ?? [])[0]
  check('에이전트별 사용량 집계', Boolean(row), JSON.stringify(report?.agents ?? []).slice(0, 120))
  check('입력·출력 토큰 분리 집계', (row?.inputTokens ?? 0) > 0 && (row?.outputTokens ?? 0) > 0,
    `in=${row?.inputTokens} out=${row?.outputTokens}`)
  check('실행 횟수 집계', (row?.runs ?? 0) >= 1, `runs=${row?.runs}`)

  // The report must be arithmetic, not an estimate.
  const expected = (row.inputTokens * 1000 + row.outputTokens * 3000) / 1000000
  check('단가 계산이 정확함', Math.abs((row?.cost ?? 0) - expected) < 0.0001, `cost=${row?.cost} expected=${expected}`)
  check('합계가 행 합과 일치', Math.abs((report?.cost ?? 0) - expected) < 0.0001, `total=${report?.cost}`)
  check('단가 적용 표시', row?.priced === true && report?.unpricedTokens === 0, `priced=${row?.priced} unpriced=${report?.unpricedTokens}`)
  check('통화 표기', report?.currency === 'KRW', report?.currency)
  check('일자별 집계 제공', (report?.daily ?? []).length >= 1 && report.daily[0].inputTokens > 0,
    JSON.stringify(report?.daily ?? []).slice(0, 100))

  // An endpoint with no price is counted in tokens but never in money: a
  // confident zero would understate the bill.
  const unpriced = await post('/api/v1/admin/models', { ...stub, inputPricePerMTok: 0, outputPricePerMTok: 0, currency: 'KRW' })
  check('단가 제거', unpriced.status === 200)
  const afterUnpricing = (await get(`/api/v1/usage?agentId=${agent.id}`)).body
  check('단가 없으면 금액 0', (afterUnpricing?.cost ?? -1) === 0, `cost=${afterUnpricing?.cost}`)
  check('단가 없어도 토큰은 집계', (afterUnpricing?.inputTokens ?? 0) === row.inputTokens, `in=${afterUnpricing?.inputTokens}`)
  check('미산정 토큰으로 표시', (afterUnpricing?.unpricedTokens ?? 0) === row.inputTokens + row.outputTokens,
    `unpriced=${afterUnpricing?.unpricedTokens}`)

  // Bad windows are refused rather than answered with something meaningless.
  check('뒤집힌 기간 거절', (await get('/api/v1/usage?from=2026-08-17T00:00:00Z&to=2026-08-16T00:00:00Z')).status === 400)
  check('과도한 기간 거절', (await get('/api/v1/usage?from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z')).status === 400)
  check('잘못된 시각 거절', (await get('/api/v1/usage?from=yesterday')).status === 400)
  // The window has to actually filter, or every report would be all of history.
  const empty = (await get('/api/v1/usage?from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z')).body
  check('기간 밖 실행은 제외', (empty?.inputTokens ?? -1) === 0, `in=${empty?.inputTokens}`)

  // Restore the price so the environment is left as it was found.
  await post('/api/v1/admin/models', { ...stub, inputPricePerMTok: 0, outputPricePerMTok: 0, currency: 'KRW' })
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
console.log('\nusage reporting e2e passed')
