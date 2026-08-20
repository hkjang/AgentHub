import { chromium } from 'playwright-core'
import { chromiumPath } from './browser.mjs'
const url = process.env.TERM_URL ?? 'http://127.0.0.1:7681/'
const browser = await chromium.launch({ executablePath: chromiumPath(), headless: true, args: ['--no-sandbox'] })
const context = await browser.newContext(process.env.TERM_USER ? { httpCredentials: { username: process.env.TERM_USER, password: process.env.TERM_PASS } } : {})
const page = await context.newPage()
const errors = []
page.on('console', (m) => { if (m.type() === 'error') errors.push(m.text()) })
page.on('websocket', (ws) => {
  console.log('ws opened:', ws.url())
  ws.on('close', () => console.log('ws CLOSED'))
  ws.on('socketerror', (e) => console.log('ws ERROR', e))
})
await page.goto(url, { waitUntil: 'networkidle' })
await page.waitForSelector('.xterm-screen', { timeout: 15000 })
await page.waitForTimeout(6000)
await page.keyboard.type('echo TYPING-WORKS')
await page.keyboard.press('Enter')
await page.waitForTimeout(3000)
const text = await page.evaluate(() => Array.from({length: 30}, (_, i) => window.term?.buffer.active.getLine(i)?.translateToString(true) ?? '').join('\n'))
console.log('--- screen ---'); console.log(text.replace(/\n{3,}/g, '\n'))
console.log('--- console errors:', errors.length ? errors : 'none')
await browser.close()
