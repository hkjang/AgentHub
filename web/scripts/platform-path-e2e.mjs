// Walks the path the platform actually takes, for every runtime type it offers.
//
// This exists because of a bug that shipped three times. Every runtime was
// verified by hand — the image built, the agent spoke its protocol, a Pod came up
// under the restricted profile — but each of those Pods was written by hand with
// the image name in it. The one step nobody exercised was the platform choosing
// the image itself, and for three runtimes it chose the shared base image, which
// contains none of their binaries. They were released unable to start.
//
// So this walks it end to end and lets the platform make every decision:
// catalog template -> agent -> spawn -> the operator builds a Pod -> it becomes
// ready -> the session opens. It needs a cluster and the runtime images, so it is
// not part of the default suite:
//
//   AGENTHUB_PLATFORM_PATH_RUNTIMES=qwencode,goose node scripts/platform-path-e2e.mjs
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
// Which runtimes to walk. Every one of them starts a Pod with a large image, so
// a site running this chooses; the default is the three that ship their own
// agent and were the ones that broke.
const wanted = (process.env.AGENTHUB_PLATFORM_PATH_RUNTIMES ?? 'qwencode,goose,holmes').split(',').map((s) => s.trim()).filter(Boolean)
const readyTimeoutMs = Number(process.env.AGENTHUB_PLATFORM_PATH_TIMEOUT ?? 300000)
// A security profile to attach, for walking a runtime that needs a privilege —
// HolmesGPT with cluster read is the case this exists for.
const securityProfileId = process.env.AGENTHUB_PLATFORM_PATH_SECURITY_PROFILE ?? ''

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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

  const stamp = Date.now().toString(36)
  const templates = (await get('/api/v1/templates')).body?.items ?? []
  const workspace = ((await get('/api/v1/workspaces')).body?.items ?? []).find((item) => item.status === 'ready')
  const profile = ((await get('/api/v1/runtime-profiles')).body?.items ?? []).find((item) => item.id === 'rp-basic')
  if (!workspace || !profile) throw new Error('need a ready workspace and the rp-basic profile')

  for (const runtimeType of wanted) {
    const template = templates.find((item) => item.runtimeType === runtimeType)
    check(`${runtimeType}: 카탈로그에 템플릿이 있음`, Boolean(template))
    if (!template) continue

    const created = await post('/api/v1/agents', {
      name: `path-${runtimeType}-${stamp}`, templateId: template.id, runtimeType,
      runtimeProfileId: profile.id, workspaceId: workspace.id, modelEndpointId: '',
      description: 'platform path e2e',
    })
    const agent = created.body?.agent ?? created.body
    if (agent?.id && securityProfileId) {
      // Creating from a template takes the template's security profile, so a
      // privilege has to be attached afterwards — which is also how an
      // administrator would do it.
      const assigned = await call('PUT', `/api/v1/agents/${agent.id}`, {
        name: agent.name, description: agent.description ?? '', runtimeType,
        runtimeProfileId: profile.id, workspaceId: workspace.id,
        securityProfileId,
      })
      check(`${runtimeType}: 보안 프로파일 ${securityProfileId} 지정`, assigned.status === 200,
        `HTTP ${assigned.status} ${JSON.stringify(assigned.body?.error?.message ?? '')}`)
    }
    check(`${runtimeType}: 에이전트 생성`, Boolean(agent?.id), `HTTP ${created.status} ${JSON.stringify(created.body?.error?.message ?? '')}`)
    if (!agent?.id) continue

    try {
      // The console's own next step. Everything after this is the platform's
      // decision: which image, which command, which sidecars.
      const spawned = await post(`/api/v1/agents/${agent.id}/spawn`)
      check(`${runtimeType}: 런타임 기동 요청`, spawned.status === 200 || spawned.status === 201 || spawned.status === 202,
        `HTTP ${spawned.status} ${JSON.stringify(spawned.body?.error?.message ?? '')}`)

      const deadline = Date.now() + readyTimeoutMs
      let runtime = null
      let lastStatus = ''
      while (Date.now() < deadline) {
        const listed = (await get('/api/v1/runtimes')).body?.items ?? []
        runtime = listed.find((item) => item.agentId === agent.id)
        lastStatus = runtime?.status ?? 'none'
        if (lastStatus === 'running' || lastStatus === 'ready') break
        if (lastStatus === 'failed') break
        await sleep(3000)
      }
      const up = lastStatus === 'running' || lastStatus === 'ready'
      check(`${runtimeType}: 런타임이 준비됨`, up, `status=${lastStatus} ${runtime?.failureReason ?? ''}`)

      if (runtime?.id) {
        // The check that would have caught the bug: the image the platform chose
        // has to be this runtime's own, not the shared base.
        const detail = runtime
        const image = detail.image ?? detail.imageReference ?? ''
        if (image) {
          check(`${runtimeType}: 자기 이미지로 기동`, !/agenthub-base:/.test(image), image)
        }
        if (up) {
          // And the session a person would open answers rather than 404ing.
          const session = await post(`/api/v1/runtimes/${runtime.id}/sessions`, { title: `path-${stamp}` })
          check(`${runtimeType}: 세션이 열림`, session.status === 200 || session.status === 201,
            `HTTP ${session.status} ${JSON.stringify(session.body?.error?.message ?? '')}`)
        }
      }
    } finally {
      const removed = await del(`/api/v1/agents/${agent.id}`)
      check(`${runtimeType}: 정리`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
    }
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nplatform path e2e passed')
