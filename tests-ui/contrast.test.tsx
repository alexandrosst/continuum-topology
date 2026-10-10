import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, test } from 'vitest'

// The words a Calm canvas puts on a tinted card or box (what is wrong, which network) are drawn in the theme's own text tokens. This reads those
// tokens from index.css and holds them to 4.5:1 against the surface they sit on, in both themes, so a palette edit cannot quietly bring back
// pale amber on a pale card.
const css = readFileSync(path.resolve(import.meta.dirname, '../src/index.css'), 'utf8')
const block = (start: string) => css.slice(css.indexOf(start), css.indexOf('}', css.indexOf(start)))
const tokens = (b: string) => Object.fromEntries([...b.matchAll(/--color-([a-z0-9-]+):\s*(#[0-9a-f]{6})/gi)].map((m) => [m[1], m[2]]))
const THEMES = { dark: tokens(block('@theme {')), light: { ...tokens(block('@theme {')), ...tokens(block(":root[data-theme='light']")) } }

const rgb = (hex: string) => [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
const mix = (a: number[], b: number[], t: number) => a.map((v, i) => v * t + b[i] * (1 - t))
const lum = (c: number[]) => {
  const [r, g, b] = c.map((v) => { const s = v / 255; return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4 })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}
const ratio = (a: number[], b: number[]) => { const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x); return (hi + 0.05) / (lo + 0.05) }

describe.each(Object.entries(THEMES))('Calm notes in the %s theme', (_, t) => {
  // A card is tinted 7% with its state colour over the card surface, a box 5% over the box surface; the tint moves the ground a little, so the
  // text is held to the ground at its most tinted.
  test.each([['warn', 'nb-925'], ['bad', 'nb-925'], ['warn', 'nb-920'], ['bad', 'nb-920'], ['info', 'nb-920']])('%s on %s reads at 4.5:1', (tone, surface) => {
    const ground = mix(rgb(t[tone]), rgb(t[surface]), 0.1)
    expect(ratio(rgb(t[tone]), ground)).toBeGreaterThanOrEqual(4.5)
  })
})
