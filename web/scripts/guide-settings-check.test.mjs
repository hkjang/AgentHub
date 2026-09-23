import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'

const source = await readFile(new URL('./guide-shots.mjs', import.meta.url), 'utf8')
const boundary = source.slice(source.indexOf('  const call ='), source.indexOf('  if (problems.length)'))
const imports = source.match(/import \{ withGuideSettings \} from '(.*?)'/)
const helper = imports ? await import(new URL(imports[1], import.meta.url)) : {}
const paths = ['/api/v1/admin/policy', '/api/v1/admin/dlp', '/api/v1/admin/settings', '/api/v1/admin/settings']
const keys = ['document', 'settings', 'sessionGateway', 'tracking']
function originals() {
  return [
    { document: { rules: [{ id: 'original', actions: ['tool.call'] }], defaultEffect: 'allow' } },
    { settings: { enabled: false, classes: { phone: 'audit' }, scanResponses: true } },
    { sessionGateway: { enabled: false, extra: { list: ['original'] } }, tracking: { enabled: false, extra: { list: ['original'] } } },
  ]
}
async function run({ bodies = originals(), failPath, failAt, failure, failWrite, failWriteAt, writeFailure, skip, callbackError } = {}) {
  const requests = [], callbacks = [], notes = []
  const context = {
    ...helper, structuredClone,
    process: { env: { GUIDE_SKIP_SEED: skip } },
    document: { cookie: '' },
    page: { evaluate: (fn, args) => fn(args) },
    fetch: async (path, options) => {
      requests.push({ path, method: options.method, body: options.body && JSON.parse(options.body) })
      if (options.method !== 'GET') {
        const writes = requests.filter(r => r.method !== 'GET').length
        const broken = (failWrite === undefined || path === failWrite) && (failWriteAt === undefined || writes === failWriteAt)
        if (broken && writeFailure === 'network') throw new Error('write disconnected')
        return { status: broken && writeFailure === 'http' ? 503 : 200, text: async () => '{}' }
      }
      const fails = path === failPath && (failAt === undefined || requests.length === failAt)
      if (fails && failure === 'network') throw new Error('network disconnected')
      return { status: fails && failure === 'http' ? 503 : 200,
        text: async () => fails && failure === 'json' ? '{invalid' : JSON.stringify(bodies[paths.indexOf(path)]) }
    },
    note: (...args) => notes.push(args),
    seed: async () => { callbacks.push('seed'); if (callbackError === 'seed') throw new Error('seed failed') },
    capture: async () => { callbacks.push('capture'); if (callbackError === 'capture') throw new Error('capture failed') },
    captureTracking: async () => { callbacks.push('tracking'); if (callbackError === 'tracking') throw new Error('tracking failed') },
  }
  let error
  try { await vm.runInNewContext(`(async () => {${boundary}})()`, context) } catch (caught) { error = caught }
  return { requests, callbacks, notes, error }
}
function blocked(result, reason) {
  assert.ok(result.error, 'must reject before work')
  assert.match(result.error.message, reason)
  assert.deepEqual(result.callbacks, [])
  assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), [])
}
for (const [index, key] of keys.entries()) {
  for (const failure of ['http', 'network', 'json']) {
    test(`${key}: ${failure} blocks all writes and callbacks`, async () => {
      blocked(await run({ failPath: paths[index], failAt: index + 1, failure }), /503|network|JSON/i)
    })
  }
  for (const invalid of [undefined, null, [], 'invalid']) {
    test(`${key}: rejects ${JSON.stringify(invalid)}`, async () => {
      const bodies = originals()
      bodies[Math.min(index, 2)][key] = invalid
      blocked(await run({ bodies }), new RegExp(key === 'document' ? 'policy' : key === 'settings' ? 'dlp' : key))
    })
  }
}
for (const [name, change] of [
  ['missing rules', b => delete b[0].document.rules],
  ['null rules', b => { b[0].document.rules = null }],
  ['object rules', b => { b[0].document.rules = {} }],
  ['missing enabled', b => delete b[1].settings.enabled],
  ['string enabled', b => { b[1].settings.enabled = 'false' }],
  ['null classes', b => { b[1].settings.classes = null }],
  ['array classes', b => { b[1].settings.classes = [] }],
]) test(name, async () => { const bodies = originals(); change(bodies); blocked(await run({ bodies }), /policy|dlp/) })
for (const invalid of [null, [], 'invalid']) {
  for (const index of [0, 1, 2]) test(`invalid envelope ${index}: ${JSON.stringify(invalid)}`, async () => {
    const bodies = originals(); bodies[index] = invalid
    blocked(await run({ bodies }), /backup/)
  })
}
for (const callbackError of [undefined, 'seed', 'capture', 'tracking']) {
  test(`exact restoration after ${callbackError ?? 'success'}`, async () => {
    const bodies = originals()
    const result = await run({ bodies, callbackError })
    assert.equal(result.error?.message, callbackError && `${callbackError} failed`)
    const writes = result.requests.filter(r => r.method !== 'GET')
    assert.deepEqual(writes, [
      { method: 'PUT', path: paths[0], body: bodies[0].document },
      { method: 'PUT', path: paths[1], body: bodies[1].settings },
      { method: 'PUT', path: `${paths[2]}/sessionGateway`, body: { value: bodies[2].sessionGateway } },
      { method: 'PUT', path: `${paths[2]}/tracking`, body: { value: bodies[2].tracking } },
    ])
    if (!callbackError) assert.deepEqual(result.callbacks, ['seed', 'capture', 'tracking'])
  })
}
function restorations(bodies) {
  return [
    { method: 'PUT', path: paths[0], body: bodies[0].document },
    { method: 'PUT', path: paths[1], body: bodies[1].settings },
    { method: 'PUT', path: `${paths[2]}/sessionGateway`, body: { value: bodies[2].sessionGateway } },
    { method: 'PUT', path: `${paths[2]}/tracking`, body: { value: bodies[2].tracking } },
  ]
}
const failedNotes = result => result.notes.filter(([, ok]) => !ok).map(([label]) => label)
for (const writeFailure of ['network', 'http']) {
  test(`a ${writeFailure} failure on the first restoration still restores the other three`, async () => {
    const bodies = originals()
    const result = await run({ bodies, failWrite: paths[0], writeFailure })
    assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
    assert.deepEqual(failedNotes(result), [`복원 ${paths[0]}`])
    assert.match(result.error.message, new RegExp(`복원 실패.*${paths[0]}`))
  })
  test(`a ${writeFailure} failure on the third restoration still restores the fourth`, async () => {
    const bodies = originals()
    const result = await run({ bodies, failWrite: `${paths[2]}/sessionGateway`, writeFailure })
    assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
    assert.deepEqual(failedNotes(result), [`복원 ${paths[2]}/sessionGateway`])
    assert.match(result.error.message, /복원 실패.*sessionGateway/)
  })
  test(`every restoration failing (${writeFailure}) is attempted and reported`, async () => {
    const bodies = originals()
    const result = await run({ bodies, writeFailure })
    assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
    assert.deepEqual(failedNotes(result), restorations(bodies).map(w => `복원 ${w.path}`))
    assert.deepEqual(result.callbacks, ['seed', 'capture', 'tracking'])
  })
  for (const callbackError of ['seed', 'capture', 'tracking']) {
    test(`${callbackError} failure is thrown, not the ${writeFailure} restoration failure`, async () => {
      const bodies = originals()
      const result = await run({ bodies, callbackError, failWrite: paths[1], writeFailure })
      assert.equal(result.error?.message, `${callbackError} failed`)
      assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
      assert.deepEqual(failedNotes(result), [`복원 ${paths[1]}`])
    })
  }
}
test('optional DLP classes and empty setting objects are valid', async () => {
  const result = await run({ bodies: [{ document: { rules: [] } }, { settings: { enabled: false } }, { sessionGateway: {}, tracking: {} }] })
  assert.ifError(result.error)
  assert.equal(result.requests.filter(r => r.method === 'PUT').length, 4)
})
test('skip runs capture only without any backup or restoration', async () => {
  const result = await run({ skip: '1', failure: 'network', failPath: paths[0] })
  assert.ifError(result.error)
  assert.deepEqual(result.callbacks, ['capture'])
  assert.deepEqual(result.requests, [])
})
for (const throws of [false, true]) test(`backups are deep copies, callback throws: ${throws}`, async () => {
  assert.ok(helper.withGuideSettings, 'script must import the tested boundary')
  const bodies = originals(), expected = structuredClone(bodies), writes = []
  const work = helper.withGuideSettings(async (method, path, body) => {
    if (method === 'GET') return { status: 200, body: bodies[paths.indexOf(path)] }
    writes.push(body)
    return { status: 200 }
  }, { seed: async () => {
    bodies[0].document.rules[0].actions.push('changed')
    bodies[1].settings.classes.phone = 'block'
    bodies[2].sessionGateway.extra.list.push('changed')
    bodies[2].tracking.extra.list.push('changed')
    if (throws) throw new Error('work failed')
  }, capture: async () => {}, captureTracking: async () => {} })
  if (throws) await assert.rejects(work, /work failed/)
  else await work
  assert.deepEqual(writes, [expected[0].document, expected[1].settings, { value: expected[2].sessionGateway }, { value: expected[2].tracking }])
})
