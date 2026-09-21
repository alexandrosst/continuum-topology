import assert from 'node:assert/strict'
import vectors from '../backend/internal/advice/testdata/vectors.json'
import { applyChange, assess, CPU, effective, FACTORS, fit, levelOf, MEMORY, newPool, scaledPool, sensitivityText, worse, adjustPool, type Class, type Check, type Fact, type CapNode, type Dimension } from '../src/lib/advice'

let failed = 0
const queue: { name: string; fn: () => void | Promise<void> }[] = []
const test = (name: string, fn: () => void | Promise<void>) => void queue.push({ name, fn })

const NOW = Date.parse('2026-09-21T12:00:00Z')
const WIN = 120_000
const close = (a: number, b: number) => (Number.isFinite(a) || Number.isFinite(b) ? Math.abs(a - b) <= 1e-9 * Math.max(1, Math.abs(a), Math.abs(b)) : a === b)

interface VFact { value: number | null; class: Class; ageSec?: number }
const toFact = (v: VFact): Fact => ({ value: v.value, class: v.class, observedAt: v.ageSec === undefined ? undefined : NOW - v.ageSec * 1000 })

/* ---------- the conformance vectors, shared with the Go tests ---------- */

test('vectors: the factors are the ones the server uses', () => {
  for (const [c, f] of Object.entries(vectors.factors)) assert.equal(FACTORS[c as keyof typeof FACTORS], f, c)
})

test('vectors: every case gives the verdict, class, interval, margin and thresholds the server computes', () => {
  assert.ok(vectors.cases.length >= 20)
  for (const c of vectors.cases) {
    const dim: Dimension = c.dimension === 'memory' ? MEMORY : CPU
    const nodes: CapNode[] = c.nodes.map((n, i) => ({ id: `n${i}`, name: 'n', allocatable: toFact(n.alloc as VFact), requested: toFact(n.requested as VFact) }))
    let pool = newPool(nodes, NOW, c.windowSec * 1000)
    for (const a of (c as { adjust?: (number | null)[] }).adjust ?? []) pool = adjustPool(pool, a)
    const r = fit(dim, { value: c.need.value, class: (c.need as { class: Class }).class }, pool, 'x')
    const e = c.expect as { verdict: string; class: string; lo?: number; hi?: number | null; known?: boolean; knownFree?: number | null; margin?: number | null; changes: { direction: string; threshold: number; becomes: string }[] }
    assert.equal(r.verdict, e.verdict, `${c.name}: verdict`)
    assert.equal(r.class, e.class, `${c.name}: class`)
    if (e.lo !== undefined) {
      assert.ok(close(pool.lo, e.lo), `${c.name}: lo ${pool.lo} vs ${e.lo}`)
      assert.ok(e.hi === null ? pool.hi === Infinity : close(pool.hi, e.hi as number), `${c.name}: hi ${pool.hi} vs ${e.hi}`)
      assert.equal(pool.nominalKnown, e.known, `${c.name}: known`)
      if (e.knownFree !== null && e.knownFree !== undefined) assert.ok(close(pool.knownFree, e.knownFree), `${c.name}: free`)
      assert.ok(e.margin === null ? r.margin === undefined : r.margin !== undefined && close(r.margin, e.margin as number), `${c.name}: margin ${r.margin} vs ${e.margin}`)
    }
    assert.equal(r.wouldChange.length, e.changes.length, `${c.name}: number of changes`)
    r.wouldChange.forEach((ch, i) => {
      assert.equal(ch.direction, e.changes[i].direction, `${c.name}: direction`)
      assert.equal(ch.becomes, e.changes[i].becomes, `${c.name}: becomes`)
      assert.ok(close(ch.threshold, e.changes[i].threshold), `${c.name}: threshold ${ch.threshold} vs ${e.changes[i].threshold}`)
    })
  }
})

test('vectors: combining verdicts and the weakest fact decide confidence', () => {
  assert.ok(vectors.assess.length >= 5)
  for (const a of vectors.assess) {
    const checks: Check[] = a.parts.map((p) => ({ name: 'x', verdict: p.verdict as Check['verdict'], class: p.class as Class, reason: 'x' }))
    const got = assess([], checks)
    assert.equal(got.verdict, a.expect.verdict, a.name)
    assert.equal(got.class, a.expect.class, a.name)
    assert.equal(got.confidence, a.expect.confidence, a.name)
  }
})

/* ---------- rules, stated on their own ---------- */

const node = (alloc: number, req: number, c: Class = 'reported', ageSec?: number): CapNode => ({ id: 'n', name: 'n', allocatable: toFact({ value: alloc, class: c, ageSec }), requested: toFact({ value: req, class: c, ageSec }) })
const unk: CapNode = { id: 'u', name: 'u', allocatable: { value: null, class: 'unknown' }, requested: { value: null, class: 'unknown' } }
const need = (v: number) => ({ value: v, class: 'reported' as Class })

test('unknown capacity is can\'t tell: never zero, never fits', () => {
  for (const n of [0.001, 1, 1e9]) assert.equal(fit(CPU, need(n), newPool([unk], NOW, WIN), 'x').verdict, 'cantTell')
  assert.equal(fit(CPU, need(1), newPool([], NOW, WIN), 'x').verdict, 'cantTell')
  assert.equal(fit(CPU, { value: null, class: 'unknown' }, newPool([node(8, 2)], NOW, WIN), 'x').verdict, 'cantTell')
})

