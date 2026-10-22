import { readFileSync } from 'node:fs'
import { describe, expect, test } from 'vitest'

// src/lib/typesAgree.ts holds the page's hand-written records (types.ts) to the generated ones (generated/wire.ts) at compile
// time. What it cannot do is notice a record that nobody paired. This does: every interface that both files declare under one
// name is paired there, or is listed here with the reason it is not.

const NOT_PAIRED: Record<string, string> = {
  Topology: "the page's also holds what a person declared (devices, sites, applications); the server's agents, suggestions and paths arrive apart from it",
  Agent: "the page's is the administration view of an agent with 33 fields; the server's is the discovery record of one",
}

const interfaces = (file: string) => [...readFileSync(file, 'utf8').matchAll(/^export interface (\w+)/gm)].map((m) => m[1])

describe('the page\'s records and the generated ones', () => {
  const page = interfaces('src/lib/types.ts')
  const wire = new Set(interfaces('src/lib/generated/wire.ts'))
  const agree = readFileSync('src/lib/typesAgree.ts', 'utf8')
  const shared = page.filter((n) => wire.has(n))

  test('there are shared records to pair', () => {
    expect(shared.length).toBeGreaterThan(15)
  })

  for (const name of shared) {
    test(`${name} is paired or says why not`, () => {
      const paired = new RegExp(`Agrees<P\\.${name},\\s*W\\.\\w+`).test(agree)
      if (NOT_PAIRED[name]) expect(paired, `${name} is paired, so it must leave NOT_PAIRED`).toBe(false)
      else expect(paired, `${name} is declared by types.ts and by the generated types: pair it in typesAgree.ts`).toBe(true)
    })
  }

  test('a name in NOT_PAIRED is still shared', () => {
    for (const name of Object.keys(NOT_PAIRED)) expect(shared, name).toContain(name)
  })
})
