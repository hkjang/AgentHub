// Fills a deployment with demo data and photographs every screen the guides show.
//
// The guides in docs/ are only worth reading if their figures are this product.
// So there is no fixture directory and no mockup here: the script talks to a
// real control plane over the same REST API the console uses, then drives the
// console itself with a browser. Whatever the screen renders is what lands in
// docs/screenshots/guide/.
//
// The data is deliberately invented — 데모물산, 홍길동, example.com — because a
// figure is published and a real name, a real host or a real secret published in
// a PDF cannot be taken back.
//
// This script writes. It fills the deployment with demo agents and keys, and it
// replaces three settings that are global to the whole platform — the policy
// document, the content-inspection rules and the session gateway. So it refuses
// to guess its target: there is no default URL, the variables are its own rather
// than the AGENTHUB_TEST_* pair the e2e scripts share, and it will not start
// without being told in writing that the deployment is disposable. It also puts
// the three global settings back the way it found them on the way out.
//
//   AGENTHUB_GUIDE_URL=http://127.0.0.1:8080 \
//   AGENTHUB_GUIDE_USER=admin AGENTHUB_GUIDE_PASSWORD=… \
//   AGENTHUB_GUIDE_DISPOSABLE=yes \
//   node scripts/guide-shots.mjs
import { chromium } from 'playwright-core'
import { mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { chromiumPath } from './browser.mjs'

const baseURL = (process.env.AGENTHUB_GUIDE_URL ?? '').replace(/\/$/, '')
const username = process.env.AGENTHUB_GUIDE_USER ?? ''
const password = process.env.AGENTHUB_GUIDE_PASSWORD ?? ''
if (!baseURL || !username || !password) {
  console.error('AGENTHUB_GUIDE_URL, AGENTHUB_GUIDE_USER, AGENTHUB_GUIDE_PASSWORD 이 모두 필요합니다.')
  console.error('이 스크립트는 정책·내용 검사·세션 게이트웨이를 덮어씁니다. 대상을 짐작하지 않습니다.')
  process.exit(2)
}
if (process.env.AGENTHUB_GUIDE_DISPOSABLE !== 'yes') {
  console.error(`${baseURL} 의 전역 설정(정책·내용 검사·세션 게이트웨이)을 덮어씁니다.`)
  console.error('버려도 되는 배포가 맞으면 AGENTHUB_GUIDE_DISPOSABLE=yes 를 주고 다시 실행하세요.')
  process.exit(2)
}
const here = dirname(fileURLToPath(import.meta.url))
const shotDir = process.env.GUIDE_SHOT_DIR ?? join(here, '..', '..', 'docs', 'screenshots', 'guide')
mkdirSync(shotDir, { recursive: true })

const problems = []
const note = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}

const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true, args: ['--no-sandbox'] })
try {
  // 1440x900 is the desktop size the guide standard fixes. deviceScaleFactor 2
  // keeps the Korean text readable once the PDF scales the figure down.
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, deviceScaleFactor: 2, locale: 'ko-KR' })
  const page = await context.newPage()

  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill('')
  await shoot(page, 'login', '로그인 화면')

  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 30000 })

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
  const ok = (response) => response.status >= 200 && response.status < 300

  // The three settings seed() replaces are global to the platform, so they are
  // read first and put back on the way out — the same shape policy-e2e.mjs and
  // dlp-e2e.mjs use. Without it one run leaves the deployment holding this
  // script's demo policy instead of its own.
  const globals = [
    ['/api/v1/admin/policy', (body) => body?.document ?? { rules: [] }],
    ['/api/v1/admin/dlp', (body) => body?.document ?? body ?? {}],
    ['/api/v1/admin/settings/sessionGateway', (body) => ({ value: body?.value ?? body ?? null })],
  ]
  const before = []
  if (process.env.GUIDE_SKIP_SEED !== '1') {
    for (const [path, read] of globals) {
      const current = await get(path)
      if (!ok(current)) { note(`복원용 읽기 ${path}`, false, `HTTP ${current.status}`); continue }
      before.push([path, read(current.body)])
    }
  }

  try {
    // GUIDE_SKIP_SEED re-photographs a deployment that already holds the demo
    // data — reshooting after a console change should not need a second copy of
    // every agent and task.
    if (process.env.GUIDE_SKIP_SEED !== '1') await seed({ get, post, put, ok })
    await capture(page)
  } finally {
    for (const [path, value] of before) {
      const restored = await put(path, value)
      note(`복원 ${path}`, ok(restored), `HTTP ${restored.status}`)
    }
  }

  if (problems.length) {
    console.log(`\n${problems.length}건이 계획대로 되지 않았습니다:`)
    for (const problem of problems) console.log(`  - ${problem}`)
    process.exitCode = 1
  } else {
    console.log(`\n캡처 완료 — ${shotDir}`)
  }
} finally {
  await browser.close()
}

