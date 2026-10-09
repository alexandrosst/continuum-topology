import { Hourglass } from 'lucide-react'
import { MeterBar } from '@/components/ui/MeterBar'
import { ICON_SM } from '@/components/ui/primitives'
import type { FusionRetentionStore } from '@/lib/api'
import { daysText, diskBar, fillsInDays, formatBytes, keptBar } from '@/lib/fusionRetention'

/** A store's volume as a bar with the figure beside it. What else is known (the storage class, the growth a day) is its tooltip. */
export function DiskBar({ s }: { s: FusionRetentionStore }) {
  const bar = diskBar(s)
  if (!bar) return null
  const more = [s.storageClass, s.bytesPerDay !== undefined ? `about ${formatBytes(s.bytesPerDay)} a day` : ''].filter(Boolean).join(', ')
  return (
    <span className="inline-flex items-center gap-2 text-xs" title={more || undefined} data-testid={`fusion-disk-${s.component}`}>
      <span className="text-nb-500">Disk</span>
      <MeterBar pct={bar.pct} tone={bar.tone} className="w-24" label={`${s.label} disk use`} valueText={`${bar.text} used`} />
      <span className="w-32 whitespace-nowrap tabular-nums text-nb-400">{bar.text}</span>
    </span>
  )
}

/** How much of the retention window holds data, beside the days it keeps. */
export function KeptBar({ s }: { s: FusionRetentionStore }) {
  const bar = keptBar(s)
  if (!bar) return null
  return (
    <span className="inline-flex items-center gap-2 text-xs" data-testid={`fusion-kept-${s.component}`}>
      <MeterBar pct={bar.pct} tone={bar.tone} className="w-24" label={`${s.label} data held`} valueText={bar.text} />
      <span className="w-32 whitespace-nowrap tabular-nums text-nb-400">{bar.text}</span>
    </span>
  )
}

/** "fills in about 3 days", only when the figures say it will. */
export function FillsIn({ s }: { s: FusionRetentionStore }) {
  const days = fillsInDays(s)
  if (days === null) return null
  return (
    <span className="inline-flex items-center gap-1 text-xs text-warn" data-testid={`fusion-fills-${s.component}`}>
      <Hourglass size={ICON_SM} aria-hidden /> fills in about {daysText(days)}
    </span>
  )
}
