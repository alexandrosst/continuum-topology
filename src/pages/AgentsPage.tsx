import clsx from 'clsx'
import { ChevronDown, ChevronRight, List, Network, Plug, Server } from 'lucide-react'
import { Fragment, useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useSearchParams } from 'react-router-dom'
import { CanSee, ConsentPanel, CopyCommand as CopyableCommand, DiscoveryChip, HealthChip, Problems } from '@/components/agents/AgentInsight'
import { CheckLine, MODULE_STYLE, ObserverLine, ScopeLine, STATUS_STYLE, when } from '@/components/discovery/AgentParts'
import ApprovalCard from '@/components/discovery/ApprovalCard'
import { useConnectFlow } from '@/components/discovery/ConnectFlow'
import { ConfirmModal } from '@/components/forms'
import { DistroIcon, WithIcon } from '@/components/ui/brand'
import { Button, EmptyState, ErrorBanner, PageHeader, Table, Td, Th, TierBadge } from '@/components/ui/primitives'
import { api, ApiError } from '@/lib/api'
import { skewLabel, skewWarning } from '@/lib/clock'
import { extrasOf, type AgentExtras } from '@/lib/consent'
import { ageOf } from '@/lib/history'
import { bytesTotal } from '@/lib/observed'
import { ipScope, ipScopeLabel } from '@/lib/present'
import { ACCESS_TIERS, type Agent, type Cluster, type Tier } from '@/lib/types'
import { useApprovalLocks } from '@/store/approvalLocks'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

/** How an agent is doing right now, from what the server can tell. */
type Health = 'ok' | 'late' | 'offline' | 'waiting' | 'expired' | 'gone' | 'unknown'
const HEALTH: Record<Health, { label: string; dot: string; text: string }> = {
  ok: { label: 'Connected', dot: 'bg-emerald-400', text: 'text-emerald-300' },
  late: { label: 'Heartbeat late', dot: 'bg-amber-400', text: 'text-amber-300' },
  offline: { label: 'Disconnected', dot: 'bg-red-400', text: 'text-red-300' },
  waiting: { label: 'Waiting for approval', dot: 'bg-amber-400', text: 'text-amber-300' },
  expired: { label: 'Request expired', dot: 'bg-nb-600', text: 'text-nb-400' },
  gone: { label: 'Revoked', dot: 'bg-nb-600', text: 'text-nb-500' },
  unknown: { label: 'Not live', dot: 'bg-nb-600', text: 'text-nb-400' },
}
/** Agents send a heartbeat every 30 seconds; two missed is "late". */
const LATE_AFTER_MS = 75_000

function healthOf(a: Agent, now: number): Health {
  if (a.status === 'pending') return 'waiting'
  if (a.status === 'expired') return 'expired'
  if (a.status !== 'approved') return 'gone'
  if (a.connected === false) return 'offline'
  // Sample and hand-made agents have no live link to judge, so they are not called disconnected.
  if (a.connected === undefined) return 'unknown'
  return a.lastHeartbeat && now - new Date(a.lastHeartbeat).getTime() > LATE_AFTER_MS ? 'late' : 'ok'
}

/** Milliseconds until a certificate expires (negative once it has). */
const msUntil = (iso?: string, now = Date.now()) => (iso ? new Date(iso).getTime() - now : undefined)
/** Agent certificates are short-lived and renewed by the agent itself, so hours are the honest unit. */
const inWords = (ms: number) => (ms < 3_600_000 ? `${Math.max(1, Math.round(ms / 60_000))} min` : ms < 48 * 3_600_000 ? `${Math.round(ms / 3_600_000)} h` : `${Math.round(ms / 86_400_000)} d`)
const tierOf = (a: Agent) => ACCESS_TIERS.find((t) => t.value === a.accessTier)?.label ?? `Tier ${a.accessTier}`
/** "now", "5m", "3h", "2d": for places with no room for a sentence. */
const shortAge = (iso: string, now = Date.now()) => {
  const s = Math.max(0, (now - new Date(iso).getTime()) / 1000)
  return s < 90 ? 'now' : s < 5400 ? `${Math.round(s / 60)}m ago` : s < 129600 ? `${Math.round(s / 3600)}h ago` : `${Math.round(s / 86400)}d ago`
}
const num = (n: number) => n.toLocaleString()

