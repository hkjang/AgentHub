// Verifies supervised workflows end to end.
//
// The mode used to make its "supervisor" a last speaker: it could say a
// specialist's work was inadequate but had no way to have it fixed, and the
// result never recorded whether anything was approved. This run checks that a
// named specialist is actually sent back to work with the feedback, that the
// revised answer is what survives into the result, and that the approval is
// recorded rather than assumed.
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
  const del = (path) => call('DELETE', path)

  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub'))
  if (!stub) throw new Error('stub model endpoint not found — deploy model-stub first')
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')

  const stamp = Date.now().toString(36)
  const created = []
  const provision = async (suffix, prompt) => {
    const result = await post('/api/v1/agents', {
      name: `sup-${suffix}-${stamp}`, description: '감독 워크플로 e2e 전용', runtimeType: 'opencode',
      runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
      systemPrompt: prompt, modelEndpointId: stub.id,
      securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
    })
    const agent = result.body?.agent ?? result.body
    if (!agent?.id) throw new Error(`agent ${suffix} not created: HTTP ${result.status} ${JSON.stringify(result.body)}`)
    created.push(agent)
    return agent
  }

  const analyst = await provision('analyst', '자료를 분석합니다.')
  const writer = await provision('writer', '보고서를 작성합니다.')
  // The stub reads which specialist to send back from the supervisor's prompt.
  const supervisor = await provision('lead', `보고서를 검토합니다.\n감독 대상: ${`sup-writer-${stamp}`}`)

  const workflows = []
  const runSupervised = async (label, steps) => {
    const saved = await post('/api/v1/workflows', {
      name: `감독 ${label} ${stamp}`, description: '감독 e2e', mode: 'supervisor',
      maxDepth: 5, maxAgentCalls: 20, maxToolCalls: 10, maxDurationSeconds: 300, maxParallelAgents: 5,
      definition: { steps }, enabled: true,
    })
    const workflow = saved.body?.workflow ?? saved.body
    if (!workflow?.id) throw new Error(`workflow ${label} not saved: HTTP ${saved.status} ${JSON.stringify(saved.body)}`)
    workflows.push(workflow)
    const run = await post(`/api/v1/workflows/${workflow.id}/run`, { input: '분기 실적 보고서를 작성하라.' })
    return run.body?.result ?? run.body
  }

  // Two specialists feeding one reviewer: the shape supervision needs.
  const result = await runSupervised('검토', [
    { id: 'step-1', agentId: analyst.id, dependsOn: [] },
    { id: 'step-2', agentId: writer.id, dependsOn: [] },
    { id: 'step-3', agentId: supervisor.id, dependsOn: ['step-1', 'step-2'] },
  ])

  check('감독 기록 생성', Boolean(result?.supervision), JSON.stringify(result?.supervision ?? result).slice(0, 160))
  check('감독자 식별', result?.supervision?.supervisor === supervisor.name, result?.supervision?.supervisor)
  const revisions = (result?.supervision?.rounds ?? []).flatMap((round) => round.revisions ?? [])
  check('보완 요청 기록', revisions.length >= 1, JSON.stringify(revisions).slice(0, 160))
  check('지목된 에이전트로 기록', revisions[0]?.agent?.includes('writer'), revisions[0]?.agent)
  check('요청 내용 보존', (revisions[0]?.request ?? '').includes('근거'), revisions[0]?.request)
  check('재검토 후 승인', result?.supervision?.approved === true && result?.supervision?.exhausted === false,
    `approved=${result?.supervision?.approved} exhausted=${result?.supervision?.exhausted}`)
  check('검토가 2회 이상 수행', (result?.supervision?.rounds ?? []).length >= 2, `rounds=${(result?.supervision?.rounds ?? []).length}`)

  // The revised answer, not the rejected one, is what the result carries.
  const writerStep = (result?.steps ?? []).find((step) => step.agentId === writer.id)
  check('개정된 답변이 기록에 남음', (writerStep?.output ?? '').includes('개정'), (writerStep?.output ?? '').slice(0, 60))
  check('결과 본문에 승인 표시', (result?.output ?? '').includes('승인'), (result?.output ?? '').slice(0, 80).replace(/\n/g, ' '))
  // The revision costs calls, and they have to be counted.
  check('재실행이 호출 수에 반영', (result?.agentCalls ?? 0) > 3, `agentCalls=${result?.agentCalls}`)

  // A graph with no single reviewer must fall back rather than promoting one.
  const flat = await runSupervised('검토자 없음', [
    { id: 'step-1', agentId: analyst.id, dependsOn: [] },
    { id: 'step-2', agentId: writer.id, dependsOn: [] },
  ])
  check('검토자가 없으면 감독하지 않음', !flat?.supervision, JSON.stringify(flat?.supervision))
  check('그래도 결과는 반환', (flat?.output ?? '').length > 0)

  for (const workflow of workflows) await del(`/api/v1/workflows/${workflow.id}`)
  for (const agent of created.reverse()) {
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
console.log('\nsupervisor workflow e2e passed')
