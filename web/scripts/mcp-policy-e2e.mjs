// Verifies the MCP tool policy where it has to hold: inside a running Pod.
//
// The agent's generated configuration must point at the in-Pod gateway rather
// than the MCP server, the gateway must hide and refuse the tools the policy
// forbids, and it must still pass the permitted ones through to the real
// upstream. Anything less is a policy the agent process could route around.
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
const kubectl = (...args) => execFileSync('kubectl', args, { encoding: 'utf8', maxBuffer: 32 << 20 })

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

  const bundles = (await get('/api/v1/mcp/bundles')).body?.items ?? (await get('/api/v1/mcp-bundles')).body?.items ?? []
  const bundle = bundles.find((item) => (item.serverIds ?? []).length > 0)
  if (!bundle) throw new Error('no MCP bundle with servers to bind')
  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')

  const stamp = Date.now().toString(36)
  const created = await post('/api/v1/agents', {
    name: `mcp-policy-${stamp}`, description: 'MCP 도구 정책 e2e 전용', runtimeType: 'opencode',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: 'MCP 도구 정책 검증용 에이전트입니다.', mcpBundleId: bundle.id,
    modelEndpointId: sample.modelEndpointId ?? '',
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)
  console.log(`agent=${agent.name} bundle=${bundle.name}`)

  const listing = await get(`/api/v1/agents/${agent.id}/mcp-policies`)
  const servers = listing.body?.servers ?? []
  check('바인딩된 MCP 서버 조회', servers.length > 0, `servers=${servers.length}`)
  const server = servers[0]

  check('바인딩되지 않은 서버 거절', (await put(`/api/v1/agents/${agent.id}/mcp-policies`, {
    serverId: '00000000-0000-0000-0000-000000000000', mode: 'allow', tools: ['x'],
  })).status === 400)
  check('잘못된 모드 거절', (await put(`/api/v1/agents/${agent.id}/mcp-policies`, {
    serverId: server.id, mode: 'everything', tools: [],
  })).status === 400)

  const saved = await put(`/api/v1/agents/${agent.id}/mcp-policies`, {
    serverId: server.id, mode: 'allow', tools: ['resolve-library-id'],
  })
  check('도구 정책 저장', saved.status === 200, `HTTP ${saved.status}`)
  const reloaded = (await get(`/api/v1/agents/${agent.id}/mcp-policies`)).body?.items ?? []
  check('저장된 정책 조회', reloaded[0]?.mode === 'allow' && reloaded[0]?.tools.join() === 'resolve-library-id',
    JSON.stringify(reloaded[0]?.tools))

  const spawned = await post(`/api/v1/agents/${agent.id}/spawn`)
  check('Runtime 기동 요청', spawned.status < 300, `HTTP ${spawned.status}`)
  const runtimeId = (spawned.body?.runtime ?? spawned.body)?.id
  const crdName = (spawned.body?.runtime ?? spawned.body)?.crdName

  // Wait for the Pod, not just the API record: the policy lives in the Pod.
  const pod = `${crdName}-0`
  let ready = false
  for (let attempt = 0; attempt < 60 && !ready; attempt++) {
    try {
      const phase = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.status.phase}').trim()
      const containers = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.status.containerStatuses[*].ready}').trim()
      ready = phase === 'Running' && containers.length > 0 && !containers.includes('false')
    } catch { /* not created yet */ }
    if (!ready) await page.waitForTimeout(5000)
  }
  check('Runtime Pod 기동', ready, pod)
  if (!ready) throw new Error(`pod ${pod} never became ready`)

  const containers = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.spec.containers[*].name}').split(/\s+/)
  check('MCP 게이트웨이 사이드카 주입', containers.includes('agenthub-mcp-gateway'), containers.join(','))

  const config = kubectl('-n', namespace, 'exec', pod, '-c', 'agent', '--', 'cat', '/etc/agenthub/opencode.json')
  check('에이전트 설정이 게이트웨이를 가리킴', config.includes('127.0.0.1:9129/mcp/'), config.slice(0, 120).replace(/\s+/g, ' '))
  check('에이전트 설정에 실제 MCP 주소 없음', !config.includes('mcp.context7.com'))

  // Ask the gateway itself, from inside the Pod, exactly what the agent would.
  const gatewayCall = (payload) => {
    try {
      return kubectl('-n', namespace, 'exec', pod, '-c', 'agent', '--',
        'curl', '-s', '-X', 'POST', `http://127.0.0.1:9129/mcp/${server.name}`,
        '-H', 'Content-Type: application/json',
        '-H', 'Accept: application/json, text/event-stream',
        '-d', JSON.stringify(payload))
    } catch (e) {
      return `gateway call failed: ${e instanceof Error ? e.message : e}`
    }
  }

  const denied = gatewayCall({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'get-library-docs', arguments: {} } })
  check('허용 목록 밖 도구 차단', denied.includes('차단') && denied.includes('error'), denied.slice(0, 140).replace(/\s+/g, ' '))

  const listed = gatewayCall({ jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} })
  const advertises = (tool) => new RegExp(`"name"\\s*:\\s*"${tool}"`).test(listed)
  check('차단된 도구는 목록에도 없음', !advertises('get-library-docs'), listed.slice(0, 200).replace(/\s+/g, ' '))
  if (listed.includes('tools')) check('허용된 도구는 목록에 있음', advertises('resolve-library-id'), listed.slice(0, 200).replace(/\s+/g, ' '))
  else console.log('  note  MCP 서버에 도달하지 못해 도구 목록은 확인하지 못했습니다 (폐쇄망 환경).')

  check('정책 삭제', (await del(`/api/v1/mcp-policies/${reloaded[0].id}`)).status === 204)
  check('삭제 후 정책 없음', ((await get(`/api/v1/agents/${agent.id}/mcp-policies`)).body?.items ?? []).length === 0)

  if (runtimeId) await post(`/api/v1/runtimes/${runtimeId}/stop`)
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
console.log('\nMCP tool policy e2e passed')
