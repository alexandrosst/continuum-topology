import { qualityLabel, rttLabel } from '@/lib/metrics'
import { bytesPerSec } from '@/lib/observed'
import type { TopoEdge } from '@/lib/graph'

export type EdgeHoverPos = { cx: number; cy: number }

/**
 * A compact hover card for a graph/infrastructure-view edge - the canvas equivalent of MapView's own
 * `LinkCard` (same anchoring against `host`'s bounding box, same visual language), so hovering a line here
 * surfaces the traffic numbers the Inspector already shows once you click it, a click earlier. `stats` and
 * `quality` already ride on the edge's own data (graph.ts's EdgeData), so this never needs its own fetch or
 * a lookup back into the raw Dependency list.
 *
 * A single dependency (application view) shows its protocol/port, whether it's actually been seen, and
 * whatever of throughput/requests/errors/p95 the backend reported; an aggregated group<->group link (no
 * arrowhead, drawn only when "Cross-cluster links" is on) instead shows how many dependencies it bundles,
 * how many of those were seen in traffic, and their combined throughput. A telemetry edge to a regional
 * operator has neither `stats` nor `quality` set, so it falls through to just the two names and its label -
 * a graceful minimum rather than a special case.
 *
 * Round trip, retransmits, TLS server name, DNS queries and interface mirror the Inspector's own
 * "Traffic" section exactly (same eBPF-only gates) - so a click is never needed just to see numbers a
 * hover already had.
 *
 * Route only ever appears on a cross-cluster dependency: whether it lands on a flat/mesh-federated network
 * route, or has to go out through the target's own external exposure (ingress, node port or load balancer)
 * to be reached at all - the only two ways a call from outside the target's own cluster can land on it.
 */
