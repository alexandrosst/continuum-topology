import { qualityLabel, rttLabel } from '@/lib/metrics'
import { bytesPerSec } from '@/lib/observed'
import { linkUtilizationPct } from '@/lib/present'
import { DetailRow, TunnelEvidence } from '@/components/ui/primitives'
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
 * how many of those were seen in traffic, their combined throughput, and - only when the bundle actually
 * mixes more than one (graph.ts's EdgeData.protocols is unset otherwise) - a breakdown by protocol, so
 * "14 dependencies" doesn't hide that it's really 9 HTTP calls, 4 Kafka and 1 gRPC. A telemetry edge to a
 * regional operator has neither `stats` nor `quality` set, so it falls through to just the two names and
 * its label - a graceful minimum rather than a special case.
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
  // DNS response latency is a gauge, same convention as rttMs/jitterMs - UDP-only, and only ever set
  // when the same opt-in that surfaces dnsQueryNames above is on (it's read off the correlated query/
  // response pair, same privacy posture as the query name itself).
  const showDnsRtt = d?.via === 'ebpf' && d?.dnsRttMs !== undefined
  // Aggregated edges only - unset whenever the bundle happens to be all one protocol, so the plain
  // count in the subtitle line below is left to speak for itself in that common case.
  const protocolMix = d?.protocols && Object.entries(d.protocols).sort((a, b) => b[1] - a[1])
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
  // The standalone ClusterLink edge and a dependency edge crossing a confirmed tunnel (EdgeData.tunnelLink)
  // never coexist on the same edge, and share the same via/redundancy/encryption shape - unified here so
  // the three rows below render identically either way. flowsObserved/avgRttMs/avgLossPct stay exclusive
  // to clusterLink: an aggregate across every dependency crossing the link, never a fact about just one.
  const tunnel = d?.clusterLink ?? d?.tunnelLink

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
      {(showBps || s?.reqPerSec !== undefined || s?.errorRate !== undefined || s?.p95Ms !== undefined || d?.rttMs !== undefined || showJitter || showHandshake || showRetransmits || showFailed || showSni || showDns || showDnsRtt || d?.iface || d?.quality || d?.route || tunnel || protocolMix) && (
        <div className="mt-1.5">
          {protocolMix && (
            <DetailRow dense label="Protocols">
              {protocolMix.map(([proto, n]) => `${proto} \u00d7${n}`).join(', ')}
            </DetailRow>
          )}
          {showBps && (
            <DetailRow dense label={d?.aggregated ? 'Combined throughput' : 'Throughput'}>
              {bytesPerSec(s!.bytesPerSec!)}
              {/* The caller's own interface capacity (present.ts's callerIfaceSpeedMbps, resolved once in
                  graph.ts), right next to the achieved rate it's being measured against - never shown for
                  an aggregated group<->group link, which has no single caller interface to speak of. */}
              {!d?.aggregated && d?.ifaceSpeedMbps !== undefined && linkUtilizationPct(s!.bytesPerSec!, d.ifaceSpeedMbps) !== undefined && (
                <span className="text-nb-500"> ({linkUtilizationPct(s!.bytesPerSec!, d.ifaceSpeedMbps)}% of {d!.iface}'s {d.ifaceSpeedMbps} Mbps)</span>
              )}
            </DetailRow>
          )}
          {s?.reqPerSec !== undefined && <DetailRow dense label="Requests">{s.reqPerSec}/s</DetailRow>}
          {s?.errorRate !== undefined && (
            <DetailRow dense label="Errors">{(s.errorRate * 100).toFixed(s.errorRate < 0.1 ? 1 : 0)}%</DetailRow>
          )}
          {s?.p95Ms !== undefined && <DetailRow dense label="p95 latency">{s.p95Ms} ms</DetailRow>}
          {d?.rttMs !== undefined && (
            <DetailRow dense label="TCP round trip" labelTitle="Smoothed TCP round trip sampled from the kernel, not an active probe">
              {rttLabel(d.rttMs)}
            </DetailRow>
          )}
          {showJitter && (
            <DetailRow dense label="Jitter" labelTitle="Kernel's own RTT mean-deviation, sampled alongside the round trip above">
              {rttLabel(d!.jitterMs!)}
            </DetailRow>
          )}
          {showHandshake && (
            <DetailRow dense label="Connection setup" labelTitle="Time from SYN to ESTABLISHED on this edge's most recent handshake - most telling on a cross-cluster/WAN edge">
              {rttLabel(d!.handshakeMs!)}
            </DetailRow>
          )}
          {showRetransmits && (
            <DetailRow dense label={showLossPct ? 'Loss' : 'Retransmits'} labelTitle={`${Math.round((s!.retransmitsPerMin ?? 0) * 10) / 10} retransmits/min`}>
              {showLossPct
                ? `${s!.lossPct! < 10 ? s!.lossPct!.toFixed(1) : Math.round(s!.lossPct!)}%`
                : `${Math.round((s!.retransmitsPerMin ?? 0) * 10) / 10}/min`}
            </DetailRow>
          )}
          {showFailed && (
            <DetailRow dense label="Failed attempts" labelTitle="Connection attempts that never reached ESTABLISHED">
              {d!.failedAttempts} total
            </DetailRow>
          )}
          {showSni && (
            <DetailRow dense label="TLS server name">
              <span title={d!.sniHost}>{d!.sniHost}</span>
            </DetailRow>
          )}
          {showDns && (
            <DetailRow dense label="DNS queries" labelTitle="Distinct domain names resolved toward this edge's destination">
              <span title={d!.dnsQueryNames!.join(', ')}>
                {d!.dnsQueryNames!.slice(0, 2).join(', ')}
                {d!.dnsQueryNames!.length > 2 ? ` +${d!.dnsQueryNames!.length - 2} more` : ''}
              </span>
            </DetailRow>
          )}
          {showDnsRtt && (
            <DetailRow dense label="DNS response" labelTitle="Time from query to matching response, correlated by transaction ID - UDP only">
              {rttLabel(d!.dnsRttMs!)}
            </DetailRow>
          )}
          {d?.iface && <DetailRow dense label="Interface">{d.iface}</DetailRow>}
          {d?.quality && <DetailRow dense label="Network path">{qualityLabel(d.quality)}</DetailRow>}
          {d?.route && (
            <DetailRow
              dense
              label="Route"
              labelTitle="How this cross-cluster call actually reaches its target: a flat network route, or out through the target's own external exposure (ingress, node port or load balancer)"
            >
              {d.route === 'gateway' ? 'via gateway' : 'direct (peer network)'}
            </DetailRow>
          )}
          {tunnel && (
            // Via/encryption/redundancy/nodes/addresses/flows - the same shared block the Inspector's
            // own "Cluster links" list and per-dependency "Cluster link" section render, so this hover
            // card never shows a different subset of this exact same evidence than a click would. Only
            // d.clusterLink (never d.tunnelLink, see its own doc above) ever carries nodeA/nodeB,
            // addressA/addressB or the flow rollup - TunnelEvidence simply renders no row for whichever
            // of those tunnelLink leaves undefined.
            <TunnelEvidence
              dense
              via={tunnel.via}
              encryption={tunnel.encryption}
              redundancy={tunnel.redundancy}
              nodeA={d?.clusterLink?.fromNode}
              nodeB={d?.clusterLink?.toNode}
              addressA={d?.clusterLink?.fromAddress}
              addressB={d?.clusterLink?.toAddress}
              flowsObserved={d?.clusterLink?.flowsObserved}
              avgRttMs={d?.clusterLink?.avgRttMs}
              avgLossPct={d?.clusterLink?.avgLossPct}
            />
          )}
        </div>
      )}
    </div>
  )
}
