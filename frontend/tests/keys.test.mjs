import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'
import assert from 'node:assert/strict'
import ts from 'typescript'

test('key creation uses explicit/session tenant and rotation cannot reassign it', async () => {
  const calls = []
  const exports = {}
  const source = readFileSync(new URL('../src/api/index.ts', import.meta.url), 'utf8')
  vm.runInNewContext(ts.transpileModule(source, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, {
    exports, URLSearchParams,
    require: () => ({ authenticatedFetch: async (url, options) => {
      calls.push({ url, options })
      return { json: async () => ({ code: 0, data: {} }) }
    } })
  })
  await exports.issueKey('tenant-a')
  assert.equal(JSON.parse(calls[0].options.body).tenantId, 'tenant-a')
  await exports.issueKey()
  assert.deepEqual(JSON.parse(calls[1].options.body), {})
  await exports.rotateKey('key-a')
  assert.equal(calls[2].url, '/gateway/v1/keys/key-a/rotate')
})
