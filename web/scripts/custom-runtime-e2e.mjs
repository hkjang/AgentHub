// Verifies the custom runtime type: a site's own agent image, started by the
// command its definition declares, serving on the port it declares.
//
// The failure this guards against is silent — with no command the container runs
// its image's default entrypoint and crash-loops with nothing in the Pod status
// explaining why — so the run checks both that a declared command actually
// starts the process and that a missing one is refused before anything spawns.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { execFileSync } from 'node:child_process'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const namespace = process.env.AGENTHUB_RUNTIME_NAMESPACE ?? 'agent-runtime-dev'
// Any image the cluster already has will do; the point is the declared command.
const image = process.env.AGENTHUB_CUSTOM_TEST_IMAGE ?? 'agenthub-base:v0.7.0'
const port = 9000

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

  const sample = ((await get('/api/v1/agents')).body?.items ?? []).find((agent) => agent.workspaceId)
  if (!sample) throw new Error('no agent to copy workspace and profile settings from')
  const stamp = Date.now().toString(36)
  const base = {
    description: 'custom 런타임 e2e 전용', runtimeType: 'custom',
    runtimeProfileId: sample.runtimeProfileId ?? '', workspaceId: sample.workspaceId ?? '',
    systemPrompt: '사내 자체 런타임 검증용입니다.',
    securityProfileId: sample.securityProfileId ?? '', networkProfileId: sample.networkProfileId ?? '',
  }

  // A definition with no command must be refused where someone can read the
  // reason, not spawn a Pod that crash-loops.
  const naked = await post('/api/v1/agents', { ...base, name: `custom-nocmd-${stamp}` })
  check('시작 명령 없는 custom 정의 거절', naked.status === 400, `HTTP ${naked.status} ${JSON.stringify(naked.body?.message ?? naked.body)}`)
  const blank = await post('/api/v1/agents', { ...base, name: `custom-blank-${stamp}`, customCommand: ['  ', ''] })
  check('공백뿐인 시작 명령 거절', blank.status === 400, `HTTP ${blank.status}`)
  const badPort = await post('/api/v1/agents', { ...base, name: `custom-badport-${stamp}`, customCommand: ['serve'], customPort: 70000 })
  check('범위를 벗어난 포트 거절', badPort.status === 400, `HTTP ${badPort.status}`)

  const created = await post('/api/v1/agents', {
    ...base, name: `custom-${stamp}`,
    customCommand: ['/opt/hermes/.venv/bin/python3', '-m', 'http.server', String(port), '--bind', '0.0.0.0'],
    customPort: port,
  })
  const agent = created.body?.agent ?? created.body
  if (!agent?.id) throw new Error(`agent not created: HTTP ${created.status} ${JSON.stringify(created.body)}`)
  check('custom 에이전트 생성', Boolean(agent.id))
  check('시작 명령 저장', (agent.spec?.customCommand ?? []).length === 6, JSON.stringify(agent.spec?.customCommand))
  check('포트 저장', agent.spec?.customPort === port, String(agent.spec?.customPort))

  // The command survives an edit that does not mention it being re-sent.
  const renamed = await put(`/api/v1/agents/${agent.id}`, {
    name: `custom-${stamp}`, description: base.description, systemPrompt: base.systemPrompt,
    runtimeProfileId: base.runtimeProfileId, workspaceId: base.workspaceId,
    customCommand: agent.spec.customCommand, customPort: port,
  })
  check('수정 후에도 시작 명령 유지', ((renamed.body?.agent ?? renamed.body)?.spec?.customCommand ?? []).length === 6, `HTTP ${renamed.status}`)

  // Pin the image the command exists in, so the run does not depend on whatever
  // the catalog currently approves for this type.
  const images = (await get('/api/v1/admin/runtime-images')).body?.items ?? []
  // Same image reference *and* the same runtime type: an image registered for
  // another runtime cannot be pinned here, and pinning one would start a Pod
  // whose command does not exist in it.
  let pinned = images.find((item) => item.runtimeType === 'custom' && (`${item.image}:${item.version}` === image || item.image === image))
  if (!pinned) {
    const [imageName, imageVersion] = image.split(':')
    const saved = await post('/api/v1/admin/runtime-images', {
      runtimeType: 'custom', name: `custom-e2e-${stamp}`, image: imageName, version: imageVersion, approved: true,
    })
    // The handler answers with the row itself, not a wrapper.
    pinned = saved.body
    check('테스트용 런타임 이미지 등록', Boolean(pinned?.id), `HTTP ${saved.status} ${JSON.stringify(saved.body).slice(0, 120)}`)
  }
  if (pinned?.id) {
    const repin = await put(`/api/v1/agents/${agent.id}`, {
      name: `custom-${stamp}`, description: base.description, systemPrompt: base.systemPrompt,
      runtimeProfileId: base.runtimeProfileId, workspaceId: base.workspaceId,
      customCommand: agent.spec.customCommand, customPort: port, runtimeImageId: pinned.id,
    })
    check('런타임 이미지 고정', repin.status === 200, `HTTP ${repin.status}`)
  }

  const spawned = await post(`/api/v1/agents/${agent.id}/spawn`)
  check('Runtime 기동 요청', spawned.status < 300, `HTTP ${spawned.status}`)
  const runtime = spawned.body?.runtime ?? spawned.body
  const pod = `${runtime?.crdName}-0`

  let ready = false
  for (let attempt = 0; attempt < 60 && !ready; attempt++) {
    try {
      const phase = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.status.phase}').trim()
      const states = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.status.containerStatuses[*].ready}').trim()
      ready = phase === 'Running' && states.length > 0 && !states.includes('false')
    } catch { /* not created yet */ }
    if (!ready) await page.waitForTimeout(5000)
  }
  check('custom Runtime Pod 준비 완료', ready, pod)

  if (ready) {
    const command = JSON.parse(kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.spec.containers[0].command}'))
    check('선언한 명령으로 기동', command.join(' ').includes('http.server ' + port), command.join(' '))
    const containerPort = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.spec.containers[0].ports[0].containerPort}').trim()
    check('선언한 포트가 컨테이너 포트에 반영', containerPort === String(port), containerPort)
    const servicePort = kubectl('-n', namespace, 'get', 'service', runtime.crdName, '-o', 'jsonpath={.spec.ports[0].port}').trim()
    check('선언한 포트가 Service에 반영', servicePort === String(port), servicePort)
    const probe = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.spec.containers[0].readinessProbe.tcpSocket.port}').trim()
    check('선언한 포트가 준비 프로브에 반영', probe === String(port), probe)
    const restarts = kubectl('-n', namespace, 'get', 'pod', pod, '-o', 'jsonpath={.status.containerStatuses[0].restartCount}').trim()
    check('CrashLoop 없이 유지', restarts === '0', `restarts=${restarts}`)
  } else {
    try { console.log(kubectl('-n', namespace, 'describe', 'pod', pod).split('Events:').pop()) } catch { /* no pod */ }
  }

  if (runtime?.id) await post(`/api/v1/runtimes/${runtime.id}/stop`)
  const removed = await del(`/api/v1/agents/${agent.id}`)
  check(`정리: custom-${stamp} 삭제`, removed.status === 204 || removed.status === 200, `HTTP ${removed.status}`)
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  problems.forEach((problem) => console.error(` - ${problem}`))
  process.exit(1)
}
console.log('\ncustom runtime e2e passed')
