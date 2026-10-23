import { act, renderHook } from '@testing-library/react'
import { beforeEach, describe, expect, test } from 'vitest'
import { useColumnVisibility } from '@/features/topology/lib/columns'

beforeEach(() => localStorage.clear())

describe('useColumnVisibility', () => {
  test('every column starts visible when the page lists no default', () => {
    const { result } = renderHook(() => useColumnVisibility('t'))
    expect(result.current.isVisible('a')).toBe(true)
  })

  test('a page can start some columns off for someone who has not chosen yet', () => {
    const { result } = renderHook(() => useColumnVisibility('t', ['b']))
    expect(result.current.isVisible('a')).toBe(true)
    expect(result.current.isVisible('b')).toBe(false)
  })

  test('turning a default-hidden column on is remembered, and the default no longer applies', () => {
    const first = renderHook(() => useColumnVisibility('t', ['b']))
    act(() => first.result.current.toggle('b'))
    expect(first.result.current.isVisible('b')).toBe(true)
    first.unmount()
    const again = renderHook(() => useColumnVisibility('t', ['b']))
    expect(again.result.current.isVisible('b')).toBe(true)
  })

  test('a choice made before the default existed is kept as it was', () => {
    localStorage.setItem('ikhnos:columns:t', JSON.stringify(['a']))
    const { result } = renderHook(() => useColumnVisibility('t', ['b']))
    expect(result.current.isVisible('a')).toBe(false)
    expect(result.current.isVisible('b')).toBe(true)
  })
})
