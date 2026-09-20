const path = '/api/v1/admin/runtime-settings'
const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)

function requireSuccess(response, operation) {
  if (!Number.isInteger(response?.status) || response.status < 200 || response.status >= 300) {
    throw new Error(`runtimeSettings ${operation} failed: HTTP ${response?.status ?? 'unknown'}`)
  }
}

// Use the caller's authenticated transport. GET has an envelope, while PUT only
// accepts profiles. Keep a private deep copy before allowing any check to write.
export async function withRuntimeSettings(call, check) {
  const response = await call('GET', path)
  requireSuccess(response, 'backup')
  if (response.parseError || !isObject(response.body) || !isObject(response.body.settings)
    || !Array.isArray(response.body.settings.profiles)) {
    throw new Error('runtimeSettings backup failed: invalid settings.profiles response')
  }
  const backup = structuredClone(response.body.settings.profiles)
  let checkFailed = false
  let checkError
  try {
    await check()
  } catch (error) {
    checkFailed = true
    checkError = error
    throw error
  } finally {
    // A failed request may already have changed the server's settings.
    try {
      requireSuccess(await call('PUT', path, { profiles: backup }), 'restore')
    } catch (error) {
      if (checkFailed) throw new AggregateError([checkError, error], 'runtimeSettings check and restore failed')
      throw error
    }
  }
}
