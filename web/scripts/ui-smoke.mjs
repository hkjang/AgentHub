import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { mkdir } from 'node:fs/promises'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? process.env.AGENTHUB_TEST_ADMIN ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'

const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor()

  await page.keyboard.press('Control+K')
  await page.getByRole('dialog', { name: '빠른 이동' }).waitFor()
  await page.keyboard.press('Escape')

  await page.getByRole('link', { name: 'Agent Catalog', exact: true }).click()
  await page.getByRole('heading', { name: 'Agent Catalog' }).waitFor()
  await page.locator('.template-card').first().click()
  const drawer = page.getByRole('dialog', { name: '새 Agent 만들기' })
  await drawer.waitFor()
  const nameInput = drawer.getByLabel(/Agent 이름/)
  await nameInput.fill('UI Input Verification')
  if (await nameInput.inputValue() !== 'UI Input Verification') throw new Error('Agent name input is not editable')
  await drawer.getByRole('button', { name: '닫기' }).click()

  await page.getByRole('link', { name: 'Control Center' }).click()
  await page.getByRole('heading', { name: 'Agent Control Center' }).waitFor()
  await page.getByText('Server Logs', { exact: true }).waitFor()

  await page.locator('.profile-button').click()
  await page.getByText(/AgentHub/).last().waitFor()
  await mkdir('../coverage', { recursive: true })
  await page.screenshot({ path: '../coverage/dashboard.png', fullPage: true })

  const mobile = await browser.newPage({ viewport: { width: 390, height: 844 } })
  await mobile.goto(baseURL, { waitUntil: 'networkidle' })
  await mobile.getByLabel('아이디').fill(username)
  await mobile.getByLabel('비밀번호').fill(password)
  await mobile.getByRole('button', { name: '로그인', exact: true }).click()
  await mobile.getByRole('button', { name: '메뉴 열기' }).waitFor()
  await mobile.getByRole('button', { name: '메뉴 열기' }).click()
  await mobile.getByRole('navigation', { name: '주 메뉴' }).waitFor()
  await mobile.locator('.sidebar.sidebar-open').waitFor()
  await mobile.waitForTimeout(250)
  await mobile.screenshot({ path: '../coverage/mobile.png', fullPage: true })
  await mobile.close()
  console.log('AgentHub UI smoke test passed')
} finally {
  await browser.close()
}
