// The rules that decide whether the console tries a silent sign-in.
//
// They run in the browser and nowhere else, so this stands in a window with a
// sessionStorage and a location the test controls, and asks the same module
// the shell imports. No provider, no server: the point of each case is that a
// particular guard says "no" on its own.
//
//   node --test scripts/silent-sso.test.mjs        (from web/)

import assert from 'node:assert/strict'
import { beforeEach, describe, it } from 'node:test'

function memoryStorage() {
  const items = new Map()
  return {
    getItem: (key) => (items.has(key) ? items.get(key) : null),
    setItem: (key, value) => { items.set(key, String(value)) },
    removeItem: (key) => { items.delete(key) },
  }
}

// A storage that throws on every access, which is what a private mode or a
// browser with site data blocked does.
const blockedStorage = {
  getItem() { throw new DOMException('blocked', 'SecurityError') },
  setItem() { throw new DOMException('blocked', 'SecurityError') },
  removeItem() { throw new DOMException('blocked', 'SecurityError') },
}

const on = { local: true, oidc: true, oidcLabel: 'Keycloak SSO', autoLogin: true }
const at = (pathname, search = '') => ({ pathname, search })

let assigned
globalThis.window = { sessionStorage: memoryStorage(), location: { assign: (url) => { assigned = url } } }
globalThis.DOMException ??= class extends Error {}

const sso = await import('../src/silentSso.ts')

describe('whether to try', () => {
  beforeEach(() => { window.sessionStorage = memoryStorage(); assigned = undefined })

  it('tries once, from an ordinary page, when the administrator turned it on', () => {
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs')), true)
  })

  it('does nothing while auto-login is off, whatever the address says', () => {
    assert.equal(sso.shouldAttemptSilentSso({ ...on, autoLogin: false }, at('/runs')), false)
    assert.equal(sso.shouldAttemptSilentSso({ ...on, oidc: false }, at('/runs')), false)
    assert.equal(sso.shouldAttemptSilentSso({ local: true, oidc: true, oidcLabel: 'SSO' }, at('/runs')), false)
  })

  it('tries only once per tab session', () => {
    sso.beginSilentSso('/runs')
    assert.match(assigned, /^\/api\/v1\/auth\/oidc\/start\?prompt=none&return_to=%2Fruns$/)
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs')), false, 'a second attempt in the same tab is the loop')
  })

  it('stops at the marker the callback leaves in the address, even with storage wiped', () => {
    assert.equal(sso.shouldAttemptSilentSso(on, at('/login', '?sso=none')), false)
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs', '?sso=none')), false)
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs', '?sso=error')), false)
  })

  it('does not sign somebody back in after they signed out on purpose', () => {
    sso.markSignedOut()
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs')), false)
  })

  it('lifts the suppression once a session exists again', () => {
    sso.markSignedOut()
    sso.clearSilentSsoState()
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs')), true)
  })

  it('treats storage it cannot read as already attempted', () => {
    window.sessionStorage = blockedStorage
    assert.equal(sso.shouldAttemptSilentSso(on, at('/runs')), false, 'reading a thrown exception as "not yet" is exactly the loop')
    assert.doesNotThrow(() => sso.beginSilentSso('/runs'), 'the move itself must not fail when the flag cannot be written')
  })

  it('never starts from the login, callback, API, MCP or health addresses', () => {
    for (const pathname of ['/login', '/api/v1/auth/oidc/callback', '/api/v1/me', '/mcp', '/healthz', '/readyz']) {
      assert.equal(sso.shouldAttemptSilentSso(on, at(pathname)), false, pathname)
    }
  })
})

describe('the way back', () => {
  it('keeps a deep link on this origin, query and hash included', () => {
    assert.equal(sso.safeReturnTo('/runs?tab=history#latest'), '/runs?tab=history#latest')
  })

  it('replaces anything that could be another host with the front page', () => {
    for (const value of ['//evil.example/', 'https://evil.example/', '/\\evil.example', 'runs', '', '/runs\r\nX']) {
      assert.equal(sso.safeReturnTo(value), '/', JSON.stringify(value))
    }
  })

  it('does not return to the login screen or a server address', () => {
    assert.equal(sso.safeReturnTo('/login?sso=none'), '/')
    assert.equal(sso.safeReturnTo('/api/v1/me'), '/')
  })

  it('is carried to the start route encoded', () => {
    assert.equal(sso.silentSsoStartUrl('/runs?tab=history#latest'), '/api/v1/auth/oidc/start?prompt=none&return_to=%2Fruns%3Ftab%3Dhistory%23latest')
  })
})
