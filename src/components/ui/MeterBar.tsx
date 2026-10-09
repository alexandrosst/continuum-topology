import clsx from 'clsx'
import type { ComponentProps } from 'react'

export type BarTone = 'ok' | 'warn' | 'bad' | 'neutral'
const FILL: Record<BarTone, string> = { ok: 'bg-ok', warn: 'bg-warn', bad: 'bg-bad', neutral: 'bg-nb-500' }

/** A thin bar for a figure that is a share of something (how fresh, how much of a lifetime is left, how full): a meter for assistive
 *  technology, with its value in words, so the bar is never the only way the figure is said. `pct` is 0 to 100; the width is the track's
 *  (`w-14` unless `className` says otherwise). Eases to a new reading like the load gauges (meter-bar). */
export function MeterBar({ pct, tone, label, valueText, className, ...p }: { pct: number; tone: BarTone; label: string; valueText: string; className?: string } & Pick<ComponentProps<'div'>, 'tabIndex' | 'title'>) {
  const width = Math.max(0, Math.min(100, pct))
  return (
    <div {...p} role="meter" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(width)} aria-valuetext={valueText} className={clsx('h-1 shrink-0 overflow-hidden rounded-full bg-nb-850', className ?? 'w-14')}>
      <div className={clsx('h-full rounded-full meter-bar', FILL[tone])} style={{ width: `${width}%` }} />
    </div>
  )
}
