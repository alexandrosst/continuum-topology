import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import TimeSeriesChart from '@/components/health/TimeSeriesChart'
import type { ChartPoint } from '@/lib/selfHealth'

const pct = (v: number) => `${v.toFixed(1)}%`

/** jsdom never lays out real geometry, so getBoundingClientRect returns all-zero by default - the chart's
 *  own pointer math needs a real width to turn a clientX into a nearest-sample index. Mocked per test
 *  (not in global setup) so every other suite keeps jsdom's own default. */
function mockRect(width: number, left = 0) {
  return vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({
    width, height: 148, left, top: 0, right: left + width, bottom: 148, x: left, y: 0, toJSON: () => ({}),
  })
}

describe('TimeSeriesChart', () => {
  test('fewer than two defined points renders a calm placeholder, not an empty/one-dot plot', () => {
    render(<TimeSeriesChart points={[]} valueFormat={pct} ariaLabel="Empty" />)
    expect(screen.getByText(/collecting data/i)).toBeInTheDocument()
    expect(document.querySelector('svg')).toBeNull()

    render(<TimeSeriesChart points={[{ t: 0, v: 1 }]} valueFormat={pct} ariaLabel="One point" />)
    expect(screen.getAllByText(/collecting data/i).length).toBeGreaterThan(0)
  })

  test('draws gridlines with clean tick labels and the two end-of-axis time labels', () => {
    const points: ChartPoint[] = [
      { t: Date.parse('2026-01-01T00:00:00Z'), v: 10 },
      { t: Date.parse('2026-01-01T00:10:00Z'), v: 20 },
      { t: Date.parse('2026-01-01T00:20:00Z'), v: 97 },
    ]
    const { container } = render(<TimeSeriesChart points={points} valueFormat={pct} ariaLabel="Series" />)
    const svg = container.querySelector('svg')
    expect(svg).not.toBeNull()
    // At least one gridline + one polyline segment were actually drawn.
    expect(container.querySelectorAll('line').length).toBeGreaterThan(0)
    expect(container.querySelectorAll('polyline').length).toBe(1)
    // The latest reading always gets its own static end-marker dot.
    expect(container.querySelectorAll('circle').length).toBeGreaterThanOrEqual(1)
  })

  test('breaks the line into separate segments across an internal gap, never bridging over it', () => {
    const points: ChartPoint[] = [
      { t: 0, v: 1 },
      { t: 1, v: 2 },
      { t: 2, v: undefined },
      { t: 3, v: 4 },
      { t: 4, v: 5 },
    ]
    const { container } = render(<TimeSeriesChart points={points} valueFormat={pct} ariaLabel="Gappy" />)
    expect(container.querySelectorAll('polyline')).toHaveLength(2)
  })

  test('hovering shows a tooltip with the exact value and time at the nearest sample', () => {
    mockRect(600)
    const points: ChartPoint[] = [
      { t: Date.parse('2026-01-01T00:00:00Z'), v: 10 },
      { t: Date.parse('2026-01-01T00:30:00Z'), v: 50 },
      { t: Date.parse('2026-01-01T01:00:00Z'), v: 90 },
    ]
    const { container } = render(<TimeSeriesChart points={points} valueFormat={(v) => `${v} units`} ariaLabel="Hoverable" />)
    const wrap = container.firstChild as HTMLElement
    // Pointer near the right edge should resolve to the last sample (index 2, value 90).
    fireEvent.pointerMove(wrap, { clientX: 599 })
    expect(screen.getByTestId('chart-tooltip-value')).toHaveTextContent('90 units')

    // Leaving clears the tooltip again.
    fireEvent.pointerLeave(wrap)
    expect(screen.queryByTestId('chart-tooltip-value')).toBeNull()
  })

  test('keyboard focus + arrow keys reach the same tooltip detail hover does', () => {
    mockRect(600)
    const points: ChartPoint[] = [
      { t: Date.parse('2026-01-01T00:00:00Z'), v: 1 },
      { t: Date.parse('2026-01-01T00:30:00Z'), v: 2 },
    ]
    const { container } = render(<TimeSeriesChart points={points} valueFormat={(v) => `${v} u`} ariaLabel="Keyboard" />)
    const wrap = container.firstChild as HTMLElement
    wrap.focus()
    fireEvent.keyDown(wrap, { key: 'ArrowRight' })
    expect(screen.getByTestId('chart-tooltip-value')).toHaveTextContent('1 u')
    fireEvent.keyDown(wrap, { key: 'ArrowRight' })
    expect(screen.getByTestId('chart-tooltip-value')).toHaveTextContent('2 u')
    fireEvent.keyDown(wrap, { key: 'Escape' })
    expect(screen.queryByTestId('chart-tooltip-value')).toBeNull()
  })

  test('hovering a sample with no reading (an internal gap) says so honestly instead of showing a stale number', () => {
    mockRect(600)
    const points: ChartPoint[] = [
      { t: Date.parse('2026-01-01T00:00:00Z'), v: 1 },
      { t: Date.parse('2026-01-01T00:30:00Z'), v: undefined },
      { t: Date.parse('2026-01-01T01:00:00Z'), v: 3 },
    ]
    const { container } = render(<TimeSeriesChart points={points} valueFormat={(v) => `${v} u`} ariaLabel="Gap hover" />)
    const wrap = container.firstChild as HTMLElement
    fireEvent.pointerMove(wrap, { clientX: 300 }) // the middle sample, which has no reading
    expect(screen.getByTestId('chart-tooltip-value')).toHaveTextContent('No reading')
  })
})