export default function EdgeHoverCard({
  edge,
  pos,
  host,
  fromName,
  toName,
}: {
  edge: TopoEdge
  pos: EdgeHoverPos
  host: HTMLElement | null
  fromName: string
  toName: string
}) {
  const box = host?.getBoundingClientRect()
  if (!box) return null
  const d = edge.data
  const s = d?.stats
  const seen = !!d?.observed && !d?.stale
  // A conntrack-only dependency's bytesPerSec (if it has one at all) isn't a real measurement - conntrack
  // can't see bytes - so, same gate the Inspector uses, only trust it when eBPF made it or it's a genuine
  // positive number.
  const showBps = s?.bytesPerSec !== undefined && (d?.via === 'ebpf' || s.bytesPerSec > 0)
  // Retransmits are only ever a real measurement on an eBPF edge - same gate the Inspector uses (a
  // conntrack-only edge's 0 there means "not measured", not "no loss").
  const showRetransmits = d?.via === 'ebpf' && s?.retransmitsPerMin !== undefined
  // Same eBPF-only gate as retransmits; only worth a row at all when something has actually failed, since
  // "0 failed attempts" on every quiet, perfectly healthy edge would just be noise in a card this small.
  const showFailed = d?.via === 'ebpf' && !!d?.failedAttempts
  const showSni = d?.via === 'ebpf' && !!d?.sniHost
  const showDns = d?.via === 'ebpf' && !!d?.dnsQueryNames?.length
  // Jitter and handshake latency are both eBPF-only gauges, same as rttMs itself (no conntrack
  // equivalent exists for either).
  const showJitter = d?.via === 'ebpf' && d?.jitterMs !== undefined
  const showHandshake = d?.via === 'ebpf' && d?.handshakeMs !== undefined
  // Real loss % (retransmits / segs_out) is strictly better than the raw retransmits/min rate once a
  // segs_out denominator exists - but that denominator can be missing even on an eBPF edge (too young to
  // have sent a full segment yet), so this falls back to the /min rate rather than hiding the row.
  const showLossPct = showRetransmits && s?.lossPct !== undefined
  const left = Math.min(pos.cx - box.left + 14, box.width - 236)
  const top = Math.max(8, pos.cy - box.top - 12)
  const label = typeof edge.label === 'string' ? edge.label : undefined

  return (
    <div
      className="pointer-events-none absolute z-10 w-56 -translate-y-full rounded-lg border border-nb-800 bg-nb-920 p-3 text-xs shadow-xl"
      style={{ left, top }}
      data-testid="edge-hover-card"
    >
      <div className="truncate text-sm font-medium text-nb-300">
        {fromName} {d?.aggregated || d?.clusterLink ? '↔' : '→'} {toName}
      </div>
      <div className="mt-0.5 text-nb-500">
        {label}
        {d?.clusterLink
          ? ' · confirmed'
          : d?.aggregated
            ? d.activeCount ? ` · ${d.activeCount} seen in traffic` : undefined
            : label !== undefined && (d?.stale ? ' · quiet' : seen ? ' · seen in traffic' : ' · declared')}
      </div>
      {(showBps || s?.reqPerSec !== undefined || s?.errorRate !== undefined || s?.p95Ms !== undefined || d?.rttMs !== undefined || showJitter || showHandshake || showRetransmits || showFailed || showSni || showDns || d?.iface || d?.quality || d?.route || d?.clusterLink) && (
        <dl className="mt-1.5 grid grid-cols-[minmax(0,auto)_1fr] gap-x-3 gap-y-0.5 text-nb-400">
          {showBps && (
            <>
              <dt>{d?.aggregated ? 'Combined throughput' : 'Throughput'}</dt>
              <dd className="text-nb-200">{bytesPerSec(s!.bytesPerSec!)}</dd>
            </>
          )}
          {s?.reqPerSec !== undefined && (
            <>
              <dt>Requests</dt>
              <dd className="text-nb-200">{s.reqPerSec}/s</dd>
            </>
          )}
          {s?.errorRate !== undefined && (
            <>
              <dt>Errors</dt>
              <dd className="text-nb-200">{(s.errorRate * 100).toFixed(s.errorRate < 0.1 ? 1 : 0)}%</dd>
            </>
          )}
          {s?.p95Ms !== undefined && (
            <>
              <dt>p95 latency</dt>
              <dd className="text-nb-200">{s.p95Ms} ms</dd>
            </>
          )}
          {d?.rttMs !== undefined && (
            <>
              <dt title="Smoothed TCP round trip sampled from the kernel, not an active probe">TCP round trip</dt>
              <dd className="text-nb-200">{rttLabel(d.rttMs)}</dd>
            </>
          )}
          {showJitter && (
            <>
              <dt title="Kernel's own RTT mean-deviation, sampled alongside the round trip above">Jitter</dt>
              <dd className="text-nb-200">{rttLabel(d!.jitterMs!)}</dd>
            </>
          )}
          {showHandshake && (
            <>
              <dt title="Time from SYN to ESTABLISHED on this edge's most recent handshake - most telling on a cross-cluster/WAN edge">Connection setup</dt>
              <dd className="text-nb-200">{rttLabel(d!.handshakeMs!)}</dd>
            </>
          )}
          {showRetransmits && (
            <>
              <dt title={`${Math.round((s!.retransmitsPerMin ?? 0) * 10) / 10} retransmits/min`}>{showLossPct ? 'Loss' : 'Retransmits'}</dt>
              <dd className="text-nb-200">
                {showLossPct
                  ? `${s!.lossPct! < 10 ? s!.lossPct!.toFixed(1) : Math.round(s!.lossPct!)}%`
                  : `${Math.round((s!.retransmitsPerMin ?? 0) * 10) / 10}/min`}
              </dd>
            </>
          )}
          {showFailed && (
            <>
              <dt title="Connection attempts that never reached ESTABLISHED">Failed attempts</dt>
              <dd className="text-nb-200">{d!.failedAttempts} total</dd>
            </>
          )}
          {showSni && (
            <>
              <dt title="Hostname seen in this edge's TLS ClientHello (SNI), before the handshake encrypts anything">TLS server name</dt>
              <dd className="truncate text-nb-200" title={d!.sniHost}>{d!.sniHost}</dd>
            </>
          )}
          {showDns && (
            <>
              <dt title="Distinct domain names resolved toward this edge's destination">DNS queries</dt>
              <dd className="truncate text-nb-200" title={d!.dnsQueryNames!.join(', ')}>
                {d!.dnsQueryNames!.slice(0, 2).join(', ')}
                {d!.dnsQueryNames!.length > 2 ? ` +${d!.dnsQueryNames!.length - 2} more` : ''}
              </dd>
            </>
          )}
          {d?.iface && (
            <>
              <dt>Interface</dt>
              <dd className="text-nb-200">{d.iface}</dd>
            </>
          )}
          {d?.quality && (
            <>
              <dt>Network path</dt>
              <dd className="text-nb-200">{qualityLabel(d.quality)}</dd>
            </>
          )}
          {d?.route && (
            <>
              <dt title="How this cross-cluster call actually reaches its target: a flat network route, or out through the target's own external exposure (ingress, node port or load balancer)">Route</dt>
              <dd className="text-nb-200">{d.route === 'gateway' ? 'via gateway' : 'direct (peer network)'}</dd>
            </>
          )}
          {d?.clusterLink && (
            <>
              <dt title="The specific evidence behind this link - a tunnel interface's name and kind, or the shared subnet prefix - confirmed from both clusters' own routing/address data, never a guess">Via</dt>
              <dd className="truncate text-nb-200" title={d.clusterLink.via}>{d.clusterLink.via}</dd>
            </>
          )}
          {d?.clusterLink && d.clusterLink.redundancy > 1 && (
            <>
              <dt title="How many independently corroborating node pairs back this link - more than one means more than one path between these clusters, not a single point of failure">Redundancy</dt>
              <dd className="text-nb-200">{d.clusterLink.redundancy} independent paths</dd>
            </>
          )}
        </dl>
      )}
    </div>
  )
}