export default function AgentsPage() {
  const { agents, clusters, sites } = useTopology()
  const server = useServer()
  const [sp, setSp] = useSearchParams()
  const map = sp.get('view') === 'map'
  const [open, setOpen] = useState<string | null>(null)
  const [filter, setFilter] = useState<'all' | Health>('all')
  const [revoking, setRevoking] = useState<Agent | null>(null)
  const [error, setError] = useState('')
  // A clock the page reads once per render: the poll re-renders it every few seconds, which keeps "ago" honest.
  const now = Date.now()

  // Diagnostics and overrides are in the raw state document, and only for people who may change an agent's access.
  const rawAgents = server.state?.agents
  const connected = server.status === 'connected'
  const canConsent = connected && server.canEdit()
  const connect = useConnectFlow()
  const canAdminister = connect.canStart
  // Enrollment is decided here: a request waits for its approval code, and one rejected for too many wrong codes stays (with the reason) until dismissed.
  const locked = useApprovalLocks((s) => s.locked)
  const waiting = agents.filter((a) => a.status === 'pending' && a.requestedAt)
  const pending = [...waiting, ...agents.filter((a) => a.status === 'rejected' && locked[a.id])]
  // A link to one request (`/agents#approval-<id>`, from the getting-started list) scrolls to its card once it is on the page.
  const { hash } = useLocation()
  const target = pending.some((a) => `#approval-${a.id}` === hash) || hash === '#approvals' ? hash : ''
  useEffect(() => {
    if (!target) return
    const el = document.getElementById(target.slice(1))
    el?.scrollIntoView({ block: 'start' })
    el?.focus({ preventScroll: true })
  }, [target])
  const cluster = (id?: string) => clusters.find((c) => c.id === id)
  const site = (c?: Cluster) => sites.find((s) => s.id === c?.siteId)

  const rows = useMemo(() => agents.map((a) => ({ a, h: healthOf(a, now) })), [agents, now])
  const counts = useMemo(() => {
    const c: Record<string, number> = { all: rows.length }
    for (const r of rows) c[r.h] = (c[r.h] ?? 0) + 1
    return c
  }, [rows])
  const shown = rows.filter((r) => filter === 'all' || r.h === filter)
  const sent = agents.reduce((n, a) => n + (a.link?.bytes ?? 0), 0)
  const soonest = agents.filter((a) => a.status === 'approved' && a.connected !== undefined).map((a) => msUntil(a.certExpiresAt, now)).filter((d): d is number => d !== undefined).sort((x, y) => x - y)[0]
  const openAgent = agents.find((a) => a.id === open)

  const revoke = async (a: Agent) => {
    const c = server.conn()
    if (!c) return
    try {
      await api.revoke(c, a.id, 'revoked in the UI')
      await server.refresh()
    } catch (e) {
      setError(e instanceof ApiError ? e.message : 'Could not revoke.')
    }
  }

  return (
    <>
      <PageHeader
        title="Agents"
        description="The programs running inside your clusters that report to this server: where each runs, whether it is alive, and how much it has sent."
        actions={
          canAdminister && (
            <Button variant="primary" onClick={connect.start}>
              <Plug size={16} /> Connect a cluster
            </Button>
          )
        }
      />

      {/* While the wizard is open it shows the same card, so it is not repeated behind the dialog. */}
      {pending.length > 0 && !connect.wizardOpen && (
        <section id="approvals" tabIndex={-1} aria-labelledby="approvals-title" className="mb-8 scroll-mt-6 space-y-3 focus:outline-none" data-testid="approvals">
          <h2 id="approvals-title" className="text-sm font-medium text-white">Waiting for your approval</h2>
          {canAdminister ? (
            pending.map((a) => (
              <div key={a.id} id={`approval-${a.id}`} tabIndex={-1} className="scroll-mt-6 focus:outline-none">
                <ApprovalCard agent={a} />
              </div>
            ))
          ) : (
            <p className="rounded-xl border border-nb-850 bg-nb-925 px-5 py-4 text-sm text-nb-400">
              {waiting.length} cluster{waiting.length === 1 ? ' is' : 's are'} waiting. An administrator has to approve {waiting.length === 1 ? 'it' : 'them'}.
            </p>
          )}
        </section>
      )}

      {agents.length === 0 ? (
        <EmptyState
          title="No cluster is connected yet"
          description="Agents run inside your clusters and report to this server. Connect a cluster to install one: it dials out to this server, and nothing is read until you approve it."
          action={canAdminister ? <Button variant="primary" onClick={connect.start}><Plug size={16} /> Connect a cluster</Button> : undefined}
        />
      ) : (
        <>
          <div className="mb-6 grid grid-cols-2 gap-3 lg:grid-cols-4" data-testid="agent-stats">
            <Stat label="Connected" value={`${counts.ok ?? 0} of ${rows.filter((r) => r.a.status === 'approved' && r.h !== 'unknown').length}`} sub={(counts.late ?? 0) + (counts.offline ?? 0) > 0 ? `${(counts.late ?? 0) + (counts.offline ?? 0)} need a look` : (counts.ok ?? 0) === 0 ? 'no agent is reporting live' : 'all approved agents are up'} warn={(counts.late ?? 0) + (counts.offline ?? 0) > 0} />
            <Stat label="Waiting for approval" value={String(counts.waiting ?? 0)} sub={counts.waiting ? 'review them above' : 'no request is waiting'} warn={!!counts.waiting} />
            <Stat label="Received" value={connected ? bytesTotal(sent) : '—'} sub={connected ? 'since the server started' : 'needs a server'} />
            <Stat label="Next certificate expiry" value={soonest === undefined ? '—' : soonest < 0 ? 'expired' : inWords(soonest)} sub={soonest === undefined ? 'no certificates yet' : 'agents renew on their own'} warn={soonest !== undefined && soonest < 0} />
          </div>

          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Show agents">
              {(['all', 'ok', 'late', 'offline', 'waiting', 'gone', 'unknown'] as const)
                .filter((k) => k === 'all' || counts[k])
                .map((k) => (
                  <button
                    key={k}
                    onClick={() => setFilter(k)}
                    aria-pressed={filter === k}
                    className={clsx('rounded-full border px-3 py-1 text-xs transition-colors', filter === k ? 'border-accent/40 bg-accent-soft text-accent' : 'border-nb-850 text-nb-400 hover:border-nb-800 hover:text-nb-300')}
                  >
                    {k === 'all' ? 'All' : HEALTH[k].label} <span className="text-nb-500">{counts[k]}</span>
                  </button>
                ))}
            </div>
            <div className="flex overflow-hidden rounded-md border border-nb-850" role="tablist" aria-label="How to show agents">
              {[{ id: false, label: 'List', icon: List }, { id: true, label: 'Map', icon: Network }].map(({ id, label, icon: I }) => (
                <button
                  key={label}
                  role="tab"
                  aria-selected={map === id}
                  onClick={() => setSp((p) => { const n = new URLSearchParams(p); if (id) n.set('view', 'map'); else n.delete('view'); return n }, { replace: true })}
                  className={clsx('flex items-center gap-1.5 px-3 py-1.5 text-sm', map === id ? 'bg-nb-940 text-white' : 'text-nb-400 hover:text-nb-300')}
                  data-testid={`agents-${label.toLowerCase()}`}
                >
                  <I size={14} aria-hidden /> {label}
                </button>
              ))}
            </div>
          </div>

          {error && <ErrorBanner className="mb-4">{error}</ErrorBanner>}

          {map ? (
            <AgentMap rows={shown} clusterOf={cluster} selected={open} onSelect={setOpen} />
          ) : (
            <Table data-testid="agents-table">
              <thead>
                <tr>
                  <Th>Agent</Th><Th>Runs in</Th><Th>Connection</Th><Th>Sent</Th><Th>Access</Th><Th>Status</Th>
                  <Th className="w-10" actionsLabel="Details" />
                </tr>
              </thead>
              <tbody>
                {shown.map(({ a, h }) => {
                  const c = cluster(a.clusterId)
                  const s = site(c)
                  const expanded = open === a.id
                  const st = HEALTH[h]
                  return (
                    <Fragment key={a.id}>
                      <tr className="group cursor-pointer hover:bg-nb-930/60" onClick={() => setOpen(expanded ? null : a.id)} data-testid="agent-row">
                        <Td valign="top">
                          <div className="flex items-center gap-2 whitespace-nowrap font-medium text-white">
                            {expanded ? <ChevronDown size={14} className="text-nb-500" aria-hidden /> : <ChevronRight size={14} className="text-nb-500" aria-hidden />}
                            {a.name}
                          </div>
                          <div className="pl-[22px] text-xs text-nb-500">v{a.version}{a.kubernetesVersion ? ` · Kubernetes ${a.kubernetesVersion}` : ''}</div>
                        </Td>
                        <Td valign="top">
                          {c ? (
                            <>
                              <Link to={`/topology?sel=${encodeURIComponent(`cluster:${c.id}`)}`} onClick={(e) => e.stopPropagation()} className="hover:text-white hover:underline">
                                <WithIcon icon={<DistroIcon distribution={c.distribution} size={16} />}>{c.name}</WithIcon>
                              </Link>
                              <div className="mt-0.5 flex items-center gap-2 text-xs text-nb-500"><TierBadge tier={c.tier as Tier} />{s?.name ?? c.region}</div>
                              {a.scope && a.scope.inScope < a.scope.namespaces && (
                                <div className="mt-0.5 text-xs text-amber-300" title={a.scope.description} data-testid="scope-badge">{a.scope.inScope} of {a.scope.namespaces} namespaces</div>
                              )}
                            </>
                          ) : (
                            <span className="text-nb-500">no cluster yet</span>
                          )}
                        </Td>
                        <Td valign="top">
                          <div className="flex items-center gap-2 whitespace-nowrap">
                            <span className={clsx('size-2 rounded-full', st.dot)} aria-hidden />
                            <span className={st.text}>{st.label}</span>
                          </div>
                          <div className="text-xs text-nb-500" title={a.lastHeartbeat ? when(a.lastHeartbeat) : undefined}>
                            {a.lastHeartbeat ? `heartbeat ${ageOf(a.lastHeartbeat)}` : 'no heartbeat yet'}
                            {a.link?.connectedSince && ` · up ${ageOf(a.link.connectedSince).replace(' ago', '')}`}
                          </div>
                          {a.status === 'approved' && extrasOf(rawAgents, a.id).diagnostics && (
                            <div className="mt-1 flex flex-wrap items-center gap-1.5">
                              <HealthChip diagnostics={extrasOf(rawAgents, a.id).diagnostics} connected={a.connected} />
                              <DiscoveryChip diagnostics={extrasOf(rawAgents, a.id).diagnostics} />
                            </div>
                          )}
                          {skewWarning(a.clockSkewMs) && a.clockSkewMs !== undefined && (
                            <span className="mt-1 inline-block rounded-full border border-amber-400/40 bg-amber-400/10 px-2 py-px text-[11px] font-medium text-amber-300" title={skewWarning(a.clockSkewMs)} data-testid="clock-chip">
                              {skewLabel(a.clockSkewMs)}
                            </span>
                          )}
                        </Td>
                        <Td valign="top" className="whitespace-nowrap tabular-nums">
                          {a.link ? (
                            <>
                              <div className="text-nb-300">{bytesTotal(a.link.bytes)}</div>
                              <div className="text-xs text-nb-500">{num(a.link.syncs + a.link.flows + a.link.measurements + a.link.heartbeats)} messages</div>
                            </>
                          ) : (
                            <span className="text-nb-600">—</span>
                          )}
                        </Td>
                        <Td valign="top" className="whitespace-nowrap" title={`Tier ${a.accessTier}`}>{tierOf(a)}</Td>
                        <Td valign="top">
                          <span className={clsx('rounded-full border px-2 py-0.5 text-xs font-medium capitalize', STATUS_STYLE[a.status])}>{a.status}</span>
                        </Td>
                        <Td valign="top" className="text-nb-600" />
                      </tr>
                      {expanded && (
                        <tr>
                          <Td colSpan={7} className="bg-nb-930/40 py-4">
                            <AgentDetail agent={a} extras={extrasOf(rawAgents, a.id)} canConsent={canConsent} onRevoke={canAdminister && connected && a.status === 'approved' ? () => setRevoking(a) : undefined} />
                          </Td>
                        </tr>
                      )}
                    </Fragment>
                  )
                })}
                {shown.length === 0 && (
                  <tr><Td colSpan={7} className="py-8 text-center text-nb-500">No agents in this state.</Td></tr>
                )}
              </tbody>
            </Table>
          )}

          {map && openAgent && (
            <div className="mt-4 rounded-xl border border-nb-850 bg-nb-925 px-5 py-4" data-testid="agent-detail">
              <div className="mb-3 flex items-center justify-between">
                <h2 className="text-sm font-medium text-white">{openAgent.name}</h2>
                <button className="text-xs text-nb-500 hover:text-nb-300" onClick={() => setOpen(null)}>Close</button>
              </div>
              <AgentDetail agent={openAgent} extras={extrasOf(rawAgents, openAgent.id)} canConsent={canConsent} onRevoke={canAdminister && connected && openAgent.status === 'approved' ? () => setRevoking(openAgent) : undefined} />
            </div>
          )}
        </>
      )}

      {connect.dialogs}

      {revoking && (
        <ConfirmModal
          title={`Revoke ${revoking.name}?`}
          message="The agent is disconnected at once and cannot come back with its current identity. Records it discovered stay in your topology. To reconnect the cluster you enroll it again with a new token."
          onConfirm={() => revoke(revoking)}
          onClose={() => setRevoking(null)}
        />
      )}
    </>
  )
}

