import { ArrowDownUp, Cable, Cpu, Database, GitBranch, MemoryStick, Recycle, Server, Users, Waves, Zap, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import { ICON_SM, InfoTip, Pill, PulseDot } from '@/components/ui/primitives'
import { ageOf } from '@/lib/history'
import { bytesPerSec, bytesTotal } from '@/lib/observed'
import {
  entityLabel,
  fieldEverReported,
  freshnessOf,
  latestDefined,
  toPoints,
  type ChartPoint,
  type SelfTelemetryEntity,
} from '@/lib/selfHealth'
import TimeSeriesChart from './TimeSeriesChart'

/** Live (reporting within the last couple of report cycles) or stale (hasn't, with how long ago it
 *  last did) - the same dot-plus-word shape ObservationChip/StatusDot already use elsewhere in this app
 *  for "can this record be trusted right now", just with its own two states rather than importing
 *  ObservationChip's unrelated ObsInfo vocabulary (approval/connection state, not telemetry freshness). */
function FreshnessTag({ entity }: { entity: SelfTelemetryEntity }) {
  const f = freshnessOf(entity)
  if (!f) return null
  const ago = ageOf(new Date(f.lastSampleAt).toISOString())
  return f.live ? (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-ok" title={`Live - last sample ${ago}.`}>
      <PulseDot color="bg-ok" pulse size="size-1.5" /> Live
    </span>
  ) : (
    <span className="inline-flex items-center gap-1.5 text-xs font-medium text-nb-500" title={`No sample in a while - last one ${ago}.`}>
      <PulseDot color="bg-nb-600" size="size-1.5" /> Stale &middot; last sample {ago}
    </span>
  )
}

/** One metric's headline figure (the chart's own direct label, at full size - see TimeSeriesChart's own
 *  doc comment for why the chart itself stays unlabeled) plus its detailed trend underneath. `badge` is
 *  read-only config context next to the figure - a flow/probe interval, not a control. */
function MetricTile({
  label,
  icon: Icon,
  points,
  valueFormat,
  tickFormat,
  color = 'var(--color-accent)',
  help,
  badge,
}: {
  label: string
  icon: LucideIcon
  points: ChartPoint[]
  valueFormat: (v: number) => string
  tickFormat?: (v: number) => string
  color?: string
  help?: string
  badge?: ReactNode
}) {
  let current: number | undefined
  for (let i = points.length - 1; i >= 0; i--) {
    if (points[i].v !== undefined) {
      current = points[i].v
      break
    }
  }
  return (
    <div className="rounded-xl border border-nb-850 bg-nb-925 p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">
            <Icon size={ICON_SM} />
            {label}
            {help && <InfoTip>{help}</InfoTip>}
          </div>
          <div className="mt-1 text-xl font-medium tabular-nums text-nb-300">{current !== undefined ? valueFormat(current) : '—'}</div>
        </div>
        {badge}
      </div>
      <TimeSeriesChart
        points={points}
        color={color}
        valueFormat={valueFormat}
        tickFormat={tickFormat}
        ariaLabel={`${label} over the last hour, currently ${current !== undefined ? valueFormat(current) : 'no reading yet'}`}
      />
    </div>
  )
}

/** The honest-absence twin of MetricTile: same header shape (so the grid doesn't jump around depending
 *  on what a given entity happens to support), a calm explanation instead of a chart - never an empty or
 *  zeroed-out plot standing in for "this was never measured". */
function UnavailableMetricTile({ label, icon: Icon, reason, badge }: { label: string; icon: LucideIcon; reason: string; badge?: ReactNode }) {
  return (
    <div className="rounded-xl border border-dashed border-nb-850 p-4">
      <div className="mb-3 flex items-start justify-between gap-3">
        <div className="flex items-center gap-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">
          <Icon size={ICON_SM} />
          {label}
        </div>
        {badge}
      </div>
      <div className="flex items-center justify-center px-4 text-center text-xs text-nb-600" style={{ aspectRatio: '600 / 148' }}>
        {reason}
      </div>
    </div>
  )
}

const pct1 = (v: number) => `${v.toFixed(1)}%`
const pct2 = (v: number) => `${v.toFixed(2)}%`
const watts = (v: number) => `${v.toFixed(1)} W`
const msPerSec = (v: number) => `${v.toFixed(1)} ms/s`

/**
 * One entity's (the server itself, or one connected agent/cluster) self-telemetry: a header naming it and
 * saying whether it's currently live, then a grid of per-metric tiles - memory, goroutines and CPU for
 * every entity, plus bandwidth share and host watts for an agent whenever that cluster has actually
 * reported one (see UnavailableMetricTile above for when it hasn't). The server entity never gets those
 * last two tiles at all, not even an "unavailable" one - they describe a *cluster's* network/power
 * footprint and a bare server process has no cluster of its own to report one for, so showing a
 * permanently-impossible tile for it would be noise dressed up as a hardware limitation (admin_telemetry.go
 * never sets either field for a "server" entity, by design). The server entity gets four tiles of its own
 * instead - connected agents, flow-ingest rate, model-cache hit rate and GC pause time - each describing
 * the server process itself rather than a cluster, so there is nothing for an agent entity to show here.
 */
export default function EntityHealthCard({ entity }: { entity: SelfTelemetryEntity }) {
  const flowInterval = latestDefined(entity, 'flowIntervalSeconds')
  const probeInterval = latestDefined(entity, 'probeIntervalSeconds')
  const hasBandwidth = fieldEverReported(entity, 'bandwidthSharePct')
  const hasWatts = fieldEverReported(entity, 'watts')
  const Icon = entity.kind === 'server' ? Server : Cable

  return (
    <section className="rounded-2xl border border-nb-850 bg-nb-920 p-5">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <Icon size={ICON_SM} className="text-nb-500" />
          <h2 className="text-base font-medium text-nb-300">{entityLabel(entity)}</h2>
          <Pill>{entity.kind === 'server' ? 'Server' : 'Agent'}</Pill>
        </div>
        <FreshnessTag entity={entity} />
      </div>

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-3">
        <MetricTile
          label="RSS memory"
          icon={MemoryStick}
          points={toPoints(entity, 'rssBytes')}
          valueFormat={bytesTotal}
          help="Resident memory this process is actually using right now, not what it was allocated."
        />
        <MetricTile
          label="Goroutines"
          icon={GitBranch}
          points={toPoints(entity, 'goroutines')}
          valueFormat={(v) => Math.round(v).toLocaleString()}
          help="Live goroutines inside the process - a rough proxy for how much concurrent work it's doing right now."
        />
        <MetricTile
          label="CPU usage"
          icon={Cpu}
          points={toPoints(entity, 'cpuPct')}
          valueFormat={pct1}
          tickFormat={(v) => `${Math.round(v)}%`}
          help="Average CPU percent since the previous sample. Omitted on an entity's very first sample - there is nothing yet to derive a rate from."
        />

        {entity.kind === 'agent' &&
          (hasBandwidth ? (
            <MetricTile
              label="Bandwidth share"
              icon={Waves}
              points={toPoints(entity, 'bandwidthSharePct')}
              valueFormat={pct2}
              help="Ikhnos's own agent traffic as a share of this cluster's total observed network throughput - never an addition on top of it, since the total already counts every byte on the wire, Ikhnos's included."
              badge={flowInterval !== undefined && <Pill title="How often this cluster's flow collector reports, which is what drives this number">Flow report every {flowInterval}s</Pill>}
            />
          ) : (
            <UnavailableMetricTile
              label="Bandwidth share"
              icon={Waves}
              reason="Not available for this cluster - no flow collector has reported network throughput for it yet."
              badge={flowInterval !== undefined && <Pill title="How often this cluster's flow collector reports">Flow report every {flowInterval}s</Pill>}
            />
          ))}

        {entity.kind === 'server' && (
          <MetricTile
            label="Connected agents"
            icon={Users}
            points={toPoints(entity, 'connectedAgents')}
            valueFormat={(v) => Math.round(v).toLocaleString()}
            help="How many agents currently hold a live connection to this server."
          />
        )}
        {entity.kind === 'server' && (
          <MetricTile
            label="Flow ingest rate"
            icon={ArrowDownUp}
            points={toPoints(entity, 'flowIngestBytesPerSec')}
            valueFormat={bytesPerSec}
            help="Flow traffic, as encoded on the wire, received across every connected agent, per second."
          />
        )}
        {entity.kind === 'server' && (
          <MetricTile
            label="Model cache hit rate"
            icon={Database}
            points={toPoints(entity, 'modelCacheHitPct')}
            valueFormat={pct1}
            help="This interval's own share of effective-model requests served from cache rather than rebuilt. Omitted on an entity's very first sample, or any interval with no cache lookups at all."
          />
        )}
        {entity.kind === 'server' && (
          <MetricTile
            label="GC pause time"
            icon={Recycle}
            points={toPoints(entity, 'gcPauseMsPerSec')}
            valueFormat={msPerSec}
            help="Milliseconds the Go garbage collector spent in a stop-the-world pause, per second of wall-clock time, since the previous sample."
          />
        )}

        {entity.kind === 'agent' &&
          (hasWatts ? (
            <MetricTile
              label="Host watts"
              icon={Zap}
              points={toPoints(entity, 'watts')}
              valueFormat={watts}
              help="This cluster's total host power draw, summed across every node that exposes an Intel RAPL reading - a live snapshot attached to every sample, not its own separately-measured history."
              badge={probeInterval !== undefined && <Pill title="How often this cluster's node probes report, which is what drives this number">Node probe every {probeInterval}s</Pill>}
            />
          ) : (
            <UnavailableMetricTile
              label="Host watts"
              icon={Zap}
              reason="Not available on this hardware - none of this cluster's nodes expose an Intel RAPL power reading."
              badge={probeInterval !== undefined && <Pill title="How often this cluster's node probes report">Node probe every {probeInterval}s</Pill>}
            />
          ))}
      </div>

      {entity.kind === 'agent' && hasWatts && (
        <p className="mt-3 text-xs text-nb-600">
          That figure is this cluster&apos;s whole host power draw, not Ikhnos&apos;s own slice of it - estimating Ikhnos&apos;s own share is still on the roadmap.
        </p>
      )}
    </section>
  )
}
