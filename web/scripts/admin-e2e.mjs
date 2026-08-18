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
  await page.locator('.audit-filters select').first().selectOption('agent.promote')
  await page.waitForTimeout(900)
  const rowsAfter = await page.locator('.table-panel tbody tr').count()
  check('필터가 표를 좁힘', rowsAfter <= rowsBefore && rowsAfter === Math.min(promoteOnly.total, 50), `${rowsBefore} → ${rowsAfter}`)
  check('페이지 요약 표시', /건 중/.test(await page.locator('.audit-pager span').innerText()))

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
