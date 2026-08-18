// Verifies that the served API, the permissions it enforces and the OpenAPI
// description it publishes are the same thing.
//
// They used to be three: chi registered the path, a middleware guessed the
// API-key scope from substrings of the URL, and a hand-written list produced the
// description. This run issues one key per scope and checks the real answers
// against what the published description promises for the same endpoint.
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
  const withKey = (method, path, token) =>
    page.evaluate(async ([method, path, token]) => {
      const response = await fetch(path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' } })
      const text = await response.text()
      let parsed = null
      try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text } }
      return { status: response.status, code: parsed?.error?.code ?? '' }
    }, [method, path, token])

  const stamp = Date.now().toString(36)
  const spec = (await call('GET', '/api/openapi.json')).body
  const operations = Object.entries(spec?.paths ?? {}).flatMap(([path, item]) => Object.entries(item).map(([method, op]) => ({ path, method, op })))
  check('OpenAPI가 전체 표면을 기술함', operations.length > 100, `${operations.length} operations`)
  check('태그로 묶여 있음', (spec?.tags ?? []).length >= 10, (spec?.tags ?? []).map((t) => t.name).join(','))
  const documented = (path, method) => operations.find((o) => o.path === path && o.method === method)?.op
  const scopeOf = (path, method) => {
    const op = documented(path, method)
    const bearer = (op?.security ?? []).find((entry) => entry.bearerAuth)
    return bearer ? bearer.bearerAuth[0] : 'browser'
  }
  check('경로 파라미터가 기술됨', (documented('/api/v1/tasks/{id}', 'get')?.parameters ?? []).some((p) => p.name === 'id' && p.in === 'path'))

  const scopes = (await call('GET', '/api/v1/api-scopes')).body?.items ?? []
  check('scope별 도달 범위 제공', scopes.length === 4 && scopes.every((s) => typeof s.routes === 'number'),
    scopes.map((s) => `${s.scope}:${s.routes}`).join(' '))
  check('mcp:read는 REST를 열지 않음', (scopes.find((s) => s.scope === 'mcp:read')?.routes ?? -1) === 0)

  const issue = async (scope) => {
    const created = await call('POST', '/api/v1/api-keys', { name: `surface-${scope}-${stamp}`, scopes: [scope] })
    if (!created.body?.token) throw new Error(`key not issued for ${scope}: HTTP ${created.status}`)
    return { id: created.body.id ?? created.body.key?.id, token: created.body.token }
  }
  const readKey = await issue('api:read')
  const writeKey = await issue('agent:write')
  const manageKey = await issue('runtime:manage')

  // The enforcement matrix. Each row is what the catalog declares, checked
  // against what the server actually answers.
  const matrix = [
    ['GET', '/api/v1/agents', readKey, 200],
    ['GET', '/api/v1/agents', writeKey, 403],
    // A read whose path contains "session" is a read. The old rule demanded the
    // scope for starting runtimes because of how the word is spelled.
    ['GET', '/api/v1/sessions', readKey, 200],
    ['POST', '/api/v1/tasks', readKey, 403],
    ['POST', '/api/v1/runtimes/none/start', writeKey, 403],
    ['POST', '/api/v1/runtimes/none/start', manageKey, 404],
    ['GET', '/api/v1/secrets', readKey, 403],
    ['GET', '/api/v1/api-keys', readKey, 403],
    ['GET', '/api/v1/admin/settings', readKey, 403],
  ]
  for (const [method, path, key, want] of matrix) {
    const result = await withKey(method, path, key.token)
    check(`${method} ${path} · ${key === readKey ? 'api:read' : key === writeKey ? 'agent:write' : 'runtime:manage'} → ${want}`,
      result.status === want, `HTTP ${result.status} ${result.code}`)
  }

  // Refusals say which of the two reasons applied, because they need different
  // fixes: one is a missing scope, the other can never be granted to a key.
  check('부족한 scope는 insufficient_scope',
    (await withKey('POST', '/api/v1/tasks', readKey.token)).code === 'insufficient_scope')
  check('브라우저 전용은 api_key_forbidden',
    (await withKey('GET', '/api/v1/secrets', readKey.token)).code === 'api_key_forbidden')

  // What is enforced is what is published.
  check('문서와 실제 권한이 일치 (읽기)', scopeOf('/api/v1/sessions', 'get') === 'api:read', scopeOf('/api/v1/sessions', 'get'))
  check('문서와 실제 권한이 일치 (런타임)', scopeOf('/api/v1/runtimes/{id}/start', 'post') === 'runtime:manage', scopeOf('/api/v1/runtimes/{id}/start', 'post'))
  check('문서와 실제 권한이 일치 (브라우저 전용)', scopeOf('/api/v1/secrets', 'get') === 'browser', scopeOf('/api/v1/secrets', 'get'))
  check('관리자 전용임을 명시', /관리자/.test(documented('/api/v1/admin/settings', 'get')?.description ?? ''),
    documented('/api/v1/admin/settings', 'get')?.description)

  // A path nobody declared is not served at all.
  check('선언되지 않은 경로는 404', (await call('GET', '/api/v1/not-a-route')).status === 404)

  // The console has to make the choice legible, since that is where keys are issued.
  await page.goto(`${baseURL}/developer`, { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: /API Keys/ }).click()
  await page.getByRole('button', { name: /API Key 추가/ }).click()
  const drawer = page.getByRole('dialog')
  await drawer.getByText('권한 범위').waitFor({ timeout: 10000 })
  check('scope를 여러 개 선택할 수 있음', (await drawer.locator('.scope-option input[type=checkbox]').count()) === 4,
    String(await drawer.locator('.scope-option input[type=checkbox]').count()))
  check('scope별 도달 범위 표시', await drawer.getByText(/REST 엔드포인트 \d+개/).first().isVisible())
  check('쓰기는 조회를 포함하지 않는다고 안내', await drawer.getByText(/쓰기 권한은 조회를 포함하지 않습니다/).isVisible())

  for (const key of [readKey, writeKey, manageKey]) {
    if (key.id) await call('DELETE', `/api/v1/api-keys/${key.id}`)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\napi surface e2e passed')
