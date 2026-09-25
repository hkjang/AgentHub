import test from 'node:test'
import assert from 'node:assert/strict'
import { readFile } from 'node:fs/promises'
import vm from 'node:vm'

const source = await readFile(new URL('./guide-shots.mjs', import.meta.url), 'utf8')
// The real script is cut out and run, so anything call depends on — the request
// deadline included — has to sit inside these two anchors.
const boundary = source.slice(source.indexOf('  const requestTimeoutMs ='), source.indexOf('  if (problems.length)'))
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
// A stalled control plane accepts the request and then says nothing. Only the
// deadline the caller attached can end it, so the stub settles on abort alone —
// a request sent without one is reported rather than left pending forever,
// which would hang this test file instead of failing it.
function stalls(options) {
  return new Promise((_, reject) => {
    if (!options.signal) {
      reject(new Error('the request was sent without a deadline and would never return'))
      return
    }
    // A deadline's own timer does not hold the Node event loop open, and here
    // nothing else is running, so the wait needs something that does.
    const alive = setInterval(() => {}, 5)
    options.signal.addEventListener('abort', () => { clearInterval(alive); reject(options.signal.reason) })
  })
}
async function run({ bodies = originals(), failPath, failAt, failure, failWrite, failWriteAt, writeFailure,
  hangPath, hangAt, hangWrite, hangInSeed, skip, callbackError } = {}) {
  const requests = [], callbacks = [], notes = [], deadlines = []
  const context = {
    ...helper, structuredClone, AbortSignal,
    process: { env: { GUIDE_SKIP_SEED: skip, AGENTHUB_GUIDE_REQUEST_TIMEOUT_MS: '50' } },
    document: { cookie: '' },
    page: { evaluate: (fn, args) => fn(args) },
    fetch: async (path, options) => {
      requests.push({ path, method: options.method, body: options.body && JSON.parse(options.body) })
      deadlines.push(Boolean(options.signal))
      if (options.method !== 'GET') {
        if (path === hangWrite) return await stalls(options)
        const writes = requests.filter(r => r.method !== 'GET').length
        const broken = (failWrite === undefined || path === failWrite) && (failWriteAt === undefined || writes === failWriteAt)
        if (broken && writeFailure === 'network') throw new Error('write disconnected')
        return { status: broken && writeFailure === 'http' ? 503 : 200, text: async () => '{}' }
      }
      if (path === hangInSeed) return await stalls(options)
      if (path === hangPath && (hangAt === undefined || requests.length === hangAt)) return await stalls(options)
      const fails = path === failPath && (failAt === undefined || requests.length === failAt)
      if (fails && failure === 'network') throw new Error('network disconnected')
      return { status: fails && failure === 'http' ? 503 : 200,
        text: async () => fails && failure === 'json' ? '{invalid' : JSON.stringify(bodies[paths.indexOf(path)]) }
    },
    note: (...args) => notes.push(args),
    seed: async (helpers) => {
      callbacks.push('seed')
      if (hangInSeed) await helpers.get(hangInSeed)
      if (callbackError === 'seed') throw new Error('seed failed')
    },
    capture: async () => { callbacks.push('capture'); if (callbackError === 'capture') throw new Error('capture failed') },
    captureTracking: async () => { callbacks.push('tracking'); if (callbackError === 'tracking') throw new Error('tracking failed') },
  }
  let error
  try { await vm.runInNewContext(`(async () => {${boundary}})()`, context) } catch (caught) { error = caught }
  return { requests, callbacks, notes, deadlines, error }
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

// A control plane that answers nothing must end the shoot, not own it.
const names = ['policy', 'dlp', 'sessionGateway', 'tracking']
for (const [index, key] of keys.entries()) {
  test(`${key}: a backup that never answers blocks all writes and callbacks`, async () => {
    const result = await run({ hangPath: paths[index], hangAt: index + 1 })
    blocked(result, new RegExp(`${names[index]} backup failed`))
    assert.match(result.error.message, new RegExp(`GET ${paths[index]} 가 50ms 안에 응답하지 않음`))
  })
}
test('a seeding request that never answers still restores all four settings', async () => {
  const bodies = originals()
  const result = await run({ bodies, hangInSeed: '/api/v1/tasks' })
  assert.match(result.error.message, /GET \/api\/v1\/tasks 가 50ms 안에 응답하지 않음/)
  assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
  assert.deepEqual(result.callbacks, ['seed'])
  assert.deepEqual(failedNotes(result), [])
})
test('a restoration that never answers is reported and the other three still run', async () => {
  const bodies = originals()
  const result = await run({ bodies, hangWrite: paths[0] })
  assert.deepEqual(result.requests.filter(r => r.method !== 'GET'), restorations(bodies))
  assert.deepEqual(failedNotes(result), [`복원 ${paths[0]}`])
  assert.match(result.error.message, new RegExp(`복원 실패.*PUT ${paths[0]} 가 50ms 안에 응답하지 않음`))
})
test('every request carries a deadline and a deployment that answers never sees one', async () => {
  const result = await run()
  assert.ifError(result.error)
  assert.equal(result.deadlines.length, 8)
  assert.deepEqual(result.deadlines.filter(Boolean).length, 8)
  assert.deepEqual(result.callbacks, ['seed', 'capture', 'tracking'])
})

// settle owns a 180s deadline of its own, and one poll that dies must not spend
// it: the queue screens are the point of the wait, and no worker already ends
// in the same { done: false } the caller reads.
const settleSource = source.slice(source.indexOf('async function settle('), source.indexOf('/** capture photographs'))
async function runSettle(reply) {
  let clock = 0
  const asked = []
  const context = {
    Date: { now: () => clock },
    setTimeout: (fn, ms) => { clock += ms; fn() },
    get: async () => {
      asked.push('/api/v1/tasks')
      const next = reply(asked.length)
      if (next instanceof Error) throw next
      return next
    },
  }
  const done = await vm.runInNewContext(`(async () => {${settleSource}\nreturn await settle(get, 180000)})()`, context)
  return { done, asked }
}
test('settle keeps polling to its own deadline when every poll times out', async () => {
  const timeout = () => new Error('GET /api/v1/tasks 가 50ms 안에 응답하지 않음')
  const { done, asked } = await runSettle(timeout)
  assert.equal(done.done, false)
  assert.match(done.detail, /^180초 안에 끝나지 않음 — 워커가/)
  assert.equal(asked.length, 90)
})
test('settle still reports the tasks once polling recovers', async () => {
  const { done, asked } = await runSettle(n => n <= 2
    ? new Error('GET /api/v1/tasks 가 50ms 안에 응답하지 않음')
    : { body: { items: [{ status: 'succeeded' }, { status: 'succeeded' }] } })
  assert.equal(done.done, true)
  assert.equal(done.detail, 'succeeded, succeeded')
  assert.equal(asked.length, 3)
})
