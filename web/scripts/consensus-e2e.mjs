// Verifies consensus workflows end to end.
//
// The mode was selectable long before it did anything: a consensus workflow ran
// as a chain and returned the last agent's answer. This run checks the three
// outcomes an operator has to be able to tell apart — unanimous, split majority
// and tie — and that the participants really answered independently.
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
  const provision = async (suffix, stance) => {
    const result = await post('/api/v1/agents', {
      name: `cns-${suffix}-${stamp}`, description: '합의 워크플로 e2e 전용', runtimeType: 'opencode',
      runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
      // The stub reads its stance from here, which is how one model backend can
      // produce a unanimous, split or tied tally.
      systemPrompt: `배포 판단을 검토합니다.\n표결 성향: ${stance}`,
      modelEndpointId: stub.id,
      securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
    })
    const agent = result.body?.agent ?? result.body
    if (!agent?.id) throw new Error(`agent ${suffix} not created: HTTP ${result.status} ${JSON.stringify(result.body)}`)
    created.push(agent)
    return agent
  }

  const rollbackA = await provision('rollback-a', '롤백')
  const rollbackB = await provision('rollback-b', '롤백')
  const hold = await provision('hold', '유지')
  const silent = await provision('silent', '무응답')

  const workflows = []
  const runConsensus = async (label, agents) => {
    const saved = await post('/api/v1/workflows', {
      name: `합의 ${label} ${stamp}`, description: '합의 e2e', mode: 'consensus',
      maxDepth: 5, maxAgentCalls: 20, maxToolCalls: 10, maxDurationSeconds: 300, maxParallelAgents: 5,
      // Saved as a chain on purpose: that is how the console wired consensus
      // workflows before the mode meant anything, and they must still vote.
      definition: { steps: agents.map((agent, index) => ({ id: `step-${index + 1}`, agentId: agent.id, dependsOn: index === 0 ? [] : [`step-${index}`] })) },
      enabled: true,
    })
    const workflow = saved.body?.workflow ?? saved.body
    if (!workflow?.id) throw new Error(`workflow ${label} not saved: HTTP ${saved.status} ${JSON.stringify(saved.body)}`)
    workflows.push(workflow)
    const run = await post(`/api/v1/workflows/${workflow.id}/run`, { input: '지금 배포를 롤백해야 하는가?' })
    return run.body?.result ?? run.body
  }

  // --- unanimous -----------------------------------------------------------
  const unanimous = await runConsensus('만장일치', [rollbackA, rollbackB])
  check('만장일치 판정', unanimous?.consensus?.unanimous === true, JSON.stringify(unanimous?.consensus ?? unanimous).slice(0, 160))
  check('만장일치 득표수', unanimous?.consensus?.agreed === 2 && unanimous?.consensus?.total === 2,
    `${unanimous?.consensus?.agreed}/${unanimous?.consensus?.total}`)
  check('결론이 결과 본문에 표시', (unanimous?.output ?? '').includes('만장일치') && (unanimous.output ?? '').includes('롤백'))
  // A chain would have run in two levels; independent voting is one.
  check('참여자가 독립적으로 실행', (unanimous?.levels ?? []).length === 1 && unanimous.levels[0].length === 2,
    JSON.stringify(unanimous?.levels))
  const sawOthers = (unanimous?.steps ?? []).some((step) => (step.output ?? '').includes('이전 단계 결과'))
  check('서로의 답을 보지 않음', !sawOthers)

  // --- split majority ------------------------------------------------------
  const majority = await runConsensus('다수결', [rollbackA, rollbackB, hold])
  check('다수결 판정', majority?.consensus?.tie === false && majority?.consensus?.unanimous === false,
    JSON.stringify(majority?.consensus).slice(0, 160))
  check('다수 의견 채택', majority?.consensus?.winner === '롤백' && majority?.consensus?.agreed === 2,
    `${majority?.consensus?.winner} ${majority?.consensus?.agreed}/${majority?.consensus?.total}`)
  check('소수 의견도 기록', (majority?.consensus?.votes ?? []).some((vote) => vote.choice === '유지'))

  // --- tie -----------------------------------------------------------------
  const tie = await runConsensus('동률', [rollbackA, hold])
  check('동률 판정', tie?.consensus?.tie === true, JSON.stringify(tie?.consensus).slice(0, 160))
  check('동률은 합의로 표시하지 않음', !(tie?.output ?? '').includes('만장일치') && (tie?.output ?? '').includes('동률'))

  // --- abstention ----------------------------------------------------------
  const abstained = await runConsensus('기권', [rollbackA, silent])
  check('기권은 표로 세지 않음', abstained?.consensus?.total === 1 && abstained?.consensus?.agreed === 1,
    `${abstained?.consensus?.agreed}/${abstained?.consensus?.total}`)
  check('기권 사실은 기록', (abstained?.consensus?.votes ?? []).some((vote) => vote.abstained === true))

  // --- other modes are untouched -------------------------------------------
  const chain = await post('/api/v1/workflows', {
    name: `순차 ${stamp}`, description: '대조군', mode: 'sequential',
    maxDepth: 5, maxAgentCalls: 20, maxToolCalls: 10, maxDurationSeconds: 300, maxParallelAgents: 5,
    definition: { steps: [{ id: 'step-1', agentId: rollbackA.id, dependsOn: [] }, { id: 'step-2', agentId: hold.id, dependsOn: ['step-1'] }] },
    enabled: true,
  })
  const chainWorkflow = chain.body?.workflow ?? chain.body
  workflows.push(chainWorkflow)
  const chainRun = (await post(`/api/v1/workflows/${chainWorkflow.id}/run`, { input: '질문' })).body?.result
  check('다른 모드는 표결하지 않음', !chainRun?.consensus, JSON.stringify(chainRun?.consensus))
  check('다른 모드는 그래프를 유지', (chainRun?.levels ?? []).length === 2, JSON.stringify(chainRun?.levels))

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
console.log('\nconsensus workflow e2e passed')
