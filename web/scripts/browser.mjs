// Finds a Chromium the e2e suites can drive.
//
// CHROMIUM_PATH wins when it is set and real. Otherwise the Playwright browser
// cache is searched, newest build first, because that is what `npx playwright
// install` leaves behind and it moves with every Playwright upgrade. A distro
// Chromium is the last resort. Guessing one fixed path meant every machine
// without it failed at launch with an error about the path rather than about
// the missing browser.
import { existsSync, readdirSync } from 'node:fs'
import { homedir } from 'node:os'
import { join } from 'node:path'

const SYSTEM_PATHS = [
  '/snap/bin/chromium',
  '/usr/bin/chromium',
  '/usr/bin/chromium-browser',
  '/usr/bin/google-chrome',
]

function playwrightBuilds() {
  const cache = process.env.PLAYWRIGHT_BROWSERS_PATH || join(homedir(), '.cache', 'ms-playwright')
  if (!existsSync(cache)) return []
  const builds = readdirSync(cache)
    .filter((entry) => entry.startsWith('chromium-'))
    // Highest build number first: it is the one the installed Playwright wants.
    .sort((a, b) => Number(b.split('-')[1] ?? 0) - Number(a.split('-')[1] ?? 0))
  return builds.flatMap((build) => [
    join(cache, build, 'chrome-linux64', 'chrome'),
    join(cache, build, 'chrome-linux', 'chrome'),
  ])
}

/** Absolute path to a usable Chromium, or '' to let Playwright pick its own. */
export function chromiumPath() {
  const configured = (process.env.CHROMIUM_PATH ?? '').trim()
  if (configured) {
    if (existsSync(configured)) return configured
    console.warn(`CHROMIUM_PATH=${configured} does not exist; falling back to a detected browser`)
  }
  for (const candidate of [...playwrightBuilds(), ...SYSTEM_PATHS]) {
    if (existsSync(candidate)) return candidate
  }
  // Playwright's own default is still better than failing here: it may have a
  // browser registered that none of these paths describe.
  return ''
}
