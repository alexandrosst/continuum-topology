import { useCallback, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent } from 'react'
import { clockLabel, niceDomain, type ChartPoint } from '@/lib/selfHealth'

/** The chart's own internal coordinate system - a fixed aspect ratio (VBW:VBH), not a pixel size. The
 *  wrapping element is given that exact CSS aspect-ratio (see the style below), so scaling the SVG's
 *  viewBox up to the card's real rendered width is uniform in both axes - text never gets squished or
 *  stretched, the one thing a plain `preserveAspectRatio="none"` stretch-to-fill would risk. */
const VBW = 600
const VBH = 148
const MARGIN = { top: 10, right: 10, bottom: 22, left: 46 }

/** Consecutive defined points join into one polyline segment; an undefined point ends the current
 *  segment instead of being bridged over - the same rule Sparkline (primitives.tsx) already applies, so
 *  a missing reading never gets drawn as if it were interpolated. Returns index ranges, not points, so
 *  the caller can derive both the line and the area fill from the same segmentation. */
function segments(points: ChartPoint[]): [number, number][] {
  const out: [number, number][] = []
  let start = -1
  for (let i = 0; i < points.length; i++) {
    if (points[i].v === undefined) {
      if (start >= 0 && i - start > 1) out.push([start, i - 1])
      start = -1
    } else if (start < 0) {
      start = i
    }
  }
  if (start >= 0 && points.length - start > 1) out.push([start, points.length - 1])
  return out
}

/**
 * A detailed, interactive time-series line chart for one metric: gridlines with clean rounded ticks, a
 * soft area fill under the line, a static marker on the latest reading, and a hover/keyboard crosshair +
 * tooltip reading out the exact value at any point - see dataviz's marks-and-anatomy.md and
 * interaction.md for the specific specs this follows (2px line, >=8px end-marker, ~10% area wash, hairline
 * recessive grid, crosshair snaps to the nearest sample, same detail on keyboard focus as on hover).
 *
 * Deliberately does NOT draw its own "current value" label on the chart: the card above it already shows
 * that as a large direct-labeled figure (see EntityHealthCard's MetricTile), so repeating it here in tiny
 * SVG text would be clutter, not a second source of truth.
 *
 * Fewer than two defined points (a metric this entity has barely started reporting) renders a calm
 * placeholder instead of a one-dot or empty plot - a different, narrower case than "this metric is not
 * available on this hardware at all", which the caller (EntityHealthCard) handles one level up by not
 * rendering this component at all for that metric.
 */