test('a guess widens the interval, a stale value is one class worse, "fits" needs the pessimistic end', () => {
  assert.equal(fit(CPU, need(5.1), newPool([node(8, 2)], NOW, WIN), 'x').verdict, 'fits')
  assert.equal(fit(CPU, need(5.2), newPool([node(8, 2)], NOW, WIN), 'x').verdict, 'cantTell')
  assert.equal(fit(CPU, need(6.9001), newPool([node(8, 2)], NOW, WIN), 'x').verdict, 'doesNotFit')
  assert.equal(fit(CPU, need(3.1), newPool([node(8, 2, 'guess')], NOW, WIN), 'x').verdict, 'cantTell')
  assert.equal(fit(CPU, need(5.6), newPool([node(8, 2, 'measured')], NOW, WIN), 'x').verdict, 'fits')
  const stale = newPool([node(8, 2, 'reported', 300)], NOW, WIN)
  assert.equal(stale.class, 'inferred')
  assert.ok(stale.aged)
  assert.equal(fit(CPU, need(4.5), stale, 'x').verdict, 'cantTell')
  assert.equal(fit(CPU, need(4.5), newPool([node(8, 2, 'reported', 60)], NOW, WIN), 'x').verdict, 'fits')
  assert.deepEqual(effective({ value: 1, class: 'guess', observedAt: NOW - 1e6 }, NOW, WIN), { class: 'guess', aged: true })
  assert.deepEqual(effective({ value: 1, class: 'reported', observedAt: NOW - WIN }, NOW, WIN), { class: 'reported', aged: false })
  assert.deepEqual(effective({ value: 1, class: 'reported' }, NOW + 1e12, WIN), { class: 'reported', aged: false })
})

test('confidence follows the weakest deciding fact, not the average', () => {
  const good = fit(CPU, need(1), newPool([node(8, 2, 'measured')], NOW, WIN), 'x')
  const mem = fit(MEMORY, need(1024 ** 3), newPool([{ id: 'n', name: 'n', allocatable: { value: 8 * 1024 ** 3, class: 'guess' }, requested: { value: 2 * 1024 ** 3, class: 'guess' } }], NOW, WIN), 'x')
  const a = assess([good, mem], [{ name: 'arch', verdict: 'fits', class: 'reported', reason: '' }])
  assert.equal(a.verdict, 'fits')
  assert.equal(a.class, 'guess')
  assert.equal(a.confidence, 'low')
  const b = assess([good], [{ name: 'requested', verdict: 'cantTell', class: 'unknown', reason: 'not known', fix: { action: 'raise-agent-tier', text: 'x', link: '/agents' } }])
  assert.equal(b.confidence, 'none')
  assert.equal(b.fixes[0].link, '/agents')
  assert.equal(levelOf(worse('measured', 'inferred')), 'medium')
})

test('applying a suggested change really changes the verdict, and one step short does not', () => {
  const pools: Record<string, CapNode[]> = { reported: [node(8, 2)], guess: [node(8, 2, 'guess')], mixed: [node(8, 2, 'measured'), node(4, 1)], stale: [node(8, 2, 'reported', 300)], partial: [node(8, 2), unk], nothing: [unk] }
  let checked = 0
  for (const nodes of Object.values(pools)) {
    for (const n of [0.5, 2, 3.5, 5, 6, 6.5, 7, 8, 9.5, 12, 20]) {
      const r = fit(CPU, need(n), newPool(nodes, NOW, WIN), 'x')
      for (const ch of r.wouldChange) {
        checked++
        const eps = 1e-6 * Math.max(1, ch.threshold)
        const past = ch.direction === 'below' ? -eps : eps
        assert.equal(applyChange(r, ch, past), ch.becomes, `${ch.text}`)
        if (ch.direction === 'below') assert.notEqual(applyChange(r, ch, eps), ch.becomes, `${ch.text}: on the safe side already changed`)
      }
    }
  }
  assert.ok(checked > 30)
})

test('sensitivity is said in the same terms as the interval', () => {
  const f = fit(CPU, need(3), newPool([node(8, 2)], NOW, WIN), 'x')
  assert.match(sensitivityText(f), /50 %/)
  const t = fit(CPU, need(5.5), newPool([node(8, 2)], NOW, WIN), 'x')
  assert.match(sensitivityText(t), /less than 8 %.*±15 %/)
  assert.match(sensitivityText(fit(CPU, need(9), newPool([node(8, 2)], NOW, WIN), 'x')), /50 % more/)
  assert.equal(sensitivityText(fit(CPU, need(1), newPool([], NOW, WIN), 'x')), '')
  assert.ok(close(scaledPool(newPool([node(8, 2)], NOW, WIN), 3).knownFree, 3))
})

test('reasons name the class, the amounts and what would flip it', () => {
  const r = fit(MEMORY, need(3.2 * 1024 ** 3), newPool([{ id: 'n', name: 'edge-2', allocatable: { value: 8 * 1024 ** 3, class: 'inferred' }, requested: { value: 4 * 1024 ** 3, class: 'inferred' } }], NOW, WIN), 'cluster edge-2')
  assert.equal(r.verdict, 'cantTell')
  assert.match(r.reason, /inferred/)
  assert.match(r.reason, /3\.2 GiB/)
  assert.match(r.reason, /4 GiB/)
  assert.ok(r.wouldChange.every((c) => /GiB/.test(c.text) && /cluster edge-2/.test(c.text)))
})

for (const { name, fn } of queue) {
  try {
    await fn()
    console.log('PASS', name)
  } catch (e) {
    failed++
    console.log('FAIL', name, '\n  ', (e as Error).message)
  }
}
console.log(failed ? `\n${failed} FAILED` : '\nall passed')
process.exit(failed ? 1 : 0)
