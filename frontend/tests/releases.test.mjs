import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import test from 'node:test'
import assert from 'node:assert/strict'
import ts from 'typescript'

test('release actions use the pipeline and leave operator/approver to the server', async () => {
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
  await exports.startRelease('v1', 'tenant-a')
  assert.equal(calls[0].url, '/pipeline/v1/releases')
  assert.deepEqual(JSON.parse(calls[0].options.body), { modelVersionId: 'v1', tenantId: 'tenant-a' })
  await exports.approveRelease('release/a', true)
  assert.equal(calls[1].url, '/pipeline/v1/releases/release%2Fa/approval')
  assert.equal(JSON.parse(calls[1].options.body).approved, true)
  assert.equal(JSON.parse(calls[1].options.body).approver, undefined)
  assert.equal(exports.releaseVersion, undefined)
})
