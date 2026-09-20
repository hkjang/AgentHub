const isObject = value => value !== null && typeof value === 'object' && !Array.isArray(value)

function requireSuccess(response, operation) {
  if (!Number.isInteger(response?.status) || response.status < 200 || response.status >= 300) {
    throw new Error(`sessionGateway ${operation} failed: HTTP ${response?.status ?? 'unknown'}`)
  }
}

// The caller supplies its authenticated transport; no browser is started here.
// Keep the backup private so a check cannot accidentally mutate what we restore.
export async function withSessionGateway(call, check) {
  const response = await call('GET', '/api/v1/admin/settings')
  requireSuccess(response, 'backup')
  if (response.parseError || !isObject(response.body)) {
    throw new Error('sessionGateway backup failed: invalid settings response')
  }
  if (!Object.hasOwn(response.body, 'sessionGateway')) {
    return { skipped: true, reason: 'sessionGateway 설정이 없어 세션 열기 검사를 건너뜁니다 (설정 삭제 API 없음)' }
  }
  if (!isObject(response.body.sessionGateway)) {
    throw new Error('sessionGateway backup failed: invalid setting object')
  }
  const backup = structuredClone(response.body.sessionGateway)
  const save = async (value, operation) => {
    requireSuccess(await call('PUT', '/api/v1/admin/settings/sessionGateway', { value }), operation)
  }
  let checkError
  try {
    await check({ gateway: structuredClone(backup), setGateway: value => save(value, 'temporary update') })
  } catch (error) {
    checkError = error
    throw error
  } finally {
    // Even a failed PUT may have reached the server before the connection died.
    try {
      await save(backup, 'restore')
    } catch (error) {
      if (checkError) throw new AggregateError([checkError, error], 'sessionGateway check and restore failed')
      throw error
    }
  }
  return { skipped: false }
}
