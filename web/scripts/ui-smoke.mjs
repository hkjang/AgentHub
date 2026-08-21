// Desktop and mobile smoke pass: signs in, opens the command palette, walks a
// couple of menus, edits one form field and captures both viewports.
//
// ui-e2e.mjs is the full route walk; this one exists for the shell itself — the
// mobile drawer in particular, which no other suite drives.
import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
import { mkdir } from 'node:fs/promises'

const baseURL = process.env.AGENTHUB_TEST_URL ?? 'http://localhost:18080'
const executablePath = chromiumPath()
const username = process.env.AGENTHUB_TEST_USER ?? process.env.AGENTHUB_TEST_ADMIN ?? 'admin'
const password = process.env.AGENTHUB_TEST_PASSWORD ?? 'local-development-password'

async function signIn(page) {
  await page.goto(baseURL, { waitUntil: 'networkidle' })
  await page.getByLabel('아이디').fill(username)
  await page.getByLabel('비밀번호').fill(password)
  await page.getByRole('button', { name: '로그인', exact: true }).click()
}

const browser = await chromium.launch({ executablePath, headless: true, args: ['--no-sandbox'] })
try {
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } })
  await signIn(page)
  await page.getByRole('heading', { name: new RegExp(`${username}님`) }).waitFor({ timeout: 20000 })

  await page.keyboard.press('Control+K')
  await page.getByRole('dialog', { name: '빠른 이동' }).waitFor()
  await page.keyboard.press('Escape')

  await page.getByRole('link', { name: '에이전트 카탈로그', exact: true }).click()
  await page.getByRole('heading', { name: /에이전트 카탈로그/ }).waitFor()
  await page.locator('.template-card').first().click()
  const drawer = page.getByRole('dialog', { name: '새 에이전트 만들기' })
  await drawer.waitFor()
  const nameInput = drawer.getByLabel(/에이전트 이름/)
  await nameInput.fill('UI Input Verification')
  if ((await nameInput.inputValue()) !== 'UI Input Verification') throw new Error('agent name input is not editable')
  await drawer.getByRole('button', { name: '닫기' }).click()

  await page.getByRole('link', { name: '로그 · 감사', exact: true }).click()
  await page.getByRole('heading', { name: /로그 · 감사/ }).waitFor()
  // The active menu has to follow the route rather than stay on the last one.
  const highlighted = await page.locator('.nav-link.active').allInnerTexts()
  if (highlighted.length !== 1 || highlighted[0].trim() !== '로그 · 감사') {
    throw new Error(`unexpected active menu: ${highlighted.map((t) => t.trim()).join(', ') || 'none'}`)
  }

  await page.locator('.profile-button').click()
  await page.locator('.profile-menu').waitFor()
  await mkdir('../coverage', { recursive: true })
  await page.screenshot({ path: '../coverage/dashboard.png', fullPage: true })

  const mobile = await browser.newPage({ viewport: { width: 390, height: 844 } })
  await signIn(mobile)
  await mobile.getByRole('button', { name: '메뉴 열기' }).waitFor({ timeout: 20000 })
  await mobile.getByRole('button', { name: '메뉴 열기' }).click()
  await mobile.getByRole('navigation', { name: '주 메뉴' }).waitFor()
  await mobile.locator('.sidebar.sidebar-open').waitFor()
  await mobile.waitForTimeout(250)
  await mobile.screenshot({ path: '../coverage/mobile.png', fullPage: true })
  // Choosing a menu on a phone has to navigate and get the drawer out of the way.
  await mobile.getByRole('link', { name: '내 에이전트', exact: true }).click()
  await mobile.getByRole('heading', { name: /내 에이전트/ }).waitFor()
  await mobile.locator('.sidebar.sidebar-open').waitFor({ state: 'detached', timeout: 5000 })
  await mobile.close()
  console.log('AgentHub UI smoke test passed')
} finally {
  await browser.close()
}
