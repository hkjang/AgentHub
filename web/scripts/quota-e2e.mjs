// Verifies that a department's capacity and one person's exception can be set
// from the console, and that the console says which of the three levels a limit
// actually came from.
//
// That last part is the point of the screen. An administrator looking at
// "Runtime 2개" cannot act on the number alone — the fix is to edit the person,
// the department, or the platform default, and editing the wrong one changes
// nothing while looking like it worked. So the checks here follow one limit down
// through the levels and read the source back at each step.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'
const DEPARTMENT = 'E2E 자원 실험실'

const problems = []
const check = (label, ok, detail = '') => {
  console.log(`${ok ? '  ok  ' : ' FAIL '} ${label}${detail ? ` — ${detail}` : ''}`)
  if (!ok) problems.push(`${label}${detail ? `: ${detail}` : ''}`)
}

const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
try {
  const context = await browser.newContext()
  const page = await context.newPage()
  page.on('dialog', (dialog) => dialog.accept())
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })

  const fill = async (group, label, value) => {
    await page.locator('fieldset', { hasText: group }).locator('label', { hasText: label }).locator('input').fill(String(value))
  }
  const openQuotas = async () => {
    await page.goto(`${baseURL}/admin/quotas`, { waitUntil: 'networkidle' })
    await page.getByRole('heading', { name: '부서 · 개인 Quota' }).waitFor({ timeout: 15000 })
  }
  const removeDepartment = async () => {
    const card = page.locator('.quota-card', { hasText: DEPARTMENT })
    if (await card.count() === 0) return
    await card.first().getByRole('button', { name: /삭제$/ }).click()
    await card.first().waitFor({ state: 'detached', timeout: 10000 })
  }

  await openQuotas()
  await removeDepartment() // a previous run that died before its cleanup

  // --- a department is capacity for a group, and a default for one member ----
  await page.getByRole('button', { name: '새 부서' }).click()
  await page.locator('#department-form').waitFor({ timeout: 5000 })
  await page.locator('label', { hasText: '부서 이름' }).locator('input').fill(DEPARTMENT)
  await fill('구성원 1인 기본', 'Runtime 수', 1)
  await fill('구성원 1인 기본', 'Memory', 2048)
  await fill('부서 총량', 'Runtime 수', 2)
  await fill('부서 총량', '토큰 예산', 500000)
  await page.getByRole('button', { name: '저장', exact: true }).click()
  const card = page.locator('.quota-card', { hasText: DEPARTMENT })
  await card.waitFor({ timeout: 10000 })
  const cardText = await card.innerText()
  check('부서 카드에 1인 기본이 보임', /Runtime 수 1개/.test(cardText), cardText.replace(/\n/g, ' · '))
  check('부서 총량도 함께 보임', /Runtime 수 2개/.test(cardText))
  check('총량 사용량 막대가 그려짐', await card.locator('.quota-usage i').count() > 0)
  check('부서 총량에 토큰 예산도 담김', /토큰 예산/.test(cardText), cardText.replace(/\n/g, ' · '))

  // Two names can slug to the same id. An upsert would have rewritten the first
  // department's limits while its members went on pointing at it, so a create
  // that collides has to be refused — and say so in words somebody can act on.
  await page.getByRole('button', { name: '새 부서' }).click()
  await page.locator('#department-form').waitFor({ timeout: 5000 })
  await page.locator('label', { hasText: '부서 이름' }).locator('input').fill(DEPARTMENT.replace(/ /g, '-'))
  await page.getByRole('button', { name: '저장', exact: true }).click()
  const banner = page.locator('.drawer .alert.error').first()
  await banner.waitFor({ timeout: 10000 })
  check('같은 이름의 부서를 다시 만들 수 없음', /이미 있습니다/.test(await banner.innerText()), await banner.innerText())
  await page.getByRole('button', { name: '취소' }).click()
  check('먼저 만든 부서의 한도가 그대로임', /Runtime 수 1개/.test(await card.innerText()))

  // --- a person joins it, and the console says where each limit came from ----
  await page.getByRole('button', { name: /^개인/ }).click()
  const row = page.locator('tbody tr', { hasText: username }).first()
  await row.getByRole('button', { name: /Quota$/ }).click()
  await page.locator('#person-quota-form').waitFor({ timeout: 5000 })
  await page.locator('label', { hasText: '부서' }).locator('select').selectOption({ label: DEPARTMENT })
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await page.locator('#person-quota-form').waitFor({ state: 'detached', timeout: 10000 })
  await row.getByRole('button', { name: /Quota$/ }).click()
  await page.locator('.quota-effective').waitFor({ timeout: 10000 })
  const memoryRow = page.locator('.quota-effective tr', { hasText: 'Memory' }).first()
  // "구성원 1명" is a number; the name is what an administrator is deciding about.
  await page.getByRole('button', { name: '취소' }).click()
  await page.getByRole('button', { name: /^부서/ }).click()
  await page.locator('.quota-card', { hasText: DEPARTMENT }).getByRole('button', { name: '한도 수정' }).click()
  const roster = page.locator('.quota-members')
  await roster.waitFor({ timeout: 5000 })
  const rosterText = await roster.innerText()
  check('부서 화면에서 구성원 이름이 보임', rosterText.includes(username), JSON.stringify(rosterText))
  await page.getByRole('button', { name: '취소' }).click()
  await page.getByRole('button', { name: /^개인/ }).click()
  await row.getByRole('button', { name: /Quota$/ }).click()
  await page.locator('.quota-effective').waitFor({ timeout: 10000 })
  check('부서가 정한 한도의 출처가 부서로 표시됨', (await memoryRow.innerText()).includes('부서'), await memoryRow.innerText())
  check('부서 이름까지 함께 표시됨', (await memoryRow.innerText()).includes(DEPARTMENT))

  // --- one person may be an exception, and it wins over the department -------
  await fill('개인 예외', 'Runtime 수', 4)
  await page.locator('label', { hasText: '메모' }).locator('input').fill('E2E 예외')
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await page.locator('#person-quota-form').waitFor({ state: 'detached', timeout: 10000 })
  check('개인 예외가 목록에 보임', (await row.innerText()).includes('E2E 예외'), await row.innerText())
  await row.getByRole('button', { name: /Quota$/ }).click()
  await page.locator('.quota-effective').waitFor({ timeout: 10000 })
  const runtimeRow = page.locator('.quota-effective tr', { hasText: 'Runtime 수' }).first()
  check('개인 예외가 적용값이 됨', (await runtimeRow.innerText()).includes('4개'), await runtimeRow.innerText())
  check('출처가 개인 예외로 표시됨', (await runtimeRow.innerText()).includes('개인 예외'))
  check('덮어쓰지 않은 항목은 부서 값을 유지', (await memoryRow.innerText()).includes('2048MB'), await memoryRow.innerText())

  // --- the person sees their own limit without asking an administrator ------
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  const panel = page.locator('.quota-panel')
  await panel.waitFor({ timeout: 15000 })
  const panelText = await panel.innerText()
  check('홈 화면에 내 한도가 보임', /Runtime 수/.test(panelText), panelText.replace(/\n/g, ' · '))
  check('내 한도에 개인 예외 4개가 반영됨', /\/ 4개/.test(panelText))
  check('부서 총량도 함께 안내됨', /부서 총량/.test(panelText))

  // --- taking the exception away puts the department's limit back -----------
  await openQuotas()
  await page.getByRole('button', { name: /^개인/ }).click()
  await page.locator('tbody tr', { hasText: username }).first().getByRole('button', { name: /Quota$/ }).click()
  await page.locator('#person-quota-form').waitFor({ timeout: 5000 })
  await fill('개인 예외', 'Runtime 수', '')
  await page.locator('label', { hasText: '메모' }).locator('input').fill('')
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await page.locator('#person-quota-form').waitFor({ state: 'detached', timeout: 10000 })
  await page.locator('tbody tr', { hasText: username }).first().getByRole('button', { name: /Quota$/ }).click()
  await page.locator('.quota-effective').waitFor({ timeout: 10000 })
  const back = await page.locator('.quota-effective tr', { hasText: 'Runtime 수' }).first().innerText()
  check('예외를 지우면 부서 한도로 돌아옴', back.includes('1개') && back.includes('부서'), back)

  // Leave the account as it was found: no department, no override.
  await page.locator('label', { hasText: '부서' }).locator('select').selectOption('')
  await page.getByRole('button', { name: '저장', exact: true }).click()
  await page.locator('#person-quota-form').waitFor({ state: 'detached', timeout: 10000 })
  await page.getByRole('button', { name: /^부서/ }).click()
  await removeDepartment()
  check('정리: 부서를 삭제함', await page.locator('.quota-card', { hasText: DEPARTMENT }).count() === 0)
} finally {
  await browser.close()
}

if (problems.length) {
  console.error(`\n${problems.length} problem(s):`)
  for (const problem of problems) console.error(` - ${problem}`)
  process.exit(1)
}
console.log('\nquota e2e passed')