function Stat({ label, value, sub, warn, to }: { label: string; value: string; sub: string; warn?: boolean; to?: string }) {
  const body = (
    <div className={clsx('h-full rounded-xl border bg-nb-925 px-5 py-4', warn ? 'border-amber-400/30' : 'border-nb-850')}>
      <div className="text-xs text-nb-500">{label}</div>
      <div className={clsx('mt-1 text-2xl font-medium tabular-nums', warn ? 'text-amber-300' : 'text-white')}>{value}</div>
      <div className="mt-0.5 text-xs text-nb-500">{sub}</div>
    </div>
  )
  return to ? <Link to={to} className="block">{body}</Link> : body
}

/** Everything the server knows about one agent that does not fit in a row. */
function AgentDetail({ agent: a, extras, canConsent, onRevoke }: { agent: Agent; extras: AgentExtras; canConsent: boolean; onRevoke?: () => void }) {
  const l = a.link
  const certLeft = msUntil(a.certExpiresAt)
  const ip = a.connectingIp
  return (
    <div className="grid gap-x-10 gap-y-5 text-sm md:grid-cols-2 xl:grid-cols-3">
      {a.status === 'approved' && extras.diagnostics && (
        <div className="md:col-span-2 xl:col-span-3" data-testid="agent-health">
          <div className="grid gap-x-10 gap-y-5 md:grid-cols-2">
            <div>
              <Heading>What this agent can see</Heading>
              <div className="mb-2 flex flex-wrap items-center gap-2"><HealthChip diagnostics={extras.diagnostics} connected={a.connected} /><DiscoveryChip diagnostics={extras.diagnostics} /><span className="text-xs text-nb-600">from the agent's own report, {ageOf(extras.diagnostics.reportedAt)}</span></div>
              <CanSee agent={a} diagnostics={extras.diagnostics} />
              {extras.diagnostics.problems.length > 0 && (
                <div className="mt-4">
                  <Heading>Problems</Heading>
                  <Problems problems={extras.diagnostics.problems} />
                </div>
              )}
            </div>
            {canConsent && (
              <div>
                <Heading>Consent</Heading>
                <ConsentPanel agent={a} diagnostics={extras.diagnostics} consent={extras.consent} />
              </div>
            )}
          </div>
        </div>
      )}
      {a.status === 'approved' && !extras.diagnostics && canConsent && (
        <div className="md:col-span-2 xl:col-span-3">
          <Heading>Consent</Heading>
          <ConsentPanel agent={a} diagnostics={undefined} consent={extras.consent} />
        </div>
      )}
      <div>
        <Heading>Connection</Heading>
        <Facts
          rows={[
            ['Connecting from', ip ? `${ip}${ipScopeLabel(ipScope(ip)) ? ` (${ipScopeLabel(ipScope(ip)).toLowerCase()})` : ''}` : undefined],
            ['Located', a.connectingGeo ? [a.connectingGeo.city, a.connectingGeo.country].filter(Boolean).join(', ') : undefined],
            ['Connected since', l?.connectedSince ? when(l.connectedSince) : a.connected === false ? 'not connected' : undefined],
            ['Connections since server start', l ? String(l.connects) : undefined],
            ['Last heartbeat', a.lastHeartbeat ? `${when(a.lastHeartbeat)} (${ageOf(a.lastHeartbeat)})` : undefined],
            ['Clock', a.clockSkewMs === undefined ? undefined : skewWarning(a.clockSkewMs) ? skewLabel(a.clockSkewMs) : 'within 2 minutes of the server'],
            ['Certificate', a.certExpiresAt ? (certLeft !== undefined && certLeft < 0 ? `expired ${when(a.certExpiresAt)}` : `expires ${when(a.certExpiresAt)}${certLeft !== undefined ? ` (in ${inWords(certLeft)})` : ''}`) : undefined],
          ]}
        />
      </div>
      <div>
        <Heading>What it sent</Heading>
        {l ? (
          <Facts
            rows={[
              ['Total', bytesTotal(l.bytes)],
              ['Cluster reports', `${num(l.syncs)}${l.lastSync ? ` · last ${ageOf(l.lastSync)}` : ''}`],
              ['Traffic reports', `${num(l.flows)}${l.lastFlows ? ` · last ${ageOf(l.lastFlows)}` : ''}`],
              ['Path measurements', num(l.measurements)],
              ['Heartbeats', num(l.heartbeats)],
            ]}
          />
        ) : (
          <p className="text-nb-500">Nothing counted yet. Counters start when the server does.</p>
        )}
        <p className="mt-2 text-xs text-nb-600">Sizes are of the messages as encoded, before gRPC framing and TLS.</p>
      </div>
      <div>
        <Heading>Access and health</Heading>
        <Facts
          rows={[
            ['Approved access', tierOf(a)],
            ['Installed allows', a.installedTier !== undefined ? ACCESS_TIERS.find((t) => t.value === a.installedTier)?.label : undefined],
            ['Enrollment ceiling', a.tierCap !== undefined ? ACCESS_TIERS.find((t) => t.value === a.tierCap)?.label : undefined],
            ['Namespace', a.status === 'approved' || a.status === 'revoked' || a.status === 'rejected' ? (a.namespace ?? 'not reported by this agent yet') : undefined],
            ['Helm release', a.status === 'approved' || a.status === 'revoked' || a.status === 'rejected' ? (a.releaseName ?? 'not reported by this agent yet') : undefined],
            ['Fingerprint', a.fingerprint ? a.fingerprint.slice(0, 12) + '…' : undefined],
            ['Requested', a.requestedAt ? when(a.requestedAt) : undefined],
          ]}
        />
      </div>
      {(a.status === 'revoked' || a.status === 'rejected') && a.teardown && (
        <div className="md:col-span-2 xl:col-span-3">
          <Heading>Clean up in the cluster</Heading>
          <p className="max-w-2xl text-xs leading-relaxed text-nb-500">
            {a.status === 'revoked' ? 'Revoking closed the connection and stopped this agent from being trusted, but it did not remove anything from the cluster.' : 'Rejecting stopped this enrollment, but nothing already installed in the cluster was removed.'}
            {' '}The ServiceAccount, its RBAC and the running pods stay until you remove them there:
            {a.teardown.namespaceGuessed && <span className="text-amber-300"> this agent never reported enough about its install to be sure — the commands below assume namespace <code>continuum-system</code> and release <code>continuum-agent</code>, so check both are right before running them.</span>}
          </p>
          <div className="mt-2 space-y-2">
            <CopyableCommand text={a.teardown.helm} />
            <p className="text-xs text-nb-600">The identity Secret survives an uninstall on purpose (so a mistaken uninstall can&apos;t orphan re-enrollment). Delete it too only if you want a clean slate before installing fresh here:</p>
            <CopyableCommand text={a.teardown.secret} />
          </div>
        </div>
      )}
      <div className="md:col-span-2 xl:col-span-3">
        <Heading>Discovery modules</Heading>
        {a.modules.length === 0 ? (
          <p className="text-nb-500">None reported.</p>
        ) : (
          <ul className="grid gap-x-8 gap-y-1.5 md:grid-cols-2 xl:grid-cols-3">
            {a.modules.map((m) => (
              <li key={m.name} className="text-xs">
                <span className={clsx('font-medium', MODULE_STYLE[m.status])}>{m.name}{m.status !== 'ok' && ` (${m.status})`}</span>
                {m.reason && <div className="text-nb-500">{m.reason}</div>}
              </li>
            ))}
          </ul>
        )}
        <ScopeLine agent={a} />
        <ObserverLine agent={a} />
        <CheckLine agent={a} />
      </div>
      {onRevoke && (
        <div className="md:col-span-2 xl:col-span-3">
          <Button size="sm" variant="danger" onClick={onRevoke} aria-label={`Revoke ${a.name}`}>Revoke this agent</Button>
        </div>
      )}
    </div>
  )
}