export default function TimeSeriesChart({
  points,
  color = 'var(--color-accent)',
  valueFormat,
  tickFormat = valueFormat,
  ariaLabel,
  surface = 'var(--color-nb-925)',
}: {
  points: ChartPoint[]
  color?: string
  /** Precise, for the tooltip - e.g. (v) => `${v.toFixed(1)} MB`. */
  valueFormat: (v: number) => string
  /** Short, for axis ticks - defaults to valueFormat when the precise form is already short enough. */
  tickFormat?: (v: number) => string
  ariaLabel: string
  /** The card's own background, so the end-marker's ring reads as a notch out of it rather than a halo -
   *  matches whatever bg-nb-9xx class the caller's card actually uses. */
  surface?: string
}) {
  const wrapRef = useRef<HTMLDivElement>(null)
  const [active, setActive] = useState<number | null>(null)
  const gradId = useId()

  const defined = points.filter((p): p is { t: number; v: number } => p.v !== undefined)
  const plotW = VBW - MARGIN.left - MARGIN.right
  const plotH = VBH - MARGIN.top - MARGIN.bottom
  const n = points.length

  const domain = useMemo(() => {
    if (defined.length < 2) return null
    const vs = defined.map((p) => p.v)
    return niceDomain(Math.min(...vs), Math.max(...vs), 4)
  }, [defined])

  const x = useCallback((i: number) => MARGIN.left + (n <= 1 ? 0 : (i / (n - 1)) * plotW), [n, plotW])
  const y = useCallback(
    (v: number) => {
      if (!domain) return MARGIN.top + plotH
      const span = domain.max - domain.min || 1
      return MARGIN.top + (1 - (v - domain.min) / span) * plotH
    },
    [domain, plotH],
  )

  const locate = useCallback(
    (clientX: number) => {
      const rect = wrapRef.current?.getBoundingClientRect()
      if (!rect || rect.width <= 0 || n === 0) return null
      const frac = Math.min(1, Math.max(0, (clientX - rect.left) / rect.width))
      const vx = frac * VBW
      const i = Math.round(((vx - MARGIN.left) / plotW) * (n - 1))
      return Math.min(n - 1, Math.max(0, i))
    },
    [n, plotW],
  )

  if (!domain) {
    return (
      <div
        className="flex items-center justify-center rounded-lg border border-dashed border-nb-850 text-xs text-nb-600"
        style={{ aspectRatio: `${VBW} / ${VBH}` }}
      >
        Collecting data - check back in a moment.
      </div>
    )
  }

  const segs = segments(points)
  let lastDefinedIndex = -1
  for (let i = points.length - 1; i >= 0; i--) {
    if (points[i].v !== undefined) {
      lastDefinedIndex = i
      break
    }
  }
  const activePoint = active !== null ? points[active] : null
  const baseline = MARGIN.top + plotH

  const move = (clientX: number) => {
    const i = locate(clientX)
    if (i !== null) setActive(i)
  }
  const onKeyDown = (e: ReactKeyboardEvent<HTMLDivElement>) => {
    if (e.key === 'ArrowRight') {
      e.preventDefault()
      setActive((a) => Math.min(n - 1, (a ?? -1) + 1))
    } else if (e.key === 'ArrowLeft') {
      e.preventDefault()
      setActive((a) => Math.max(0, (a ?? n) - 1))
    } else if (e.key === 'Escape') {
      setActive(null)
    }
  }

  // Tooltip horizontal anchor as a percent of the card's own width, clamped so it never clips off either
  // edge - the chart's own margins already keep data off the very edge, this just keeps the tooltip box
  // itself on-card too.
  const activePct = active !== null ? (x(active) / VBW) * 100 : 0
  const anchor = activePct < 18 ? 'left' : activePct > 82 ? 'right' : 'center'

  return (
    <div
      ref={wrapRef}
      className="relative w-full select-none"
      style={{ aspectRatio: `${VBW} / ${VBH}` }}
      tabIndex={0}
      role="group"
      aria-label={ariaLabel}
      onPointerMove={(e) => move(e.clientX)}
      onPointerDown={(e) => move(e.clientX)}
      onPointerLeave={() => setActive(null)}
      onBlur={() => setActive(null)}
      onKeyDown={onKeyDown}
    >
      <svg viewBox={`0 0 ${VBW} ${VBH}`} preserveAspectRatio="none" width="100%" height="100%" aria-hidden className="block overflow-visible">
        <defs>
          <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={color} stopOpacity={0.22} />
            <stop offset="100%" stopColor={color} stopOpacity={0.02} />
          </linearGradient>
        </defs>

        {/* Gridlines + y-axis ticks: recessive hairlines, clean rounded values (niceDomain), the only
            labels this chart puts on the axis itself - see marks-and-anatomy.md. */}
        {domain.ticks.map((t) => (
          <g key={t}>
            <line x1={MARGIN.left} x2={VBW - MARGIN.right} y1={y(t)} y2={y(t)} stroke="var(--color-nb-850)" strokeWidth={1} />
            <text x={MARGIN.left - 6} y={y(t)} textAnchor="end" dominantBaseline="middle" fontSize={10} fill="var(--color-nb-600)">
              {tickFormat(t)}
            </text>
          </g>
        ))}

        {/* x-axis: first/last sample time only - a sparse axis a reader can actually read, not a tick per
            sample. */}
        {n >= 2 && (
          <>
            <text x={MARGIN.left} y={VBH - 6} textAnchor="start" fontSize={10} fill="var(--color-nb-600)">
              {clockLabel(points[0].t)}
            </text>
            <text x={VBW - MARGIN.right} y={VBH - 6} textAnchor="end" fontSize={10} fill="var(--color-nb-600)">
              {clockLabel(points[n - 1].t)}
            </text>
          </>
        )}

        {/* Area wash + line, per defined segment - a gap in the data breaks both rather than bridging
            over it (see segments() above). */}
        {segs.map(([s, e]) => {
          const pts = points.slice(s, e + 1) as { t: number; v: number }[]
          const linePts = pts.map((p, i) => `${x(s + i)},${y(p.v)}`).join(' ')
          const areaPts = `${x(s)},${baseline} ${linePts} ${x(e)},${baseline}`
          return (
            <g key={s}>
              <polygon points={areaPts} fill={`url(#${gradId})`} stroke="none" />
              <polyline points={linePts} fill="none" stroke={color} strokeWidth={2} strokeLinecap="round" strokeLinejoin="round" />
            </g>
          )
        })}

        {/* The latest reading, always marked - this chart's one "direct label" (as a dot, not text; see
            this component's own doc comment for why the number itself lives in the card header instead). */}
        {lastDefinedIndex >= 0 && (
          <circle cx={x(lastDefinedIndex)} cy={y(points[lastDefinedIndex].v as number)} r={4} fill={color} stroke={surface} strokeWidth={2} />
        )}

        {/* Hover/keyboard crosshair: snaps to the nearest sample (never a free-floating pixel position),
            lists that sample whether or not it happens to have a value - interaction.md's "the crosshair
            finds the X". */}
        {activePoint && (
          <g>
            <line x1={x(active as number)} x2={x(active as number)} y1={MARGIN.top} y2={baseline} stroke="var(--color-nb-600)" strokeWidth={1} />
            {activePoint.v !== undefined && (
              <circle cx={x(active as number)} cy={y(activePoint.v)} r={5} fill={color} stroke={surface} strokeWidth={2} />
            )}
          </g>
        )}
      </svg>

      {activePoint && (
        <div
          className="pointer-events-none absolute top-1 whitespace-nowrap rounded-md border border-nb-800 bg-nb-920 px-2 py-1 text-xs shadow-lg"
          style={{
            left: anchor === 'center' ? `${activePct}%` : anchor === 'left' ? '0%' : undefined,
            right: anchor === 'right' ? '0%' : undefined,
            transform: anchor === 'center' ? 'translateX(-50%)' : undefined,
          }}
        >
          <div data-testid="chart-tooltip-value" className="font-medium tabular-nums text-nb-300">{activePoint.v !== undefined ? valueFormat(activePoint.v) : <span className="text-nb-600">No reading</span>}</div>
          <div data-testid="chart-tooltip-time" className="text-[10px] tabular-nums text-nb-500">{clockLabel(activePoint.t)}</div>
        </div>
      )}
    </div>
  )
}
