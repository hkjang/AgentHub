// Verifies that an agent definition survives a round trip through YAML.
//
// The point of the export is that a definition can be reviewed in a repository
// and applied to another cluster, so this run checks the document names its
// references rather than carrying local identifiers, that re-importing updates
// instead of duplicating, and that a reference the cluster does not have is
// refused by name rather than silently dropped.
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

  const call = (method, path, body, contentType) =>
    page.evaluate(async ([method, path, body, contentType]) => {
      const csrf = document.cookie.split('; ').find((c) => c.startsWith('agenthub_csrf='))
      const headers = {}
      if (contentType) headers['Content-Type'] = contentType
      if (csrf) headers['X-CSRF-Token'] = decodeURIComponent(csrf.split('=').slice(1).join('='))
      const response = await fetch(path, { method, credentials: 'include', headers, body: body === null ? undefined : body })
      const text = await response.text()
      let parsed = null
      try { parsed = text ? JSON.parse(text) : null } catch { parsed = null }
      return { status: response.status, body: parsed, text, disposition: response.headers.get('content-disposition') ?? '' }
    }, [method, path, body ?? null, contentType ?? null])

  const get = (path) => call('GET', path)
  const postJSON = (path, value) => call('POST', path, JSON.stringify(value), 'application/json')
  const postYAML = (path, value) => call('POST', path, value, 'application/yaml')
  const del = (path) => call('DELETE', path)

  const stamp = Date.now().toString(36)
  const models = (await get('/api/v1/models')).body?.items ?? []
  const stub = models.find((model) => model.baseUrl?.includes('model-stub')) ?? models[0]
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample || !stub) throw new Error('need an existing agent and a model endpoint to copy references from')
  const workspaces = (await get('/api/v1/workspaces')).body?.items ?? []
  const profiles = (await get('/api/v1/runtime-profiles')).body?.items ?? []
  const workspaceName = workspaces.find((item) => item.id === sample.workspaceId)?.name
  const profileName = profiles.find((item) => item.id === sample.runtimeProfileId)?.name

  const created = await postJSON('/api/v1/agents', {
    name: `gitops-${stamp}`, description: 'GitOps e2e 전용', runtimeType: 'opencode',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: '내보내기 검증용 에이전트입니다.', modelEndpointId: stub.id,
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)

  const exported = await get(`/api/v1/agents/${agent.id}/export`)
  check('정의 내보내기', exported.status === 200, `HTTP ${exported.status}`)
  const yaml = exported.text
  check('다운로드 파일명 지정', exported.disposition.includes(`gitops-${stamp}`), exported.disposition)
  check('문서 종류 표기', yaml.includes('kind: AgentDefinition') && yaml.includes('apiVersion: agenthub.io/v1alpha1'),
    yaml.split('\n').slice(0, 2).join(' '))
  check('참조를 이름으로 내보냄', Boolean(workspaceName) && yaml.includes(workspaceName) && yaml.includes(stub.name),
    `workspace=${workspaceName} model=${stub.name}`)
  check('로컬 식별자를 포함하지 않음', !yaml.includes(agent.id) && !yaml.includes(stub.id), yaml.slice(0, 160).replace(/\n/g, ' '))
  check('프로파일 이름 포함', Boolean(profileName) && yaml.includes(profileName), String(profileName))
  check('시스템 프롬프트 보존', yaml.includes('내보내기 검증용'))

  // Re-importing the same document must update, not duplicate: a GitOps flow
  // re-applies the file on every change.
  const reimported = await postYAML('/api/v1/agents/import', yaml)
  check('같은 문서 재적용은 갱신', reimported.status === 200 && reimported.body?.mode === 'updated',
    `HTTP ${reimported.status} mode=${reimported.body?.mode}`)
  const sameName = ((await get('/api/v1/agents')).body?.items ?? []).filter((item) => item.name === `gitops-${stamp}`)
  check('중복 생성되지 않음', sameName.length === 1, `count=${sameName.length}`)

  // A renamed document creates a new agent, which is how a definition is
  // promoted into a cluster that has never seen it.
  const renamedYaml = yaml.replace(`gitops-${stamp}`, `gitops-copy-${stamp}`)
  const copied = await postYAML('/api/v1/agents/import', renamedYaml)
  check('새 이름은 새 에이전트로 생성', copied.status === 201 && copied.body?.mode === 'created',
    `HTTP ${copied.status} mode=${copied.body?.mode}`)
  const copy = copied.body?.agent
  check('가져온 정의의 참조가 연결됨', copy?.workspaceId === sample.workspaceId && copy?.modelEndpointId === stub.id,
    `workspace=${copy?.workspaceId} model=${copy?.modelEndpointId}`)

  // A reference this cluster does not have must be named, not dropped.
  const dangling = renamedYaml.replace(stub.name, '존재하지-않는-모델')
  const rejected = await postYAML('/api/v1/agents/import', dangling.replace(`gitops-copy-${stamp}`, `gitops-bad-${stamp}`))
  check('없는 참조는 이름과 함께 거절', rejected.status === 400 && (rejected.body?.error?.message ?? '').includes('존재하지-않는-모델'),
    `HTTP ${rejected.status} ${rejected.body?.error?.message ?? ''}`)
  check('거절된 정의는 생성되지 않음',
    ((await get('/api/v1/agents')).body?.items ?? []).every((item) => item.name !== `gitops-bad-${stamp}`))

  check('잘못된 YAML 거절', (await postYAML('/api/v1/agents/import', 'kind: [oops')).status === 400)
  check('다른 kind 거절', (await postYAML('/api/v1/agents/import', 'kind: Deployment\nmetadata:\n  name: x\n')).status === 400)
  check('이름 없는 정의 거절', (await postYAML('/api/v1/agents/import', 'kind: AgentDefinition\nspec:\n  runtimeType: opencode\n')).status === 400)
  check('알 수 없는 런타임 유형 거절',
    (await postYAML('/api/v1/agents/import', `kind: AgentDefinition\nmetadata:\n  name: bad-${stamp}\nspec:\n  runtimeType: nope\n`)).status === 400)

  // The runtime type shapes the Pod and the storage, so it stays immutable even
  // when a document says otherwise.
  const retyped = yaml.replace('runtimeType: opencode', 'runtimeType: hermes')
  check('런타임 유형 변경 시도 거절', (await postYAML('/api/v1/agents/import', retyped)).status === 400)

  for (const id of [copy?.id, agent.id].filter(Boolean)) {
    const removed = await del(`/api/v1/agents/${id}`)
    check(`정리: ${id.slice(0, 8)} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  problems.forEach((problem) => console.error(` - ${problem}`))
  process.exit(1)
}
console.log('\nGitOps definition e2e passed')
