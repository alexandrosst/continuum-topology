import { existsSync, readdirSync, readFileSync, statSync } from 'node:fs'
import { dirname, join, normalize, relative } from 'node:path'
import { describe, expect, test } from 'vitest'

// How the page is laid out, held by a test so that it stays so.
//
//   src/app/       the shell: the routes, the layout, what loads which page. It may import anything.
//   src/features/  one folder for each part of the product (the agents, the pipeline, FUSION...). A feature owns its page, its components
//                  and the logic that only it uses.
//   everything else under src/ is shared: the primitives, the stores, the API client, the model and what more than one feature needs.
//
// A feature imports shared code and the features BELOW it in LAYERS, never one beside it or above it, so that no two features can need
// each other: what both need is shared, and a feature that needs another's piece says so by being above it. Shared code never imports a
// feature, and nothing imports the shell.

/** The features, lowest layer first. Features in one layer cannot import each other. */
const LAYERS: string[][] = [
  // Each of these needs nothing of the others.
  ['accounts', 'activity', 'fusion', 'health', 'history', 'links', 'proposals', 'settings'],
  // From here each layer may use any layer listed before it, and none beside it.
  ['discovery', 'placement'],
  ['pipeline'],
  ['agents'],
  ['topology'],
]

const SRC = join(process.cwd(), 'src')

function walk(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) walk(p, out)
    else if (/\.(ts|tsx)$/.test(name)) out.push(p)
  }
  return out
}

const IMPORT = /(?:from|import)\s*\(?\s*['"]([^'"\n]+)['"]/g

function resolve(from: string, spec: string): string | undefined {
  const base = spec.startsWith('@/') ? join(SRC, spec.slice(2)) : spec.startsWith('.') ? normalize(join(dirname(from), spec)) : undefined
  if (!base) return undefined
  for (const ext of ['', '.ts', '.tsx', '/index.ts', '/index.tsx']) {
    const p = base + ext
    if (existsSync(p) && statSync(p).isFile()) return p
  }
  return undefined
}

type Where = { kind: 'app' } | { kind: 'feature'; name: string } | { kind: 'shared' }

function where(file: string): Where {
  const rel = relative(SRC, file).split(/[\\/]/)
  if (rel[0] === 'app' || rel[0] === 'main.tsx') return { kind: 'app' }
  if (rel[0] === 'features' && rel.length > 2) return { kind: 'feature', name: rel[1] }
  return { kind: 'shared' }
}

const layerOf = (name: string) => LAYERS.findIndex((l) => l.includes(name))

const edges: { from: string; to: string; a: Where; b: Where }[] = []
for (const file of walk(SRC)) {
  if (file.includes(`${join(SRC, 'lib', 'generated')}`)) continue
  const text = readFileSync(file, 'utf8')
  for (const m of text.matchAll(IMPORT)) {
    const to = resolve(file, m[1])
    if (to) edges.push({ from: relative(SRC, file), to: relative(SRC, to), a: where(file), b: where(to) })
  }
}

describe('the layout of the page', () => {
  test('every folder under features/ is in a layer, and every layer names a folder', () => {
    const dir = join(SRC, 'features')
    const folders = existsSync(dir) ? readdirSync(dir).filter((n) => statSync(join(dir, n)).isDirectory()) : []
    expect(folders.filter((n) => layerOf(n) < 0)).toEqual([])
    expect(LAYERS.flat().filter((n) => !folders.includes(n))).toEqual([])
  })

  test('shared code does not import a feature or the shell', () => {
    const bad = edges.filter((e) => e.a.kind === 'shared' && e.b.kind !== 'shared').map((e) => `${e.from} imports ${e.to}`)
    expect(bad).toEqual([])
  })

  test('nothing imports the shell but the shell', () => {
    const bad = edges.filter((e) => e.a.kind !== 'app' && e.b.kind === 'app').map((e) => `${e.from} imports ${e.to}`)
    expect(bad).toEqual([])
  })

  test('a feature imports only shared code and the features below it', () => {
    const bad = edges
      .filter((e) => e.a.kind === 'feature' && e.b.kind === 'feature' && e.a.name !== e.b.name && !(layerOf(e.b.name) >= 0 && layerOf(e.b.name) < layerOf(e.a.name)))
      .map((e) => `${e.from} imports ${e.to}`)
    expect(bad).toEqual([])
  })
})