/** shoot waits for the screen to settle and writes one figure. */
async function shoot(page, name, label) {
  // A spinner frozen into a figure is the one thing the standard calls out, so
  // the wait is for the network to go quiet and then for a paint to land.
  await page.waitForLoadState('networkidle').catch(() => {})
  await page.waitForTimeout(700)
  await page.screenshot({ path: join(shotDir, `${name}.png`) })
  note(`${label} → ${name}.png`, true)
}

/** visit opens a console route and photographs it. */
async function visit(page, route, name, label, prepare) {
  await page.goto(`${baseURL}${route}`, { waitUntil: 'networkidle' })
  if (prepare) {
    try {
      await prepare(page)
    } catch (error) {
      note(`${label} 준비`, false, String(error).slice(0, 160))
    }
  }
  await shoot(page, name, label)
}

/** seed writes the demo deployment every figure is taken against. */
async function seed({ get, post, put, ok }) {
  const models = [
    { name: '사내 LLM 게이트웨이', provider: 'openai', baseUrl: 'https://llm.example.internal/v1', defaultModel: 'gpt-oss-120b', enabled: true, inputPricePerMTok: 900, outputPricePerMTok: 2700, currency: 'KRW' },
    { name: '요약 전용 소형 모델', provider: 'openai', baseUrl: 'https://llm.example.internal/v1', defaultModel: 'qwen3-8b', enabled: true, inputPricePerMTok: 120, outputPricePerMTok: 360, currency: 'KRW' },
  ]
  const modelIds = []
  for (const model of models) {
    const created = await post('/api/v1/admin/models', model)
    note(`모델 등록 ${model.name}`, ok(created), `HTTP ${created.status}`)
    if (created.body?.id) modelIds.push(created.body.id)
  }

  const servers = [
    { name: '사내 위키 검색', description: '규정·설계 문서를 찾아 읽습니다', mode: 'shared', transport: 'http', endpoint: 'https://wiki.example.internal/mcp', riskLevel: 'low', enabled: true, authType: 'bearer' },
    { name: '사내 Git', description: '저장소 조회와 브랜치 생성', mode: 'shared', transport: 'http', endpoint: 'https://git.example.internal/mcp', riskLevel: 'medium', enabled: true, authType: 'bearer', approvalRequired: true },
    { name: '이슈 트래커', description: '이슈 조회와 코멘트 작성', mode: 'shared', transport: 'http', endpoint: 'https://issues.example.internal/mcp', riskLevel: 'medium', enabled: true, authType: 'header', authHeader: 'X-Issue-Token' },
  ]
  const serverIds = []
  for (const server of servers) {
    const created = await post('/api/v1/admin/mcp-servers', server)
    note(`MCP 서버 등록 ${server.name}`, ok(created), `HTTP ${created.status}`)
    if (created.body?.id) serverIds.push(created.body.id)
  }
  for (const bundle of [
    { name: '개발 지원 팩', description: '코드를 다루는 에이전트에게 붙이는 묶음', serverIds: serverIds.slice(0, 2), enabled: true },
    { name: '조사 팩', description: '문서를 찾아 읽는 에이전트에게 붙이는 묶음', serverIds: serverIds.slice(0, 1).concat(serverIds.slice(2)), enabled: true },
  ]) {
    const created = await post('/api/v1/admin/mcp-bundles', bundle)
    note(`MCP 번들 등록 ${bundle.name}`, ok(created), `HTTP ${created.status}`)
  }
  const bundleIds = ((await get('/api/v1/mcp-bundles')).body?.items ?? []).map((item) => item.id)

  for (const department of [
    { name: '플랫폼팀', description: '플랫폼 운영' },
    { name: '데이터팀', description: '데이터 분석' },
  ]) {
    const created = await post('/api/v1/admin/departments', department)
    note(`부서 등록 ${department.name}`, ok(created), `HTTP ${created.status}`)
  }

  // Accounts are not seeded here on purpose: the platform only ever creates a
  // user at bootstrap or at the first SSO sign-in, and a capture script that
  // invented a fourth way would be photographing a product that does not exist.
  // A demo deployment gets its extra members by signing them in through SSO.
  const members = (await get('/api/v1/admin/users')).body?.items ?? []
  note('사용자 목록에 사람이 있음', members.length > 1, `${members.length}명`)

  const workspaces = [
    { name: '결제서비스-정비', type: 'empty', sizeGb: 20 },
    { name: '사내규정-조사', type: 'empty', sizeGb: 10 },
    { name: '월간보고-데이터', type: 'empty', sizeGb: 10 },
  ]
  const workspaceIds = []
  for (const workspace of workspaces) {
    const created = await post('/api/v1/workspaces', workspace)
    note(`작업공간 생성 ${workspace.name}`, ok(created), `HTTP ${created.status}`)
    if (created.body?.id) workspaceIds.push(created.body.id)
  }
  if (workspaceIds[0]) {
    const snapshot = await post(`/api/v1/workspaces/${workspaceIds[0]}/snapshots`, { name: '정비-시작전' })
    note('스냅샷 생성', ok(snapshot), `HTTP ${snapshot.status}`)
  }

  const agents = [
    { name: '결제서비스 코드 정비', description: '테스트를 고치고 의존성을 정리합니다', runtimeType: 'opencode', systemPrompt: '결제 서비스 저장소에서 실패한 테스트를 고칩니다. 배포는 하지 않습니다.', workspaceId: workspaceIds[0], mcpBundleId: bundleIds[0] },
    { name: '사내규정 조사', description: '규정 문서를 찾아 근거와 함께 정리합니다', runtimeType: 'hermes', systemPrompt: '사내 규정 문서를 찾아 근거 문서 이름과 함께 답합니다.', workspaceId: workspaceIds[1], mcpBundleId: bundleIds[1] },
    { name: '월간 지표 정리', description: '월말 지표를 표로 정리합니다', runtimeType: 'qwencode', systemPrompt: '주어진 데이터로 월간 지표 표를 만듭니다.', workspaceId: workspaceIds[2] },
  ]
  const agentIds = []
  for (const [index, agent] of agents.entries()) {
    const created = await post('/api/v1/agents', {
      ...agent,
      runtimeProfileId: 'rp-basic',
      securityProfileId: 'sp-restricted',
      networkProfileId: 'np-restricted',
      modelEndpointId: modelIds[index === 2 ? 1 : 0] ?? '',
    })
    note(`에이전트 생성 ${agent.name}`, ok(created), `HTTP ${created.status} ${created.body?.error?.message ?? ''}`)
    const id = created.body?.agent?.id ?? created.body?.id
    if (id) agentIds.push(id)
  }

  for (const [index, workflow] of [
    { name: '조사 후 정리', description: '조사한 내용을 코드 담당이 이어받습니다', mode: 'sequential' },
    { name: '두 관점 동시 검토', description: '같은 요청을 두 에이전트가 각자 답합니다', mode: 'parallel' },
  ].entries()) {
    const steps = agentIds.slice(0, 2).map((agentId, step) => ({
      id: step === 0 ? 'first' : 'second',
      agentId,
      dependsOn: workflow.mode === 'sequential' && step === 1 ? ['first'] : [],
    }))
    const created = await post('/api/v1/workflows', {
      ...workflow, maxDepth: 4, maxAgentCalls: 12, maxToolCalls: 50, maxDurationSeconds: 900, maxParallelAgents: 3,
      definition: { steps }, enabled: true,
    })
    note(`워크플로 생성 ${workflow.name}`, ok(created), `HTTP ${created.status} ${created.body?.error?.message ?? ''}`)
    void index
  }

  for (const [index, task] of [
    { title: '결제 테스트 3건 수정', input: 'payments 모듈의 실패한 테스트를 고쳐 주세요.' },
    { title: '데이터 보관 기간 규정 정리', input: '사내 데이터 보관 기간 규정을 근거 문서와 함께 정리해 주세요.' },
    { title: '9월 처리량 표 만들기', input: '9월 일자별 처리량을 표로 만들어 주세요.' },
  ].entries()) {
    const agentId = agentIds[index % Math.max(agentIds.length, 1)]
    if (!agentId) break
    const created = await post('/api/v1/tasks', { agentId, title: task.title, input: task.input })
    note(`작업 등록 ${task.title}`, ok(created), `HTTP ${created.status} ${created.body?.error?.message ?? ''}`)
  }

  const testSet = await post('/api/v1/evaluation/test-sets', {
    name: '배포 전 필수 점검', description: '에이전트가 운영에 나가기 전에 갖춰야 할 것', passThreshold: 100,
    cases: [
      { name: '런타임 지정', expectedRuntime: 'opencode' },
      { name: '프로파일 연결', requiresProfile: true },
      { name: '작업공간 연결', requiresWorkspace: true },
      { name: '보안 프로파일', requiresSecurity: true },
    ],
  })
  note('평가 세트 생성', ok(testSet), `HTTP ${testSet.status}`)
  if (testSet.body?.id && agentIds[0]) {
    const evaluated = await post(`/api/v1/agents/${agentIds[0]}/evaluate`, { testSetId: testSet.body.id })
    note('평가 실행', ok(evaluated), `HTTP ${evaluated.status}`)
  }

  for (const secret of [
    { name: '사내 Git 토큰', kind: 'api_key', value: 'demo-not-a-real-token-0001' },
    { name: '이슈 트래커 토큰', kind: 'api_key', value: 'demo-not-a-real-token-0002' },
  ]) {
    const created = await post('/api/v1/secrets', secret)
    note(`시크릿 등록 ${secret.name}`, ok(created), `HTTP ${created.status}`)
  }
  for (const key of [
    { name: 'IDE 연동 (읽기)', scopes: ['api:read', 'mcp:read'] },
    { name: '야간 배치', scopes: ['agent:write'] },
  ]) {
    const created = await post('/api/v1/api-keys', key)
    note(`API 키 발급 ${key.name}`, ok(created), `HTTP ${created.status}`)
  }

  const policy = await put('/api/v1/admin/policy', {
    rules: [
      { id: 'no-rrn-to-model', effect: 'deny', actions: ['model.call'], dataClasses: ['rrn'], reason: '주민등록번호는 모델로 보내지 않습니다.' },
      { id: 'git-write-needs-review', effect: 'require_approval', actions: ['tool.call'], servers: ['사내 Git'], reason: '저장소를 바꾸는 호출은 담당자가 확인합니다.' },
      { id: 'contractor-no-export', effect: 'deny', actions: ['decision.export'], roles: ['user'], reason: '결정 기록 외부 전송은 관리자만 합니다.' },
    ],
  })
  note('정책 저장', ok(policy), `HTTP ${policy.status} ${policy.body?.error?.message ?? ''}`)

  const dlp = await put('/api/v1/admin/dlp', { enabled: true, classes: { rrn: 'block', card: 'block', phone: 'redact', email: 'audit' } })
  note('내용 검사 설정', ok(dlp), `HTTP ${dlp.status} ${dlp.body?.error?.message ?? ''}`)

  const gateway = await put('/api/v1/admin/settings/sessionGateway', { value: { enabled: true, scheme: 'https', baseDomain: 'agents.example.internal', sessionHours: 8 } })
  note('세션 게이트웨이 설정', ok(gateway), `HTTP ${gateway.status}`)
}

