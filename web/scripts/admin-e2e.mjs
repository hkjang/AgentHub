// Verifies the administration screens against real data.
//
// Every figure an operator reads here is an aggregate over rows the execution
// plane already wrote, so the check that matters is not that the endpoint
// answers — it is that the numbers move when work happens, and that they agree
// with the detail screens they are summarising.
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
  const download = (path) =>
    page.evaluate(async (path) => {
      const response = await fetch(path, { credentials: 'include' })
      // The bytes, not the decoded string: Response.text() strips a leading BOM,
      // so the one thing worth checking about it would be invisible.
      const buffer = new Uint8Array(await response.arrayBuffer())
      return {
        status: response.status,
        type: response.headers.get('content-type'),
        disposition: response.headers.get('content-disposition'),
        bom: buffer[0] === 0xef && buffer[1] === 0xbb && buffer[2] === 0xbf,
        text: new TextDecoder('utf-8').decode(buffer),
      }
    }, path)
  const get = (path) => call('GET', path)
  const post = (path, body) => call('POST', path, body)
  const put = (path, body) => call('PUT', path, body)

  // Labels carry a run marker so a pause reason or a task title from this run is
  // distinguishable from one left by a previous run against the same deployment.
  const stamp = Date.now().toString(36)
  const overview = (await get('/api/v1/admin/overview?days=7')).body
  check('운영 현황 응답', Boolean(overview?.execution && overview?.spend && overview?.users), Object.keys(overview ?? {}).join(','))

  // The totals must agree with the screens they summarise, or the overview is a
  // second source of truth that quietly drifts.
  const agents = (await get('/api/v1/agents')).body?.items ?? []
  check('에이전트 수가 목록과 일치', overview.agents.total === agents.length, `${overview.agents.total} vs ${agents.length}`)
  const users = (await get('/api/v1/admin/users')).body
  check('사용자 수가 목록과 일치', overview.users.total === (users?.items ?? []).length, `${overview.users.total} vs ${(users?.items ?? []).length}`)
  check('계정별 활동 제공', Boolean(users?.activity) && Object.values(users.activity).every((a) => typeof a.tasks === 'number'))
  const queue = (await get('/api/v1/queue?scope=all')).body
  check('대기열 수치가 큐 화면과 일치', overview.queue.ready === queue.ready && overview.queue.running === queue.running,
    `${overview.queue.ready}/${overview.queue.running} vs ${queue.ready}/${queue.running}`)

  // Success rate is a ratio of finished tasks, so it has to be computable from
  // the counts printed beside it.
  const finished = overview.execution.completed + overview.execution.failed + overview.execution.deadLetter
  const expected = finished > 0 ? (overview.execution.completed / finished) * 100 : 0
  check('성공률이 완료·실패 수와 일치', Math.abs(overview.execution.successRate - expected) < 0.01,
    `${overview.execution.successRate} vs ${expected}`)

  // The spend breakdowns are truncated; the totals must not be.
  const usersSpend = overview.spend.users.reduce((sum, row) => sum + row.inputTokens + row.outputTokens, 0)
  check('사용자별 합이 전체 토큰을 넘지 않음', usersSpend <= overview.spend.inputTokens + overview.spend.outputTokens,
    `${usersSpend} vs ${overview.spend.inputTokens + overview.spend.outputTokens}`)
  const usage = (await get('/api/v1/usage?scope=all')).body
  check('사용량 화면과 통화가 일치', overview.spend.currency === usage.currency, `${overview.spend.currency} vs ${usage.currency}`)

  // Windows are validated rather than silently clamped: a report for the wrong
  // period is worse than an error.
  check('기간 0일 거절', (await get('/api/v1/admin/overview?days=0')).status === 400)
  check('기간 과다 거절', (await get('/api/v1/admin/overview?days=9999')).status === 400)
  check('잘못된 시각 거절', (await get('/api/v1/admin/overview?from=yesterday')).status === 400)

  // The audit trail is searched, not scrolled.
  const all = (await get('/api/v1/admin/audit?limit=5')).body
  check('감사 페이지 정보 제공', typeof all?.total === 'number' && all.limit === 5 && Array.isArray(all.actions),
    `total=${all?.total} actions=${(all?.actions ?? []).length}`)
  const promoteOnly = (await get('/api/v1/admin/audit?action=agent.promote&limit=100')).body
  check('동작으로 필터', (promoteOnly?.items ?? []).every((item) => item.action.startsWith('agent.promote')) && promoteOnly.total <= all.total,
    `${promoteOnly?.total} of ${all?.total}`)
  const prefix = (await get('/api/v1/admin/audit?action=agent.&limit=100')).body
  check('접두사로 필터', (prefix?.items ?? []).every((item) => item.action.startsWith('agent.')) && prefix.total >= promoteOnly.total,
    `${prefix?.total} >= ${promoteOnly?.total}`)
  const byActor = (await get(`/api/v1/admin/audit?actor=${username.slice(1, 4)}&limit=5`)).body
  check('수행자 일부로 필터', (byActor?.items ?? []).every((item) => item.actor.includes(username.slice(1, 4))), `${byActor?.total}`)
  const nobody = (await get('/api/v1/admin/audit?actor=존재하지않는사용자')).body
  check('일치가 없으면 빈 결과', nobody?.total === 0 && nobody.items.length === 0)
  const future = (await get(`/api/v1/admin/audit?from=${new Date(Date.now() + 86400000).toISOString()}`)).body
  check('기간 밖은 제외', future?.total === 0, `${future?.total}`)
  check('잘못된 시각 거절 (감사)', (await get('/api/v1/admin/audit?from=오늘')).status === 400)

  const firstPage = (await get('/api/v1/admin/audit?limit=2&offset=0')).body
  const secondPage = (await get('/api/v1/admin/audit?limit=2&offset=2')).body
  check('페이지가 겹치지 않음',
    firstPage.items.length === 0 || secondPage.items.length === 0 ||
    firstPage.items.every((item) => !secondPage.items.some((other) => other.id === item.id)),
    `${firstPage.items.map((i) => i.id)} vs ${secondPage.items.map((i) => i.id)}`)

  // Exports are what a reconciliation and an audit review actually keep.
  const usageCsv = await download('/api/v1/admin/usage/export?days=30')
  check('사용량 CSV 내려받기', usageCsv.status === 200 && /text\/csv/.test(usageCsv.type ?? ''), `${usageCsv.status} ${usageCsv.type}`)
  check('CSV 파일 이름 지정', /filename="agenthub-usage-\d{8}\.csv"/.test(usageCsv.disposition ?? ''), usageCsv.disposition)
  // Excel on a Korean Windows install reads a BOM-less UTF-8 file as the legacy
  // code page and shows mojibake, which reads as a broken export.
  check('CSV에 BOM 포함', usageCsv.bom)
  check('CSV 머리글', usageCsv.text.replace(/^﻿/, '').startsWith('구분,ID,이름,실행,입력토큰,출력토큰,비용,통화,단가적용'),
    usageCsv.text.replace(/^﻿/, '').slice(0, 60))
  const auditCsv = await download('/api/v1/admin/audit/export?action=agent.promote')
  check('감사 CSV 내려받기', auditCsv.status === 200 && /text\/csv/.test(auditCsv.type ?? ''), `${auditCsv.status}`)
  const auditRows = auditCsv.text.trim().split('\n').length - 1
  check('감사 CSV가 필터를 따름', auditRows === promoteOnly.total, `${auditRows} rows vs ${promoteOnly.total}`)
  // The export itself is an administrative action, so it is in the trail too.
  const afterExport = (await get('/api/v1/admin/audit?action=admin.&limit=5')).body
  check('내려받기도 감사에 남음', (afterExport?.items ?? []).some((item) => item.action === 'admin.audit.export' || item.action === 'admin.usage.export'),
    (afterExport?.items ?? []).map((i) => i.action).join(','))

  // --- Operating the plane ---
  //
  // The overview can say a queue has no worker behind it and that events were
  // not delivered; these are the actions those findings ask for.
  const execution = (await get('/api/v1/admin/execution')).body
  check('실행 상태 응답', typeof execution?.paused === 'boolean' && Array.isArray(execution?.workers),
    `paused=${execution?.paused} workers=${(execution?.workers ?? []).length}`)
  check('워커 하트비트 주기 공개', execution.heartbeatSeconds > 0 && execution.staleAfterSeconds > execution.heartbeatSeconds,
    `${execution.heartbeatSeconds}/${execution.staleAfterSeconds}`)

  const paused = await post('/api/v1/admin/execution/pause', { paused: true, reason: `점검 ${stamp}` })
  check('실행 중지', paused.status === 200 && paused.body?.paused === true, `HTTP ${paused.status}`)
  // The people whose work stopped moving are not administrators, so the pause
  // reaches every signed-in user.
  const capabilities = (await get('/api/v1/capabilities')).body
  check('중지 상태가 사용자에게 보임', capabilities?.executionPaused === true && capabilities.executionPausedReason === `점검 ${stamp}`,
    `${capabilities?.executionPaused} ${capabilities?.executionPausedReason}`)
  // Queueing continues while paused: losing the work that arrived during an
  // upgrade would be worse than running it late.
  const duringPause = await post('/api/v1/tasks', { agentId: agents[0].id, title: `중지 중 ${stamp}`, input: '확인' })
  check('중지 중에도 작업 등록은 가능', duringPause.status === 202 || duringPause.status === 201 || duringPause.status === 409,
    `HTTP ${duringPause.status}`)
  const resumed = await post('/api/v1/admin/execution/pause', { paused: false })
  check('실행 재개', resumed.status === 200 && resumed.body?.paused === false)
  check('재개하면 사용자 화면에서도 사라짐', (await get('/api/v1/capabilities')).body?.executionPaused === false)

  const reclaimed = await post('/api/v1/admin/execution/reclaim', {})
  check('멈춘 작업 회수 호출', reclaimed.status === 200 && typeof reclaimed.body?.reclaimed === 'number', `HTTP ${reclaimed.status}`)

  // Only the two terminal failures may be recovered in bulk.
  check('완료된 작업은 일괄 재실행 불가', (await post('/api/v1/admin/execution/requeue', { status: 'completed' })).status === 400)
  check('취소된 작업도 불가', (await post('/api/v1/admin/execution/requeue', { status: 'cancelled' })).status === 400)
  const requeued = await post('/api/v1/admin/execution/requeue', { status: 'dead_letter', sinceHours: 24 })
  check('처리 불가 작업 일괄 재실행', requeued.status === 200 && typeof requeued.body?.requeued === 'number', `HTTP ${requeued.status}`)

  const redelivered = await post('/api/v1/admin/execution/events/redeliver', {})
  check('이벤트 재배달 호출', redelivered.status === 200 && typeof redelivered.body?.redelivered === 'number')

  // Retention deletes history that cannot be reconstructed, so the floors matter
  // more than the feature.
  check('감사 보관 하한 거절', (await post('/api/v1/admin/execution/cleanup', { auditDays: 3 })).status === 400)
  check('실행 기록 보관 하한 거절', (await post('/api/v1/admin/execution/cleanup', { runDays: 1 })).status === 400)
  const dryRun = await post('/api/v1/admin/execution/cleanup', { taskDays: 7, runDays: 7, eventDays: 3, auditDays: 30 })
  check('정리 미리보기는 삭제하지 않음', dryRun.status === 200 && dryRun.body?.dryRun === true, `HTTP ${dryRun.status}`)
  const auditBefore = (await get('/api/v1/admin/audit?limit=1')).body?.total
  await post('/api/v1/admin/execution/cleanup', { auditDays: 3650 })
  check('미리보기 후에도 감사 로그가 그대로', (await get('/api/v1/admin/audit?limit=1')).body?.total >= auditBefore,
    `${auditBefore}`)
  const retention = await put('/api/v1/admin/execution/retention', { taskDays: 30, runDays: 30, eventDays: 7, auditDays: 365 })
  check('보관 기간 저장', retention.status === 200 && retention.body?.auditDays === 365, `HTTP ${retention.status}`)
  check('보관 기간이 상태에 반영', (await get('/api/v1/admin/execution')).body?.retention?.auditDays === 365)
  await put('/api/v1/admin/execution/retention', { taskDays: 0, runDays: 0, eventDays: 0, auditDays: 0 })

  // The platform-wide runtime environment is copied into each runtime's object,
  // so saving it has to push the change out — otherwise "저장했습니다" means
  // nothing changes until somebody restarts every runtime by hand.
  const environment = await put('/api/v1/admin/settings/runtimeEnvironment', {
    value: {
      files: [{ path: '/etc/pip.conf', content: `[global]\nindex-url = https://nexus.local/simple\n`, mode: '0644', description: `e2e ${stamp}` }],
      variables: [{ name: 'PIP_INDEX_URL', value: 'https://nexus.local/simple' }],
    },
  })
  check('런타임 환경 저장', environment.status === 200 && environment.body?.saved === true, `HTTP ${environment.status}`)
  const push = environment.body?.runtimeEnvironment
  check('저장이 기존 런타임에 적용을 시도함', push && typeof push.applied === 'number' && typeof push.message === 'string',
    JSON.stringify(push))
  check('적용 결과를 사람이 읽을 수 있게 설명함', /적용|재시작|Kubernetes|CRD/.test(push?.message ?? ''), push?.message)
  const storedEnvironment = (await get('/api/v1/admin/settings')).body?.runtimeEnvironment
  check('설정이 그대로 저장됨', storedEnvironment?.files?.[0]?.path === '/etc/pip.conf' && /nexus\.local/.test(storedEnvironment.files[0].content),
    JSON.stringify(storedEnvironment?.files?.[0] ?? {}))
  // Paths the platform owns are refused, because a file dropped by the operator
  // would look exactly like one that was applied.
  const refused = await put('/api/v1/admin/settings/runtimeEnvironment', {
    value: { files: [{ path: '/etc/agenthub/runtime.json', content: 'x' }], variables: [] },
  })
  check('플랫폼 경로는 거절', refused.status === 400, `HTTP ${refused.status}`)
  await put('/api/v1/admin/settings/runtimeEnvironment', { value: storedEnvironment ?? { files: [], variables: [] } })

  const workers = (await get('/api/v1/admin/workers')).body
  check('워커 목록과 용량', Array.isArray(workers?.items) && typeof workers?.capacity === 'number',
    `${(workers?.items ?? []).length} workers, capacity ${workers?.capacity}`)

  // The console has to render all of it, since that is where it is read.
  await page.goto(`${baseURL}/admin/overview`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: '운영 현황' }).waitFor({ timeout: 15000 })
  check('KPI 카드 표시', (await page.locator('.kpi-row article').count()) === 6, String(await page.locator('.kpi-row article').count()))
  check('사용량 분해 표 표시', (await page.locator('.insight-table').count()) === 3)
  check('기간 전환 동작', await page.getByRole('button', { name: '24시간' }).isVisible())
  await page.getByRole('button', { name: '24시간' }).click()
  await page.waitForTimeout(800)
  check('기간 전환 후에도 렌더', await page.getByRole('heading', { name: '운영 현황' }).isVisible())

  await page.goto(`${baseURL}/admin/operations`, { waitUntil: 'networkidle' })
  await page.getByRole('button', { name: '감사 로그' }).click()
  await page.locator('.audit-filters').waitFor({ timeout: 10000 })
  check('감사 검색 필터 표시', (await page.locator('.audit-filters label').count()) === 5, String(await page.locator('.audit-filters label').count()))
  const rowsBefore = await page.locator('.table-panel tbody tr').count()
  // The action list is built from what the log actually contains, so the value to
  // filter by is read from the page rather than assumed. A fresh deployment has
  // no promotions in it yet, and pinning this to one action made the run fail on
  // a new cluster while proving nothing about the filter.
  const actionValue = await page.locator('.audit-filters select').first().evaluate((select) =>
    Array.from(select.options).map((option) => option.value).find((value) => value !== ''))
  const filtered = (await get(`/api/v1/admin/audit?action=${encodeURIComponent(actionValue)}&limit=100`)).body
  await page.locator('.audit-filters select').first().selectOption(actionValue)
  await page.waitForTimeout(900)
  const rowsAfter = await page.locator('.table-panel tbody tr').count()
  check('필터가 표를 좁힘', rowsAfter <= rowsBefore && rowsAfter === Math.min(filtered.total, 50), `${actionValue}: ${rowsBefore} → ${rowsAfter}`)
  check('페이지 요약 표시', /건 중/.test(await page.locator('.audit-pager span').innerText()))

  await page.goto(`${baseURL}/admin/execution`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: '실행 제어' }).waitFor({ timeout: 15000 })
  check('실행 스위치 표시', await page.locator('.switch-panel').isVisible())
  check('워커 표 표시', (await page.locator('.panel table').count()) >= 1)
  check('보관 기간 입력 4개', (await page.locator('.retention-panel input[type=number]').count()) === 4,
    String(await page.locator('.retention-panel input[type=number]').count()))
  check('사용법 안내 제공', await page.getByText('이 화면은 이럴 때 씁니다').isVisible())

  await page.goto(`${baseURL}/admin/users`, { waitUntil: 'networkidle' })
  await page.getByRole('heading', { name: '사용자 · 팀' }).waitFor({ timeout: 10000 })
  check('사용자 표에 사용량 열', (await page.locator('thead th').allInnerTexts()).some((text) => /토큰/.test(text)))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nadmin e2e passed')
