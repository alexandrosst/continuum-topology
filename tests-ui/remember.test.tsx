import { afterEach, describe, expect, test, vi } from 'vitest'
import { readText, writeText } from '@/lib/remember'

describe('remembered text', () => {
  afterEach(() => { vi.restoreAllMocks(); localStorage.clear() })

  test('comes back as written, and is null when nothing was chosen', () => {
    expect(readText('k')).toBeNull()
    writeText('k', 'full')
    expect(readText('k')).toBe('full')
  })

  test('blocked storage never throws: the read is null and the write is a no-op', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked') })
    expect(readText('k')).toBeNull()
    expect(() => writeText('k', 'x')).not.toThrow()
  })
})
