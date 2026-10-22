import { describe, expect, test } from 'vitest'
import { computeFocus, MAX_RIPPLE, type FocusEdge, type FocusNode } from '@/features/topology/lib/focus'

const box = (id: string, x: number, y: number, parentId?: string, type = 'card'): FocusNode => ({ id, type, parentId, position: { x, y }, measured: { width: 100, height: 50 } })
// Two clusters side by side; a and b in the first, c and d in the second, e on its own far away.
const nodes: FocusNode[] = [
  box('g1', 0, 0, undefined, 'boundary'),
  box('a', 10, 10, 'g1'),
  box('b', 10, 80, 'g1'),
  box('g2', 600, 0, undefined, 'boundary'),
  box('c', 10, 10, 'g2'),
  box('d', 10, 80, 'g2'),
  box('e', 3000, 3000),
]
const edges: FocusEdge[] = [
  { id: 'ab', source: 'a', target: 'b' },
  { id: 'ac', source: 'a', target: 'c' },
  { id: 'cd', source: 'c', target: 'd' },
]

describe('computeFocus', () => {
  test('a card keeps itself, the cards its lines reach and the boxes they sit in; the rest fades', () => {
    const f = computeFocus(nodes, edges, 'a', null)!
    expect([...f.keep].sort()).toEqual(['a', 'b', 'c', 'g1', 'g2'])
    expect([...f.core].sort()).toEqual(['a', 'b', 'c'])
    expect([...f.hotIds].sort()).toEqual(['ab', 'ac'])
    expect([...f.ripple.keys()].sort()).toEqual(['d', 'e'])
  })

  test('a cluster box keeps its contents, and the cards its crossing lines reach outside', () => {
    const f = computeFocus(nodes, edges, 'g1', null)!
    expect([...f.hotIds]).toEqual(['ac']) // ab is inside the cluster, so not one of its crossing lines
    expect(f.keep.has('a')).toBe(true)
    expect(f.keep.has('b')).toBe(true)
    expect(f.keep.has('c')).toBe(true)
    expect(f.keep.has('g2')).toBe(true) // the box c sits in
    expect(f.keep.has('d')).toBe(false) // d is not reached
  })

  test('a clicked line lights its two ends', () => {
    const f = computeFocus(nodes, edges, null, 'cd')!
    expect([...f.core].sort()).toEqual(['c', 'd'])
  })

  test('an entity with no lines does not fade anything', () => {
    expect(computeFocus(nodes, edges, 'e', null)).toBeNull()
    expect(computeFocus(nodes, edges, null, null)).toBeNull()
  })

  test('the fade spreads outward: a node waits longer the further it is, up to a cap', () => {
    const f = computeFocus(nodes, edges, 'a', null)!
    expect(f.ripple.get('d')!).toBeLessThan(f.ripple.get('e')!)
    expect(f.ripple.get('e')).toBe(MAX_RIPPLE)
  })
})
