import { readFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import vm from 'node:vm'
import test from 'node:test'
import assert from 'node:assert/strict'
import ts from 'typescript'

const require = createRequire(import.meta.url)
const source = readFileSync(new URL('../src/composables/useAuth.ts', import.meta.url), 'utf8')
function harness(fetch) {
  const values = new Map()
  const exports = {}
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, {
    exports, require, fetch, Headers, Date,
    sessionStorage: { getItem: (key) => values.get(key), setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) }
  })
  return exports
}
const session = (accessToken, expiresAt = new Date(Date.now() + 3600000).toISOString()) => ({ accessToken, refreshToken: accessToken + '-refresh', tenantId: 'tenant-a', role: 'developer', expiresAt })
const reply = (data, status = 200) => new Response(JSON.stringify({ code: status === 200 ? 0 : 1, data }), { status })

test('API uses server-issued token and refuses external targets', async () => {
  const api = harness(async (url, options) => {
    if (url.endsWith('/login')) return reply(session('real-token'))
    assert.equal(options.headers.get('Authorization'), 'Bearer real-token')
    return reply({})
  })
  await api.useAuth().login('a@example.com', 'password', 'tenant-a')
  await api.authenticatedFetch('/api/v1/notebooks/workspace')
  await api.authenticatedFetch('/model-registry/v1/models')
  await api.authenticatedFetch('/gateway/v1/keys')
  await api.authenticatedFetch('/pipeline/v1/releases')
  await assert.rejects(api.authenticatedFetch('https://external.example/api/'))
})

test('expired concurrent requests share one refresh', async () => {
  let refreshes = 0
  const api = harness(async (url, options) => {
    if (url.endsWith('/login')) return reply(session('old', new Date(0).toISOString()))
    if (url.endsWith('/refresh')) { refreshes++; return reply(session('new')) }
    assert.equal(options.headers.get('Authorization'), 'Bearer new')
    return reply({})
  })
  await api.useAuth().login('a@example.com', 'password', 'tenant-a')
  await Promise.all([api.authenticatedFetch('/api/a'), api.authenticatedFetch('/api/b')])
  assert.equal(refreshes, 1)
})

test('failed old refresh cannot clear a newer login', async () => {
  let finish
  let logins = 0
  const api = harness(async (url) => {
    if (url.endsWith('/login')) return reply(++logins === 1 ? session('old', new Date(0).toISOString()) : session('new'))
    if (url.endsWith('/refresh')) return new Promise((resolve) => { finish = resolve })
    return reply({})
  })
  await api.useAuth().login('a@example.com', 'password', 'tenant-a')
  const pending = assert.rejects(api.authenticatedFetch('/api/a'))
  await api.useAuth().login('a@example.com', 'password', 'tenant-a')
  finish(reply(null, 401))
  await pending
  assert.equal(api.useAuth().isLoggedIn.value, true)
})
