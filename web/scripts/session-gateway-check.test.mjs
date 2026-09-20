import assert from 'node:assert/strict'
import test from 'node:test'
import { withSessionGateway } from './session-gateway-check.mjs'

const original = { enabled: true, baseDomain: 'original.example', scheme: 'https', sessionHours: 4, future: { values: ['keep'] } }

for (const throws of [false, true]) {
  test(`restores the complete backup after ${throws ? 'a thrown check' : 'success'}`, async () => {
    const writes = []
    const call = async (method, path, body) => {
      if (method === 'GET') {
        assert.equal(path, '/api/v1/admin/settings')
        return { status: 200, body: { sessionGateway: structuredClone(original) } }
      }
      assert.equal(path, '/api/v1/admin/settings/sessionGateway')
      writes.push(structuredClone(body.value))
      return { status: 200, body: { saved: true } }
    }
    const run = withSessionGateway(call, async ({ gateway, setGateway }) => {
      gateway.future.values.push('changed')
      await setGateway({ ...gateway, enabled: false })
      if (throws) throw new Error('check failed')
    })
    if (throws) await assert.rejects(run, /check failed/)
    else assert.deepEqual(await run, { skipped: false })
    assert.equal(writes.length, 2)
    assert.deepEqual(writes.at(-1), original)
  })
}

for (const [label, response] of [
  ['read rejected', { status: 403, body: {} }],
  ['wrong endpoint', { status: 405, body: null }],
  ['null body', { status: 200, body: null }],
  ['array body', { status: 200, body: [] }],
  ['invalid JSON', { status: 200, body: { raw: 'not JSON' }, parseError: true }],
  ...[null, [], false, 'bad'].map(value => ['invalid gateway ' + JSON.stringify(value), { status: 200, body: { sessionGateway: value } }]),
]) {
  test(`${label} fails without writing`, async () => {
    let writes = 0
    await assert.rejects(withSessionGateway(async (method) => {
      if (method !== 'GET') writes++
      return response
    }, () => assert.fail('check must not run')), /sessionGateway/)
    assert.equal(writes, 0)
  })
}

test('missing key explicitly skips without creating an empty setting', async () => {
  let writes = 0
  const result = await withSessionGateway(async method => {
    if (method !== 'GET') writes++
    return { status: 200, body: {} }
  }, () => assert.fail('check must not run'))
  assert.equal(result.skipped, true)
  assert.match(result.reason, /sessionGateway/)
  assert.equal(writes, 0)
})

test('network read failure never writes', async () => {
  const methods = []
  await assert.rejects(withSessionGateway(async method => {
    methods.push(method)
    throw new Error('read failed')
  }, () => assert.fail('check must not run')), /read failed/)
  assert.deepEqual(methods, ['GET'])
})

for (const failAt of [1, 2]) {
  for (const network of [false, true]) {
    test(`${network ? 'network' : 'HTTP'} failure on ${failAt === 1 ? 'temporary' : 'restore'} PUT rejects`, async () => {
      const writes = []
      let checked = false
      await assert.rejects(withSessionGateway(async (method, path, body) => {
        if (method === 'GET') return { status: 200, body: { sessionGateway: original } }
        writes.push(structuredClone(body.value))
        if (writes.length === failAt) {
          if (network) throw new Error('connection lost')
          return { status: 500 }
        }
        return { status: 200 }
      }, async ({ setGateway }) => {
        await setGateway({ enabled: false })
        checked = true
      }), network ? /connection lost/ : /sessionGateway.*HTTP 500/)
      assert.equal(checked, failAt === 2)
      assert.equal(writes.length, 2)
      assert.deepEqual(writes.at(-1), original)
    })
  }
}

test('both check and restore failures remain visible', async () => {
  await assert.rejects(withSessionGateway(async method => {
    if (method === 'GET') return { status: 200, body: { sessionGateway: original } }
    return { status: 500 }
  }, () => { throw new Error('check failed') }), error => {
    assert.ok(error instanceof AggregateError)
    assert.match(error.errors[0].message, /check failed/)
    assert.match(error.errors[1].message, /restore.*HTTP 500/)
    return true
  })
})
