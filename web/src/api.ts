type ErrorResponse = { error?: { code?: string; message?: string } }

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
