// Verifies agent versioning and the promotion gate through the console.
//
// Saving an agent used to be the whole release process: the next scheduled run
// executed whatever the definition said at that moment, evaluated or not. This
// run edits an agent, checks that every save left a version behind, that a
// version with no passing evaluation cannot be promoted by an ordinary
// promotion, that the gate actually refuses a task queued against an unpromoted
// definition, and that restoring a previous version puts production back.
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
  const sample = ((await get('/api/v1/agents')).body?.items ?? [])[0]
  if (!sample) throw new Error('no agent to copy runtime settings from')
  const model = sample.modelEndpointId
  if (!model) throw new Error('no model endpoint bound to the sample agent')
  const define = (description, prompt) => ({
    name: `release-${stamp}`, description, runtimeType: sample.runtimeType,
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: prompt, modelEndpointId: model,
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })

  const created = await post('/api/v1/agents', define('릴리스 e2e 전용', 'v1 지시문'))
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)

  // A rollback target has to exist from the first save, not from the first edit.
  const first = (await get(`/api/v1/agents/${agent.id}/versions`)).body
  check('생성도 버전으로 남음', (first?.items ?? []).length === 1 && first.items[0].version === 1,
    JSON.stringify((first?.items ?? []).map((v) => v.version)))
  check('승격 게이트는 기본적으로 꺼져 있음', first?.release?.requirePromotion === false)

  await put(`/api/v1/agents/${agent.id}`, define('두 번째 정의', 'v2 지시문'))
  await put(`/api/v1/agents/${agent.id}`, define('세 번째 정의', 'v3 지시문'))
  const saved = (await get(`/api/v1/agents/${agent.id}/versions`)).body
  check('수정할 때마다 버전 보존', (saved?.items ?? []).map((v) => v.version).join(',') === '3,2,1',
    (saved?.items ?? []).map((v) => v.version).join(','))
  check('버전마다 그 시점 정의 보관', saved?.items?.find((v) => v.version === 1)?.systemPrompt === 'v1 지시문',
    saved?.items?.find((v) => v.version === 1)?.systemPrompt)

  // A promotion without a passing evaluation is refused; the administrator
  // override exists but has to say why.
  const bare = await post(`/api/v1/agents/${agent.id}/promote`, { version: 3 })
  check('사전검증 없는 승격 거절', bare.status === 409, `HTTP ${bare.status}`)
  const anonymous = await post(`/api/v1/agents/${agent.id}/promote`, { version: 3, force: true })
  check('사유 없는 강제 승격 거절', anonymous.status === 400 && anonymous.body?.error?.code === 'force_requires_note',
    anonymous.body?.error?.code)

  const testSet = await post('/api/v1/evaluation/test-sets', {
    name: `release-${stamp}`, description: '릴리스 e2e 전용', passThreshold: 50,
    cases: [{ name: '모델 엔드포인트 연결', requiresModel: true }],
  })
  const evaluated = await post(`/api/v1/agents/${agent.id}/evaluate`, { testSetId: testSet.body?.id })
  check('사전검증 결과가 버전에 기록됨', evaluated.body?.agentVersion === 3 && evaluated.body?.status === 'passed',
    `v${evaluated.body?.agentVersion} ${evaluated.body?.status}`)

  const promoted = await post(`/api/v1/agents/${agent.id}/promote`, { version: 3 })
  check('통과한 버전 승격', promoted.status === 200 && promoted.body?.promotedVersion === 3, `HTTP ${promoted.status}`)

  await post(`/api/v1/agents/${agent.id}/promote`, { requirePromotion: true })
  const allowed = await post('/api/v1/tasks', { agentId: agent.id, title: `승격본 ${stamp}`, input: '확인' })
  check('승격본은 그대로 실행됨', allowed.status === 202 || allowed.status === 201, `HTTP ${allowed.status}`)

  // The edit that would have run tonight, refused while it is still an edit.
  await put(`/api/v1/agents/${agent.id}`, define('검증되지 않은 편집', 'v4 지시문'))
  const refused = await post('/api/v1/tasks', { agentId: agent.id, title: `미승격 ${stamp}`, input: '확인' })
  check('미승격 정의는 큐에 들어가지 않음', refused.status === 409 && refused.body?.error?.code === 'promotion_required',
    `HTTP ${refused.status} ${refused.body?.error?.code ?? ''}`)
  check('거절 사유가 조치를 안내함', /v4/.test(refused.body?.error?.message ?? '') && /v3/.test(refused.body?.error?.message ?? ''),
    refused.body?.error?.message)

  // Rolling back is a new version, not a rewound counter: runs already recorded
  // against v4 have to keep meaning what they meant.
  const restored = await post(`/api/v1/agents/${agent.id}/versions/3/restore`)
  check('이전 정의 복원', restored.status === 200 && restored.body?.agent?.version === 5, `v${restored.body?.agent?.version}`)
  const after = (await get(`/api/v1/agents/${agent.id}/versions`)).body
  check('복원본이 즉시 운영 승격됨', after?.release?.promotedVersion === 5, `promoted=${after?.release?.promotedVersion}`)
  check('복원본은 승격본과 같은 정의', after?.items?.find((v) => v.version === 5)?.systemPrompt === 'v3 지시문',
    after?.items?.find((v) => v.version === 5)?.systemPrompt)
  const reopened = await post('/api/v1/tasks', { agentId: agent.id, title: `복원 후 ${stamp}`, input: '확인' })
  check('복원 후 다시 실행 가능', reopened.status === 202 || reopened.status === 201, `HTTP ${reopened.status}`)

  // The console has to show all of this, since that is where it is operated.
  await page.goto(`${baseURL}/agents`, { waitUntil: 'networkidle' })
  await page.getByRole('row', { name: new RegExp(`release-${stamp}`) }).getByTitle('버전 · 운영 승격').click()
  const drawer = page.getByRole('dialog').filter({ hasText: '버전 기록' })
  await drawer.waitFor({ timeout: 15000 })
  check('버전 목록 표시', (await drawer.locator('.version-list li').count()) === 5,
    String(await drawer.locator('.version-list li').count()))
  check('운영 승격 표시', await drawer.getByText('운영 승격', { exact: true }).first().isVisible())
  check('사전검증 결과 표시', await drawer.getByText(/사전검증 통과/).first().isVisible())
  check('게이트 상태 표시', await drawer.getByText('승격된 정의만 실행').isVisible())
  check('사용 안내 제공', await drawer.getByText('버전과 운영 승격은 이렇게 씁니다').isVisible())

  const removed = await del(`/api/v1/agents/${agent.id}`)
  check(`정리: release-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  await del(`/api/v1/evaluation/test-sets/${testSet.body?.id}`)
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nagent release e2e passed')
