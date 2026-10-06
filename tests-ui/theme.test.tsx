import { readFileSync } from 'node:fs'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { initTheme } from '@/lib/theme'

function osPrefers(light: boolean) {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: light && q.includes('light'), media: q, addEventListener() {}, removeEventListener() {} }))
}

afterEach(() => {
  vi.unstubAllGlobals()
  localStorage.clear()
  document.documentElement.removeAttribute('data-theme')
})

describe('theme at startup', () => {
  it('follows a light OS when nothing is pinned', () => {
    osPrefers(true)
    initTheme()
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })

  it('follows a dark OS when nothing is pinned', () => {
    osPrefers(false)
    initTheme()
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('a pinned choice beats the OS', () => {
    osPrefers(true)
    localStorage.setItem('continuum:theme', 'dark')
    initTheme()
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })
})

// The server's Content-Security-Policy has no script-src of its own, so scripts fall back to 'self': an inline <script> in the page is
// blocked there (it was: the theme script never ran and the page stayed dark). Keep every script a file.
describe('index.html under the server CSP', () => {
  const html = readFileSync('index.html', 'utf8').replace(/<!--[\s\S]*?-->/g, '')
  const scripts = [...html.matchAll(/<script\b([^>]*)>([\s\S]*?)<\/script>/g)]

  it('has no inline script', () => {
    expect(scripts.length).toBeGreaterThan(0)
    for (const [, attrs, body] of scripts) {
      expect(attrs).toMatch(/\bsrc=/)
      expect(body.trim()).toBe('')
    }
  })

  it('loads the theme script synchronously, ahead of the app', () => {
    const theme = scripts.findIndex(([, attrs]) => attrs.includes('/theme-init.js'))
    expect(theme).toBeGreaterThanOrEqual(0)
    expect(scripts[theme][1]).not.toMatch(/\b(defer|async|type=)/)
  })
})
