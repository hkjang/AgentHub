type ErrorResponse = { error?: { code?: string; message?: string } }

/**
 * Fired when the portal session is gone — expired, revoked, or dropped by a
 * restart. Until the shell listened for this, every screen just showed
 * "로그인이 필요합니다." and stayed there: the session was over but the app never
 * said so, and only a manual reload took the user back to the sign-in page.
 */
export const UNAUTHORIZED_EVENT = 'agenthub:unauthorized'

function reportUnauthorized(url: string) {
  // A rejected sign-in is a wrong password, not an expired session, and a 401
  // relayed from a runtime's own API belongs to that runtime.
  if (url.startsWith('/api/v1/auth/login') || url.includes('/session/')) return
  window.dispatchEvent(new Event(UNAUTHORIZED_EVENT))
}

function cookie(name: string) {
  return document.cookie.split(';').map((part) => part.trim()).find((part) => part.startsWith(`${name}=`))?.slice(name.length + 1) ?? ''
}

export async function request<T>(url: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  const method = (options.method ?? 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', decodeURIComponent(cookie('agenthub_csrf')))
  const response = await fetch(url, { ...options, headers, credentials: 'same-origin' })
  if (!response.ok) {
    if (response.status === 401) reportUnauthorized(url)
    let payload: ErrorResponse = {}
    try { payload = await response.json() } catch { /* response is not JSON */ }
    throw new Error(payload.error?.message ?? `요청을 처리하지 못했습니다. (${response.status})`)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

/** Fetches a non-JSON body, such as an exported YAML definition. */
async function requestText(url: string, options: RequestInit = {}): Promise<string> {
  const headers = new Headers(options.headers)
  const method = (options.method ?? 'GET').toUpperCase()
  if (!['GET', 'HEAD', 'OPTIONS'].includes(method)) headers.set('X-CSRF-Token', decodeURIComponent(cookie('agenthub_csrf')))
  const response = await fetch(url, { ...options, headers, credentials: 'same-origin' })
  if (!response.ok) {
    if (response.status === 401) reportUnauthorized(url)
    let payload: ErrorResponse = {}
    try { payload = await response.json() } catch { /* response is not JSON */ }
    throw new Error(payload.error?.message ?? `요청을 처리하지 못했습니다. (${response.status})`)
  }
  return response.text()
}

export const api = {
  get: <T>(url: string) => request<T>(url),
  /** GET returning the raw body. */
  text: (url: string) => requestText(url),
  /** POST of a raw body, for uploads that are not JSON documents. */
  postText: <T>(url: string, body: string) =>
    request<T>(url, { method: 'POST', body, headers: { 'Content-Type': 'application/yaml' } }),
  post: <T>(url: string, value?: unknown) => request<T>(url, { method: 'POST', body: value === undefined ? undefined : JSON.stringify(value) }),
  put: <T>(url: string, value: unknown) => request<T>(url, { method: 'PUT', body: JSON.stringify(value) }),
  delete: <T>(url: string) => request<T>(url, { method: 'DELETE' })
}
