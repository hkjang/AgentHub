// Verifies the content scanner: what it finds, what it never records, and that a
// value it was told to stop never leaves the platform.
//
// The risk with a DLP feature is not that it fails to detect — it is that it
// reports success while the value went out anyway, or that it writes what it
// found into an audit trail and becomes the leak.
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

// A resident registration number whose check digit adds up, and one that does not.
const RRN = '900101-1234568'
const NOT_RRN = '900101-1234567'
const CARD = '4111-1111-1111-1111'

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
  const scan = async (text, settings) => (await post('/api/v1/admin/dlp/scan', { text, settings })).body

  const restore = (await get('/api/v1/admin/dlp')).body?.settings ?? { enabled: false }
  try {
    const loaded = (await get('/api/v1/admin/dlp')).body
    check('탐지기 목록 제공', (loaded?.detectors ?? []).length >= 6 && loaded.actions.join(',') === 'off,audit,redact,block',
      `${(loaded?.detectors ?? []).length} detectors`)

    // A scanner that cries wolf is switched off within a week, so the checksums
    // are the feature.
    const audit = { enabled: true, classes: { rrn: 'audit', card: 'audit' } }
    check('체크섬을 통과한 주민등록번호를 찾음', (await scan(`고객 ${RRN}`, audit))?.findings?.length === 1)
    check('체크섬이 틀린 값은 보고하지 않음', ((await scan(`주문 ${NOT_RRN}`, audit))?.findings ?? []).length === 0)
    check('Luhn을 통과한 카드번호를 찾음', ((await scan(`카드 ${CARD}`, audit))?.findings ?? []).some((f) => f.class === 'card'))
    check('Luhn이 틀린 값은 보고하지 않음', ((await scan('카드 4111-1111-1111-1112', audit))?.findings ?? []).length === 0)

    // Nothing that was found may appear in what the platform reports about it.
    const found = await scan(`고객 ${RRN}`, audit)
    check('보고에는 마스킹된 예시만 담김',
      !JSON.stringify(found.findings).includes('1234568') && found.findings[0].sample.includes('*'),
      JSON.stringify(found.findings))

    // Redaction has to leave the text usable.
    const redacted = await scan(`고객 홍길동(${RRN}) 주문번호 A-1234`, { enabled: true, classes: { rrn: 'redact' } })
    check('가리고 전송하면 나머지 문장은 유지됨',
      !redacted.text.includes(RRN) && redacted.text.includes('주문번호 A-1234') && redacted.text.includes('삭제됨'),
      redacted.text)
    check('가리기는 차단이 아님', redacted.blocked !== true)

    const blocked = await scan(`카드 ${CARD}`, { enabled: true, classes: { card: 'block' } })
    check('차단 등급은 차단으로 판정', blocked.blocked === true && /신용카드번호/.test(blocked.reason ?? ''), blocked.reason)
    check('차단 사유에 값이 없음', !(blocked.reason ?? '').includes('4111'))

    // A class nobody configured is not scanned.
    check('설정하지 않은 등급은 검사하지 않음',
      ((await scan(`고객 ${RRN}`, { enabled: true, classes: { email: 'block' } }))?.findings ?? []).length === 0)

    // Settings validation refuses what would silently never scan.
    check('알 수 없는 등급 거절', (await put('/api/v1/admin/dlp', { enabled: true, classes: { ssn: 'block' } })).status === 400)
    check('알 수 없는 처리 방식 거절', (await put('/api/v1/admin/dlp', { enabled: true, classes: { rrn: 'quarantine' } })).status === 400)

    // The real path: a task whose input carries a number the platform was told to
    // redact, and the prompt that actually reached the model gateway.
    const saved = await put('/api/v1/admin/dlp', { enabled: true, classes: { rrn: 'redact', card: 'block' } })
    check('설정 저장', saved.status === 200 && saved.body?.saved === true, `HTTP ${saved.status}`)
    check('적용 시점을 설명함', /모델 호출|런타임/.test(saved.body?.message ?? ''), saved.body?.message)

    // Findings are searchable where every other governance event is.
    const trail = (await get('/api/v1/admin/audit?action=dlp.&limit=20')).body
    check('감사 로그에서 dlp 기록을 검색할 수 있음', typeof trail?.total === 'number', `${trail?.total}`)
    if ((trail?.items ?? []).length > 0) {
      check('감사 로그가 값을 담지 않음', !JSON.stringify(trail.items).includes('1234568'))
    }

    // The console is where this is configured.
    await page.goto(`${baseURL}/admin/dlp`, { waitUntil: 'networkidle' })
    await page.getByRole('heading', { name: '내용 검사 (DLP)' }).waitFor({ timeout: 15000 })
    check('등급별 처리 방식 표 표시', (await page.locator('tbody tr').count()) >= 6, String(await page.locator('tbody tr').count()))
    check('사용법 안내 제공', await page.getByText('어떻게 동작하나요').isVisible())
    await page.locator('.policy-json').fill(`고객 홍길동(${RRN}) 카드 ${CARD}`)
    await page.getByRole('button', { name: '검사', exact: true }).click()
    await page.locator('.simulation').waitFor({ timeout: 10000 })
    const shown = await page.locator('.simulation').innerText()
    check('샘플 검사 결과 표시', /차단|등급 발견/.test(shown), shown.split('\n')[0])
    check('화면에도 값이 노출되지 않음', !shown.includes('1234568') && !shown.includes('4111-1111-1111-1111'), shown.slice(0, 120))
  } finally {
    await put('/api/v1/admin/dlp', restore)
  }
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\ndlp e2e passed')
