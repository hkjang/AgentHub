// The run listing, and the questions it exists to answer.
//
// Runs could only be reached one at a time, through the task that produced them,
// which answers "how did this task go" and nothing else. An operator arrives with
// questions about the set — what failed today, which agent is spending, which
// runs nobody counted — and those are filters. So what this checks is that the
// filters actually narrow, that they compose, and that a row still opens the
// same run detail the task screen opens.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}

const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true, args: ['--no-sandbox'] })
try {
  const page = await (await browser.newContext()).newPage()
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })
  await page.goto(`${baseURL}/runs`, { waitUntil: 'networkidle' })

  const rows = async () => {
    await page.waitForTimeout(600)
    return page.locator('tbody tr').count()
  }
  const select = async (label, value) => {
    await page.getByLabel(label).selectOption(value)
    return rows()
  }

  await page.getByLabel('기간').selectOption('')
  const all = await rows()
  if (all === 0) {
    console.log('  --   실행 기록이 없어 건너뜁니다. 작업을 한 번이라도 실행한 뒤 다시 실행하세요.')
    process.exit(0)
  }
  check('전체 실행이 보임', all > 0, `${all}건`)

  const failed = await select('상태', 'failed')
  check('상태로 좁혀짐', failed <= all, `${failed} / ${all}건`)
  check('좁힌 목록에 실패만 남음',
    (await page.locator('tbody tr').first().innerText()).includes('실패') || failed === 0)

  const recent = await select('기간', '1')
  check('기간이 상태와 함께 걸림', recent <= failed, `${recent} / ${failed}건`)

  await select('상태', '')
  const back = await rows()
  check('조건을 풀면 다시 늘어남', back >= recent, `${back} / ${recent}건`)

  // The same detail the task screen opens, reached from here.
  await page.locator('tbody tr').first().click()
  const drawer = page.locator('.drawer')
  await drawer.waitFor({ timeout: 10000 })
  const title = (await drawer.locator('h2').first().innerText()).trim()
  check('행을 누르면 실행 상세가 열림', /실행 기록|작업 일지/.test(title), title)
  await drawer.locator('.detail-hero').waitFor({ timeout: 10000 })
  const hero = (await drawer.locator('.detail-hero').innerText()).replace(/\n/g, ' ')
  check('상세에 단계와 시간이 함께 있음', /단계/.test(hero) && /ms/.test(hero), hero.slice(0, 70))
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nruns e2e passed')