const Heading = ({ children }: { children: string }) => <div className="mb-1.5 text-xs font-medium uppercase tracking-wide text-nb-500">{children}</div>

function Facts({ rows }: { rows: [string, string | undefined][] }) {
  return (
    <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1">
      {rows.filter((r): r is [string, string] => !!r[1]).map(([k, v]) => (
        <Fragment key={k}>
          <dt className="text-nb-500">{k}</dt>
          <dd className="min-w-0 break-words text-nb-300">{v}</dd>
        </Fragment>
      ))}
    </dl>
  )
}

/* ---------- the map: this server in the middle of the picture, agents by tier ---------- */

const ROWS: { tier: Tier; label: string }[] = [
  { tier: 'cloud', label: 'Cloud' },
  { tier: 'edge', label: 'Edge' },
  { tier: 'far-edge', label: 'Far edge' },
]
const CARD_W = 236
const CARD_H = 62
const GAP = 22

function AgentMap({ rows, clusterOf, selected, onSelect }: { rows: { a: Agent; h: Health }[]; clusterOf: (id?: string) => Cluster | undefined; selected: string | null; onSelect: (id: string | null) => void }) {
  const W = 1000
  const perLine = 4
  // Group by the tier of the agent's cluster; an agent with no cluster yet goes in a row of its own.
  const groups = [...ROWS.map((r) => ({ ...r, items: rows.filter((x) => clusterOf(x.a.clusterId)?.tier === r.tier) })), { tier: 'none' as const, label: 'Not in a cluster yet', items: rows.filter((x) => !clusterOf(x.a.clusterId)) }].filter((g) => g.items.length > 0)
  let y = 96
  const placed: { x: number; y: number; a: Agent; h: Health }[] = []
  const labels: { y: number; text: string }[] = []
  for (const g of groups) {
    labels.push({ y, text: g.label })
    y += 18
    for (let i = 0; i < g.items.length; i += perLine) {
      const line = g.items.slice(i, i + perLine)
      const total = line.length * CARD_W + (line.length - 1) * GAP
      line.forEach((it, k) => placed.push({ x: (W - total) / 2 + k * (CARD_W + GAP), y, ...it }))
      y += CARD_H + GAP
    }
    y += 14
  }
  const H = Math.max(y, 180)
  const hub = { x: W / 2, y: 34 }
  return (
    <div className="overflow-x-auto rounded-xl border border-nb-850 bg-nb-925 p-2" data-testid="agents-map">
      <svg viewBox={`0 0 ${W} ${H}`} className="mx-auto w-full min-w-[640px] max-w-[1000px]" role="img" aria-label="Agents connected to this server, grouped by the tier of their cluster">
        {placed.map((p) => {
          const st = HEALTH[p.h]
          const live = p.h === 'ok' || p.h === 'late'
          const d = `M${hub.x},${hub.y + 20} C${hub.x},${(hub.y + p.y) / 2 + 20} ${p.x + CARD_W / 2},${(hub.y + p.y) / 2} ${p.x + CARD_W / 2},${p.y}`
          return (
            <path
              key={p.a.id}
              d={d}
              fill="none"
              strokeWidth={live ? 1.6 : 1.2}
              strokeLinecap="round"
              className={live ? 'map-link' : undefined}
              data-reverse="1"
              style={{ stroke: p.h === 'ok' ? '#5fd3a0' : p.h === 'late' ? '#fbbf24' : '#6f7b85', ['--flow' as string]: '2.6s', strokeDasharray: live ? undefined : '2 5', opacity: live ? 0.85 : 0.5 }}
              data-status={st.label}
            />
          )
        })}
        <g transform={`translate(${hub.x - 70} ${hub.y - 20})`}>
          <rect width={140} height={40} rx={10} style={{ fill: 'var(--color-nb-920)', stroke: 'var(--color-accent)' }} strokeWidth={1.4} />
          <foreignObject x={0} y={0} width={140} height={40}>
            <div className="flex h-full items-center justify-center gap-2 text-sm font-medium text-white"><Server size={15} className="text-accent" aria-hidden /> This server</div>
          </foreignObject>
        </g>
        {labels.map((l) => (
          <text key={l.text} x={22} y={l.y + 4} fontSize={11} style={{ fill: 'var(--color-nb-500)' }} className="uppercase tracking-wide">{l.text}</text>
        ))}
        {placed.map((p) => {
          const c = clusterOf(p.a.clusterId)
          const st = HEALTH[p.h]
          const sel = selected === p.a.id
          return (
            <foreignObject key={p.a.id} x={p.x} y={p.y} width={CARD_W} height={CARD_H}>
              <button
                onClick={() => onSelect(sel ? null : p.a.id)}
                aria-pressed={sel}
                className={clsx('flex h-full w-full flex-col justify-center rounded-lg border bg-nb-920 px-3 text-left transition-colors hover:border-nb-700', sel ? 'border-accent' : 'border-nb-850')}
                data-testid="agent-node"
              >
                <span className="flex items-center gap-2 text-sm font-medium text-white">
                  <span className={clsx('size-2 shrink-0 rounded-full', st.dot)} aria-hidden />
                  <span className="truncate">{p.a.name}</span>
                </span>
                <span className="mt-0.5 truncate text-xs text-nb-500">{c ? c.name : 'no cluster yet'} · {p.a.lastHeartbeat ? shortAge(p.a.lastHeartbeat) : st.label.toLowerCase()}</span>
                <span className="truncate text-[11px] text-nb-600">{p.a.link ? `${bytesTotal(p.a.link.bytes)} received` : tierOf(p.a)}</span>
              </button>
            </foreignObject>
          )
        })}
      </svg>
    </div>
  )
}
