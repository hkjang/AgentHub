const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)
const globals = [
  ['policy', '/api/v1/admin/policy', 'document', value => isObject(value) && Array.isArray(value.rules)],
  ['dlp', '/api/v1/admin/dlp', 'settings', value => isObject(value) && typeof value.enabled === 'boolean'
    && (!Object.hasOwn(value, 'classes') || isObject(value.classes))],
  ['sessionGateway', '/api/v1/admin/settings', 'sessionGateway', isObject],
  ['tracking', '/api/v1/admin/settings', 'tracking', isObject],
]

// All four API values must be restorable before any work starts. There is no
// setting deletion API, so an absent gateway/tracker cannot safely be seeded.
export async function withGuideSettings(call, { seed, capture, captureTracking, skipSeed = false, note = () => {} }) {
  if (skipSeed) return await capture()

  const before = []
  for (const [name, readPath, key, valid] of globals) {
    let response
    try {
      response = await call('GET', readPath)
    } catch (cause) {
      throw new Error(`${name} backup failed: ${cause.message}`, { cause })
    }
    if (!Number.isInteger(response?.status) || response.status < 200 || response.status >= 300) {
      throw new Error(`${name} backup failed: HTTP ${response?.status ?? 'unknown'}`)
    }
    if (response.parseError) throw new Error(`${name} backup failed: invalid JSON`)
    if (!isObject(response.body) || !valid(response.body[key])) {
      throw new Error(`${name} backup failed: missing or invalid ${key}`)
    }
    const value = structuredClone(response.body[key])
    before.push(readPath === '/api/v1/admin/settings'
      ? [`${readPath}/${key}`, { value }]
      : [readPath, value])
  }

  let failedWork = false
  try {
    await seed()
    await capture()
    await captureTracking()
  } catch (error) {
    failedWork = true
    throw error
  } finally {
    // A failed write may already have reached the server. Keep all work inside
    // this restoration boundary, but never enter it with an incomplete backup.
    // One call that rejects — a restarted control plane, a closed page — must
    // not leave the settings behind it on the demo values, so every restoration
    // is attempted and its failure recorded rather than thrown from the loop.
    const failed = []
    for (const [path, value] of before) {
      let ok = false, detail
      try {
        const restored = await call('PUT', path, value)
        ok = restored.status >= 200 && restored.status < 300
        detail = `HTTP ${restored.status}`
      } catch (cause) {
        detail = cause.message
      }
      note(`복원 ${path}`, ok, detail)
      if (!ok) failed.push(`${path} (${detail})`)
    }
    // What the run was doing when it broke matters more than the way out, so a
    // failure here is only raised when the work itself got through.
    if (failed.length && !failedWork) throw new Error(`복원 실패: ${failed.join(', ')}`)
  }
}
