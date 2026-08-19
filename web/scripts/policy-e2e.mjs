// Verifies the central policy: what it refuses, what it explains, and that the
// order on screen is the order that decides.
//
// The controls this joins up were all real and all separate, so the risk is not
// that a rule fails to save — it is that a rule saves and quietly decides
// something other than what its author read.
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
  const simulate = async (request, document) => (await post('/api/v1/admin/policy/simulate', { request, document })).body

  const stamp = Date.now().toString(36)
  const agents = (await get('/api/v1/agents')).body?.items ?? []
  if (!agents.length) throw new Error('no agent to write a policy about')
  const agent = agents[0]
  const restore = (await get('/api/v1/admin/policy')).body?.document ?? { rules: [] }

  try {
    // An empty policy changes nothing, which is what makes it safe to introduce.
    check('빈 정책은 모두 허용', (await simulate({ action: 'task.create', role: 'user' }, { rules: [] }))?.effect === 'allow')

    // Validation refuses what would otherwise save and silently never apply.
    const invalid = [
      [{ rules: [{ id: '', effect: 'deny', reason: 'x' }] }, 'ID'],
      [{ rules: [{ id: 'a', effect: 'nope', reason: 'x' }] }, '효과'],
      [{ rules: [{ id: 'a', effect: 'deny', actions: ['tool.invoke'], reason: 'x' }] }, '동작'],
      [{ rules: [{ id: 'a', effect: 'deny' }] }, '사유'],
      [{ rules: [{ id: 'a', effect: 'allow' }, { id: 'A', effect: 'allow' }] }, '중복'],
    ]
    for (const [document, mentions] of invalid) {
      const result = await put('/api/v1/admin/policy', document)
      check(`잘못된 정책 거절 (${mentions})`, result.status === 400 && (result.body?.error?.message ?? '').includes(mentions),
        `HTTP ${result.status} ${result.body?.error?.message ?? ''}`)
    }

    // Order is the policy: a narrow allow above a broad deny is an exception.
    const ordered = {
      rules: [
        { id: `allow-oncall-${stamp}`, effect: 'allow', actions: ['tool.call'], users: ['oncall'] },
        { id: `deny-writes-${stamp}`, effect: 'deny', actions: ['tool.call'], tools: ['github/delete_*'], reason: '쓰기 도구는 금지' },
      ],
    }
    check('예외가 위에 있으면 예외가 이김',
      (await simulate({ action: 'tool.call', user: 'oncall', server: 'github', tool: 'delete_branch' }, ordered))?.effect === 'allow')
    const denied = await simulate({ action: 'tool.call', user: 'intern', server: 'github', tool: 'delete_branch' }, ordered)
    check('그 외에는 차단되고 규칙과 사유를 알려 줌',
      denied?.effect === 'deny' && denied.ruleId === `deny-writes-${stamp}` && /쓰기 도구/.test(denied.reason ?? ''),
      JSON.stringify(denied))
    const reversed = { rules: [ordered.rules[1], ordered.rules[0]] }
    check('순서를 뒤집으면 결과가 바뀜 — 순서가 곧 정책',
      (await simulate({ action: 'tool.call', user: 'oncall', server: 'github', tool: 'delete_branch' }, reversed))?.effect === 'deny')
    // A tool rule must not fire on a request that has no tool.
    check('도구 조건은 도구가 없는 요청에 걸리지 않음',
      (await simulate({ action: 'task.create', user: 'intern' }, ordered))?.effect === 'allow')
    // Server-qualified patterns stay on their server.
    check('서버로 한정한 패턴은 다른 서버에 번지지 않음',
      (await simulate({ action: 'tool.call', user: 'intern', server: 'gitlab', tool: 'delete_branch' }, ordered))?.effect === 'allow')

    // Enforcement, not just simulation.
    const freeze = {
      rules: [{
        id: `freeze-${stamp}`, effect: 'deny', actions: ['task.create', 'runtime.start'],
        agents: [agent.id], reason: `감사 기간 동결 ${stamp}`,
      }],
    }
    const saved = await put('/api/v1/admin/policy', freeze)
    check('정책 저장', saved.status === 200 && saved.body?.rules === 1, `HTTP ${saved.status}`)
    check('저장 결과가 런타임 적용까지 설명함', /런타임|규칙을 저장/.test(saved.body?.message ?? ''), saved.body?.message)

    const task = await post('/api/v1/tasks', { agentId: agent.id, title: `정책 ${stamp}`, input: 'x' })
    check('정책이 작업 생성을 차단', task.status === 403 && task.body?.error?.code === 'policy_denied',
      `HTTP ${task.status} ${task.body?.error?.code ?? ''}`)
    check('거절 메시지에 사유와 규칙 ID가 있음',
      (task.body?.error?.message ?? '').includes(stamp) && (task.body?.error?.message ?? '').includes(`freeze-${stamp}`),
      task.body?.error?.message)
    const spawn = await post(`/api/v1/agents/${agent.id}/spawn`, {})
    check('정책이 런타임 시작도 차단', spawn.status === 403, `HTTP ${spawn.status}`)

    // A policy nobody can prove was applied is a policy that gets argued about
    // after an incident rather than before one.
    const audit = (await get('/api/v1/admin/audit?action=policy.&limit=10')).body?.items ?? []
    check('차단이 감사 기록에 남음',
      audit.some((item) => item.action === 'policy.task.create' && item.outcome === 'denied' && item.resourceId === `freeze-${stamp}`),
      audit.map((i) => `${i.action}:${i.outcome}`).join(','))
    check('정책 변경도 감사 기록에 남음', audit.some((item) => item.action === 'policy.update'))

    // An explicit allow above the freeze reopens it, through the real endpoint.
    await put('/api/v1/admin/policy', {
      rules: [{ id: `unfreeze-${stamp}`, effect: 'allow', actions: ['task.create'], roles: ['admin'] }, ...freeze.rules],
    })
    const reopened = await post('/api/v1/tasks', { agentId: agent.id, title: `정책 해제 ${stamp}`, input: 'x' })
    check('위에 둔 허용 규칙이 실제 요청도 통과시킴', reopened.status === 202 || reopened.status === 201 || reopened.status === 409,
      `HTTP ${reopened.status} ${reopened.body?.error?.code ?? ''}`)

    // The console is where this is operated.
    await page.goto(`${baseURL}/admin/policy`, { waitUntil: 'networkidle' })
    await page.getByRole('heading', { name: '정책' }).first().waitFor({ timeout: 15000 })
    check('규칙 목록 표시', (await page.locator('tbody tr').count()) === 2, String(await page.locator('tbody tr').count()))
    check('효과 배지 표시', (await page.locator('.effect-badge').count()) >= 2)
    check('사용법 안내 제공', await page.getByText('정책은 이렇게 동작합니다').isVisible())
    // The simulator judges what is on screen, including unsaved edits.
    await page.locator('.ops-panel select').last().selectOption('admin')
    await page.getByRole('button', { name: '판정' }).click()
    await page.locator('.simulation').waitFor({ timeout: 10000 })
    check('시뮬레이터가 판정을 보여 줌', /허용|차단|승인/.test(await page.locator('.simulation strong').innerText()),
      await page.locator('.simulation').innerText())
  } finally {
    await put('/api/v1/admin/policy', restore)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\npolicy e2e passed')
