import assert from 'node:assert/strict'
import test from 'node:test'
import { withRuntimeSettings } from './runtime-settings-check.mjs'

const original = [{ runtimeType: 'opencode', enabled: false, description: 'keep',
  config: { theme: { colors: ['dark'], contrast: false } }, env: { TZ: 'UTC' } }]
const response = profiles => ({ status: 200, body: { settings: { profiles },
  suggestions: ['not writable'], runtimes: [], targets: ['env'] } })

for (const [name, before] of [
  ['HTTP 401', { status: 401 }], ['HTTP 500', { status: 500 }],
  ['redirect', { status: 302 }], ['missing status', { body: {} }],
  ['malformed JSON', { ...response(original), parseError: true }],
  ...[null, [], {}, { settings: null }, { settings: [] }, { settings: {} },
    { settings: { profiles: null } }, { settings: { profiles: {} } },
    { settings: { profiles: '[]' } }, { profiles: original }]
    .map((body, index) => [`invalid shape ${index}`, { status: 200, body }]),
]) {
  test(`backup ${name} prevents every PUT and callback`, async () => {
    const calls = []
    await assert.rejects(withRuntimeSettings(async (method, path) => {
      calls.push([method, path])
      return before
    }, () => assert.fail('callback must not run')), /backup/)
    assert.deepEqual(calls, [['GET', '/api/v1/admin/runtime-settings']])
  })
}

test('backup network failure prevents the callback and writes', async () => {
  const failure = new Error('connection lost')
  let calls = 0
  await assert.rejects(withRuntimeSettings(async method => {
    calls++
    assert.equal(method, 'GET')
    throw failure
  }, () => assert.fail('callback must not run')), error => error === failure)
  assert.equal(calls, 1)
})

for (const profiles of [original, []]) {
  for (const throws of [false, true]) {
    test(`restores a private deep copy (${profiles.length} profiles, throws=${throws})`, async () => {
      const before = response(structuredClone(profiles))
      const writes = []
      const failure = new Error('inspection failed')
      const call = async (method, path, body) => {
        assert.equal(path, '/api/v1/admin/runtime-settings')
        if (method === 'GET') return before
        writes.push(body)
        return { status: 200 }
      }
      const run = withRuntimeSettings(call, async (...args) => {
        assert.deepEqual(args, []) // The private backup never reaches the callback.
        if (profiles.length) before.body.settings.profiles[0].config.theme.colors.push('changed')
        before.body.settings.profiles.push({ runtimeType: 'langflow' })
        await call('PUT', '/api/v1/admin/runtime-settings', { profiles: [] })
        if (throws) throw failure
      })
      if (throws) await assert.rejects(run, error => error === failure)
      else await run
      assert.deepEqual(writes, [{ profiles: [] }, { profiles }])
    })
  }
}

for (const network of [false, true]) {
  test(`a temporary PUT failure still restores (network=${network})`, async () => {
    const writes = []
    const failure = new Error('temporary update failed')
    const call = async (method, path, body) => {
      if (method === 'GET') return response(original)
      writes.push(body)
      if (writes.length === 1) {
        if (network) throw failure
        return { status: 503 }
      }
      return { status: 200 }
    }
    await assert.rejects(withRuntimeSettings(call, async () => {
      const result = await call('PUT', '/api/v1/admin/runtime-settings', { profiles: [] })
      if (result.status !== 200) throw failure
    }), error => error === failure)
    assert.deepEqual(writes, [{ profiles: [] }, { profiles: original }])
  })

  for (const checkFails of [false, true]) {
    test(`restore failure propagates (network=${network}, checkFails=${checkFails})`, async () => {
      const checkError = new Error('check failed')
      const restoreError = new Error('restore connection lost')
      await assert.rejects(withRuntimeSettings(async method => {
        if (method === 'GET') return response(original)
        if (network) throw restoreError
        return { status: 503 }
      }, () => { if (checkFails) throw checkError }), error => {
        const restore = checkFails ? error.errors?.[1] : error
        if (checkFails) {
          assert.ok(error instanceof AggregateError)
          assert.equal(error.errors[0], checkError)
        }
        if (network) assert.equal(restore, restoreError)
        else assert.match(restore.message, /restore.*HTTP 503/)
        return true
      })
    })
  }
}

test('even a falsy thrown value is retained with a restore error', async () => {
  await assert.rejects(withRuntimeSettings(async method => method === 'GET'
    ? response(original) : { status: 500 }, () => { throw undefined }), error => {
    assert.ok(error instanceof AggregateError)
    assert.equal(error.errors[0], undefined)
    assert.match(error.errors[1].message, /restore.*HTTP 500/)
    return true
  })
})