/** capture photographs every screen the two guides refer to. */
async function capture(page) {
  const screens = [
    ['/', 'dashboard', '대시보드'],
    ['/catalog', 'catalog', '카탈로그'],
    ['/agents', 'agents', '내 에이전트'],
    ['/agents/builder', 'agent-builder', '에이전트 빌더'],
    ['/workspaces', 'workspaces', '작업공간'],
    ['/workspaces/snapshots', 'workspaces-snapshots', '스냅샷'],
    ['/tasks', 'tasks', '작업 대기열'],
    ['/runs', 'runs', '실행 기록'],
    ['/workflows', 'workflows', '워크플로'],
    ['/mcp/catalog', 'mcp-catalog', 'MCP 카탈로그'],
    ['/mcp/bundles', 'mcp-bundles', 'MCP 번들'],
    ['/evaluation', 'evaluation', '사전검증'],
    ['/sessions', 'sessions', '세션'],
    ['/reviews', 'reviews', '승인 대기'],
    ['/developer', 'developer', '개발자 도구'],
    ['/runtime', 'runtimes', '런타임'],
    ['/admin/overview', 'admin-overview', '관리자 · 운영 현황'],
    ['/admin/users', 'admin-users', '관리자 · 사용자와 팀'],
    ['/admin/operations', 'admin-operations', '관리자 · 로그와 감사'],
    ['/admin/models', 'admin-models', '관리자 · 모델'],
    ['/admin/mcp', 'admin-mcp', '관리자 · MCP 서버'],
    ['/admin/mcp-bundles', 'admin-mcp-bundles', '관리자 · MCP 번들'],
    ['/admin/policy', 'admin-policy', '관리자 · 정책'],
    ['/admin/dlp', 'admin-dlp', '관리자 · 내용 검사'],
    ['/admin/provenance', 'admin-provenance', '관리자 · 결정 기록'],
    ['/admin/quotas', 'admin-quotas', '관리자 · 할당량'],
    ['/admin/execution', 'admin-execution', '관리자 · 실행 제어'],
    ['/admin/runtime-profiles', 'admin-runtime-profiles', '관리자 · 런타임 프로파일'],
    ['/admin/runtime-images', 'admin-runtime-images', '관리자 · 런타임 이미지'],
    ['/admin/runtime-settings', 'admin-runtime-settings', '관리자 · 런타임 설정'],
    ['/admin/security', 'admin-security', '관리자 · 보안'],
    ['/admin/settings', 'admin-settings', '관리자 · 전역 설정'],
  ]
  for (const [route, name, label] of screens) {
    await visit(page, route, name, label)
  }
}
