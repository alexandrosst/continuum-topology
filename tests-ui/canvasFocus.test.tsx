import { beforeEach, describe, expect, test } from 'vitest'
import { activeId, focusedBy, useCanvasFocus } from '@/store/canvasFocus'

describe('canvas focus', () => {
  beforeEach(() => useCanvasFocus.setState({ hover: null, pinned: null, network: null }))

  test('a bundle lights its calls when the hover or the selection is one of its boxes or ends, and not otherwise', () => {
    const ids = ['g:a', 'g:b', 'svc-1']
    expect(focusedBy(useCanvasFocus.getState(), ids)).toBe(false)
    useCanvasFocus.getState().setHover('g:a')
    expect(focusedBy(useCanvasFocus.getState(), ids)).toBe(true)
    useCanvasFocus.getState().setHover(null)
    useCanvasFocus.getState().setPinned('svc-1')
    expect(focusedBy(useCanvasFocus.getState(), ids)).toBe(true)
    useCanvasFocus.getState().setPinned('elsewhere')
    expect(focusedBy(useCanvasFocus.getState(), ids)).toBe(false)
    expect(focusedBy(useCanvasFocus.getState(), undefined)).toBe(false)
  })

  test('the same hover set twice does not notify, so a pointer resting on a node costs nothing', () => {
    let calls = 0
    const off = useCanvasFocus.subscribe(() => calls++)
    useCanvasFocus.getState().setHover('x')
    useCanvasFocus.getState().setHover('x')
    off()
    expect(calls).toBe(1)
    expect(activeId(useCanvasFocus.getState())).toBe('x')
  })
})
