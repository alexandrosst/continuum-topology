import assert from 'node:assert/strict'
import { test } from 'node:test'
import { cleanRegistry, digestProblem, imageOk, imageProblems, ociBase, previewImage, registryProblem, tagProblem } from '../src/lib/image'

const D = 'sha256:' + '0123456789abcdef'.repeat(4)

test('image registry: the shapes the server accepts', () => {
  for (const ok of ['', 'myteam', 'docker.io/myteam', 'ghcr.io/me/x', 'reg.example.com:8443/team', 'localhost:5000', 'localhost:5000/a/b-c/d_e/f.g', '10.0.0.5:5000/team', 'reg.example.com/team/', '  reg.example.com/team  ']) {
    assert.equal(registryProblem(ok), '', ok)
  }
})

test('image registry: what the server refuses is refused while typing', () => {
  for (const bad of ['https://reg.example.com/x', 'Reg.Example.com/x', 'reg example.com', 'reg.example.com/x y', 'reg.example.com/x@sha256:abc', 'reg.example.com/x?y=1', 'reg.example.com/x#f', 'reg.example.com//x', '/x', 'reg.example.com:99999999/x', 'reg.example.com/-x', 'reg.example.com/x,y', 'reg.example.com/$(id)', '-reg.example.com', "reg.example.com/x'y", 'a'.repeat(256)]) {
    assert.notEqual(registryProblem(bad), '', bad)
  }
  assert.match(registryProblem('https://r.io/x'), /scheme/)
  assert.match(registryProblem('R.io'), /lowercase/)
  assert.equal(cleanRegistry('  reg.example.com/team//  '), 'reg.example.com/team')
})

test('image tag and digest', () => {
  for (const ok of ['', '1.2.3', 'latest', 'v1_2-3.rc1', '_x', 'a'.repeat(128)]) assert.equal(tagProblem(ok), '', ok)
  for (const bad of ['-1', '.1', 'a b', 'a:b', 'a@b', 'a/b', 'a,b', 'a'.repeat(129), 'é']) assert.notEqual(tagProblem(bad), '', bad)
  assert.equal(digestProblem(''), '')
  assert.equal(digestProblem(D), '')
  for (const bad of ['sha256:abc', 'sha512:' + 'a'.repeat(64), 'a'.repeat(64), 'sha256:' + 'A'.repeat(64), 'sha256:' + 'g'.repeat(64), D + '0']) assert.notEqual(digestProblem(bad), '', bad)
})

test('image settings: a tag or digest needs a registry, and everything empty is fine', () => {
  assert.ok(imageOk(imageProblems({ registry: '', tag: '', digest: '' })))
  assert.ok(imageOk(imageProblems({ registry: 'myteam', tag: '1.0', digest: D })))
  assert.match(imageProblems({ registry: '', tag: '1.0', digest: '' }).tag, /registry first/)
  assert.match(imageProblems({ registry: '', tag: '', digest: D }).digest, /registry first/)
  const p = imageProblems({ registry: 'BAD', tag: 'a b', digest: 'x' })
  assert.ok(p.registry && p.tag && p.digest && !imageOk(p))
})

test('ociBase mirrors the server: a bare name is a Docker Hub namespace', () => {
  for (const [i, o] of Object.entries({ myteam: 'registry-1.docker.io/myteam', 'docker.io/myteam/': 'registry-1.docker.io/myteam', 'index.docker.io/team/sub': 'registry-1.docker.io/team/sub', 'ghcr.io/me/continuum': 'ghcr.io/me/continuum', 'localhost:5000/x': 'localhost:5000/x', 'reg.example.com:8443/team': 'reg.example.com:8443/team' })) {
    assert.equal(ociBase(i), o)
  }
})

test('previewImage: four cases and precedence', () => {
  const none = previewImage({ registry: '', tag: '', digest: '' })
  assert.equal(none.configured, false)
  assert.equal(none.reference, 'continuum/continuum')
  assert.equal(none.chartRef, '')

  const reg = previewImage({ registry: 'myteam/', tag: '', digest: '' })
  assert.deepEqual([reg.configured, reg.repository, reg.reference, reg.chartRef], [true, 'myteam/continuum', 'myteam/continuum', 'oci://registry-1.docker.io/myteam/continuum-agent'])

  const tagged = previewImage({ registry: 'ghcr.io/me/x', tag: '1.2.3', digest: '' })
  assert.deepEqual([tagged.reference, tagged.pinnedReference, tagged.chartRef], ['ghcr.io/me/x/continuum:1.2.3', '', 'oci://ghcr.io/me/x/continuum-agent'])

  const pinned = previewImage({ registry: 'ghcr.io/me/x', tag: '1.2.3', digest: D })
  assert.equal(pinned.reference, `ghcr.io/me/x/continuum@${D}`)
  assert.equal(pinned.pinnedReference, pinned.reference)

  // the organisation's own registry beats the server's default, as a whole; empty falls back to it
  const server = { registry: 'flag.io/t', tag: '9', digest: '' }
  const own = previewImage({ registry: 'own.io', tag: '', digest: '' }, server)
  assert.deepEqual([own.repository, own.tag, own.fromServer], ['own.io/continuum', '', false])
  const inherited = previewImage({ registry: '', tag: '', digest: '' }, server)
  assert.deepEqual([inherited.repository, inherited.tag, inherited.fromServer, inherited.reference], ['flag.io/t/continuum', '9', true, 'flag.io/t/continuum:9'])
})
