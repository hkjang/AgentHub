/**
 * Silent sign-in: when the identity provider still holds the person's session,
 * the console sends them there with prompt=none and they come back signed in
 * without ever seeing the login screen.
 *
 * Everything in here is about not doing it twice. prompt=none never draws a
 * screen — either a code comes straight back or the provider answers
 * login_required — and retrying a refusal on the next page load would bounce
 * the browser between the two sites for as long as the person watched it
 * flicker. Three guards, each covering a way the others can be lost:
 *
 *   1. one attempt per tab session, remembered in sessionStorage;
 *   2. none at all after the person signed out on purpose;
 *   3. a marker in the address the callback lands on after a refusal.
 *
 * The move is a top-level navigation, not a hidden iframe: it works where
 * third-party cookies are blocked and does not care whether the provider
 * allows itself to be framed.
 */

export type AuthMethods = { local: boolean; oidc: boolean; oidcLabel: string; autoLogin?: boolean }

// sessionStorage rather than localStorage: a silent attempt belongs to this
// tab's browsing session, so a fresh tab tries again while a reload after a
// refusal does not.
const ATTEMPTED_KEY = 'agenthub.sso.silentAttempted'
const SIGNED_OUT_KEY = 'agenthub.sso.signedOut'

function readFlag(key: string): boolean {
  try {
    return window.sessionStorage.getItem(key) === 'true'
  } catch {
    // Private modes and blocked site data throw here. Reading that as "not yet
    // attempted" is exactly the loop; "already attempted" is the safe answer.
    return true
  }
}

function writeFlag(key: string, value: boolean) {
  try {
    if (value) window.sessionStorage.setItem(key, 'true')
    else window.sessionStorage.removeItem(key)
  } catch {
    /* nothing to do: readFlag already fails closed */
  }
}

/** Records that the person signed out on purpose, which suppresses auto-login. */
export function markSignedOut() {
  writeFlag(SIGNED_OUT_KEY, true)
  writeFlag(ATTEMPTED_KEY, true)
}

/** Lifts the suppression once a session exists again. */
export function clearSilentSsoState() {
  writeFlag(SIGNED_OUT_KEY, false)
  writeFlag(ATTEMPTED_KEY, false)
}

// Where a silent attempt must not start, and where it must not return to.
// The login address is where the loop comes from (the callback lives under
// /api/); the rest are answered by the server, not pages a person opens, so
// a console that somehow booted there has nothing to sign into.
const EXCLUDED_PREFIXES = ['/login', '/api/', '/mcp', '/healthz', '/readyz']

/** Whether a silent attempt may begin from this address. */
export function eligiblePath(pathname: string): boolean {
  return !EXCLUDED_PREFIXES.some((prefix) => pathname === prefix.replace(/\/$/, '') || pathname.startsWith(prefix))
}

/**
 * The place to come back to after signing in. Only a path on this origin is
 * accepted — a value beginning with "//" or a scheme is somebody else's site —
 * and the login screen itself is never a destination.
 */
export function safeReturnTo(value: string): string {
  if (!value.startsWith('/') || value.startsWith('//') || /[\\\r\n]/.test(value)) return '/'
  const path = value.split(/[?#]/, 1)[0]
  return eligiblePath(path) ? value : '/'
}

/**
 * Decides whether to try signing in without showing a login screen.
 *
 * Every guard answers "no" on its own: the setting is off, the person signed
 * out, this tab already tried, the address carries the callback's refusal
 * marker, or this is not a page a person navigated to.
 */
export function shouldAttemptSilentSso(methods: AuthMethods, location: { pathname: string; search: string }): boolean {
  if (!methods.oidc || !methods.autoLogin) return false
  if (!eligiblePath(location.pathname)) return false
  // The callback appends this marker when the provider had no session, so a
  // refusal is remembered even if sessionStorage was cleared in between.
  const sso = new URLSearchParams(location.search).get('sso')
  if (sso === 'none' || sso === 'error') return false
  if (readFlag(SIGNED_OUT_KEY)) return false
  if (readFlag(ATTEMPTED_KEY)) return false
  return true
}

/** The address the start route is asked at, marked silent and carrying the way back. */
export function silentSsoStartUrl(returnTo: string): string {
  return `/api/v1/auth/oidc/start?prompt=none&return_to=${encodeURIComponent(safeReturnTo(returnTo))}`
}

/** Sends the browser to the provider for one silent attempt. */
export function beginSilentSso(returnTo: string) {
  // Marked before the move, so a callback that never comes — a provider that
  // hangs, a tab closed mid-flight — still counts as the one attempt.
  writeFlag(ATTEMPTED_KEY, true)
  window.location.assign(silentSsoStartUrl(returnTo))
}
