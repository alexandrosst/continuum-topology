import type { StatusTone } from '@/components/ui/Status'
import { COLLECTORS, collectorState, effectiveNote, sortProblems, tierName, uptimeWords, type AgentDiagnostics, type AgentExtras } from '@/lib/consent'
import { skewLabel, skewWarning } from '@/lib/clock'
import { agentLiveness, reportIsStale } from '@/lib/status'
import type { Agent } from '@/lib/types'

/**
 * Diagnose: what is wrong with this agent, in the order to deal with it, from what the server already knows. Pure: the same agent and
 * the same clock give the same answer, so the drawer, the copied summary and the tests all read one thing.
 *
 * Two lists come out. `findings` is the to-do list: only what is not fine, worst first, each with what it means and what to do. `checks`
 * is the whole table, fine or not, grouped, so a person can see that something was looked at and found well, as well as what was not.
 */
export type Tone = Extract<StatusTone, 'healthy' | 'attention' | 'down' | 'unknown' | 'notice' | 'setup' | 'off'>

export interface Finding {
  id: string
  tone: Extract<Tone, 'down' | 'attention' | 'setup' | 'unknown' | 'notice'>
  title: string
  detail: string
  /** What to do about it, in a sentence. */
  fix?: string
  /** A command that does it, to copy: a person runs it, nothing here does. */
  command?: string
}

export interface Check {
  group: 'Connection' | 'Server and agent agree' | 'Certificate and clock' | 'What it can see' | 'What it reports'
  id: string
  label: string
  tone: Tone
  /** The value, short. */
  value: string
  /** The sentence behind it. */
  note?: string
}

export interface Diagnosis {
  verdict: Extract<Tone, 'healthy' | 'attention' | 'down' | 'unknown' | 'setup'>
  headline: string
  findings: Finding[]
  checks: Check[]
}

const RANK: Record<Finding['tone'], number> = { down: 0, attention: 1, setup: 2, unknown: 3, notice: 4 }

const msUntil = (iso: string | undefined, now: number): number | undefined => (iso ? Date.parse(iso) - now : undefined)

const inWords = (ms: number): string => {
  const a = Math.abs(ms)
  return a < 3_600_000 ? `${Math.max(1, Math.round(a / 60_000))} min` : a < 48 * 3_600_000 ? `${Math.round(a / 3_600_000)} h` : `${Math.round(a / 86_400_000)} d`
}

/** The kubectl line that shows an agent's own log. The release is the one the agent said it was installed as, or the chart's default. */
export const logCommand = (a: Pick<Agent, 'namespace' | 'releaseName'>): string => `kubectl -n ${a.namespace ?? 'ikhnos-system'} logs deploy/${a.releaseName ?? 'ikhnos-agent'} --tail=200`
const podsCommand = (a: Pick<Agent, 'namespace'>): string => `kubectl -n ${a.namespace ?? 'ikhnos-system'} get pods -o wide`

export function diagnoseAgent(a: Agent, extras: AgentExtras, now: number): Diagnosis {
  const findings: Finding[] = []
  const checks: Check[] = []
  const d: AgentDiagnostics | undefined = extras.diagnostics
  const add = (f: Finding) => findings.push(f)

  // --- not an approved agent: the whole question is the approval ---
  if (a.status !== 'approved') {
    const word = { pending: 'waiting for approval', revoked: 'revoked', rejected: 'rejected', expired: 'expired (nobody approved it in time)' }[a.status]
    const tone: Finding['tone'] = a.status === 'pending' ? 'attention' : a.status === 'expired' ? 'unknown' : 'down'
    const f: Finding = {
      id: 'approval',
      tone,
      title: `This agent is ${word}`,
      detail: a.reason ?? (a.status === 'pending' ? 'It has enrolled and asked to connect. Nothing is read from its cluster until it is approved.' : 'It cannot send anything with its present identity.'),
      fix: a.status === 'pending' ? 'Approve it on this page with the code its install printed.' : a.status === 'expired' ? 'The agent asks again by itself with a new code; approve the new request.' : 'To connect the cluster again, enroll it with a new token.',
    }
    add(f)
    checks.push({ group: 'Connection', id: 'approval', label: 'Approval', tone, value: a.status, note: f.detail })
    return { verdict: tone === 'unknown' ? 'unknown' : tone === 'attention' ? 'attention' : 'down', headline: f.title, findings, checks }
  }

  // --- connection ---
  const live = agentLiveness(a, now)
  const silent = live.silentMs === undefined ? undefined : Math.round(live.silentMs / 1000)
  const connCond = extras.conditions?.find((c) => c.type === 'Connected')
  if (live.state === 'down') {
    add({ id: 'stream', tone: 'down', title: 'The agent is not connected', detail: `${connCond?.message ?? 'No stream from the agent is open'}${a.lastHeartbeat ? `; its last heartbeat was ${uptimeWords(Math.max(0, (now - Date.parse(a.lastHeartbeat)) / 1000))} ago` : ''}.`, fix: 'Check that its pod is running and that it can reach this server (network policy, proxy, the server address). Its own log says why it cannot connect.', command: `${podsCommand(a)}\n${logCommand(a)}` })
  } else if (live.state === 'late') {
    add({ id: 'heartbeat', tone: 'attention', title: 'The agent’s heartbeat is late', detail: `Nothing has been heard for ${uptimeWords(silent ?? 0)}; the agent beats every 30 s and the server calls it late after ${uptimeWords(a.lateAfterSeconds ?? 75)}.`, fix: 'Usually a stalled connection or a pod that is being rescheduled; if it does not recover in a minute or two, look at the agent’s log.', command: logCommand(a) })
  }
  checks.push({
    group: 'Connection', id: 'stream', label: 'Stream',
    tone: live.state === 'live' ? 'healthy' : live.state === 'late' ? 'attention' : live.state === 'down' ? 'down' : 'unknown',
    value: live.state === 'live' ? 'open' : live.state === 'late' ? 'late' : live.state === 'down' ? 'closed' : 'not judged',
    note: live.state === 'unknown' ? 'A sample or hand-made agent has no live link to judge.' : a.link?.connectedSince ? `Connected since ${new Date(a.link.connectedSince).toLocaleString()}.` : undefined,
  })
  if (silent !== undefined) checks.push({ group: 'Connection', id: 'heartbeat', label: 'Last heartbeat', tone: live.state === 'late' ? 'attention' : live.state === 'down' ? 'unknown' : 'healthy', value: `${uptimeWords(silent)} ago` })
  if (a.link) {
    // Many connections since the server started, with the stream short-lived, is a link that keeps dropping.
    const young = a.link.connectedSince ? now - Date.parse(a.link.connectedSince) < 10 * 60_000 : false
    const flapping = a.link.connects >= 5 && young
    if (flapping) add({ id: 'flapping', tone: 'attention', title: 'The connection keeps dropping', detail: `${a.link.connects} connections since the server started, and this one is only ${uptimeWords((now - Date.parse(a.link.connectedSince!)) / 1000)} old.`, fix: 'Look for a load balancer or proxy that closes idle streams (set its timeout above a minute), and for the agent restarting; its log shows both.', command: logCommand(a) })
    checks.push({ group: 'Connection', id: 'connects', label: 'Connections since the server started', tone: flapping ? 'attention' : 'healthy', value: String(a.link.connects) })
  }

  // --- server and agent agree ---
  for (const c of extras.conditions ?? []) {
    if (c.type === 'Connected') continue
    const tone: Tone = c.status === 'True' ? 'healthy' : c.status === 'False' ? 'attention' : 'unknown'
    checks.push({ group: 'Server and agent agree', id: `cond-${c.type}`, label: c.type === 'ConfigInForce' ? 'The configuration it was sent is the one in force' : c.type === 'Compatible' ? 'Protocol version' : c.type, tone, value: c.status === 'True' ? 'yes' : c.status === 'False' ? 'no' : 'not known', note: c.message })
    if (c.status === 'False') {
      add(c.type === 'ConfigInForce'
        ? { id: 'config', tone: 'attention', title: 'The agent has not confirmed the latest configuration', detail: c.message, fix: 'It usually settles within a heartbeat or two. If it stays, the agent may be stuck: its log says whether it refused the configuration.', command: logCommand(a) }
        : { id: 'protocol', tone: 'attention', title: 'The agent and the server do not speak the same protocol version', detail: c.message, fix: c.reason === 'ProtocolTooOld' ? 'Upgrade the agent with the command on its details.' : 'Upgrade this server, or run an older agent.' })
    }
  }
  if (a.consistency) {
    const bad = a.consistency.differences > 0
    checks.push({ group: 'Server and agent agree', id: 'consistency', label: 'The server’s picture matches the cluster’s own', tone: bad ? 'attention' : 'healthy', value: bad ? `${a.consistency.differences} differed` : 'matched', note: `Last checked ${new Date(a.consistency.lastCheck).toLocaleString()}; ${a.consistency.checks} checks so far.${a.consistency.summary ? ` ${a.consistency.summary}` : ''} A difference is corrected as it is found.` })
    if (bad) add({ id: 'consistency', tone: 'notice', title: 'The server’s picture differed from the cluster’s at the last check', detail: `${a.consistency.differences} difference${a.consistency.differences === 1 ? '' : 's'}, corrected. ${a.consistency.summary ?? ''}`.trim(), fix: 'Nothing to do unless it keeps coming back; then a watch of the agent is missing events.' })
  }

  // --- certificate and clock ---
  const left = msUntil(a.certExpiresAt, now)
  if (left !== undefined) {
    const expired = left < 0
    const soon = !expired && left < 2 * 3_600_000 && live.state === 'live'
    checks.push({ group: 'Certificate and clock', id: 'cert', label: 'Certificate', tone: expired ? 'down' : soon ? 'attention' : 'healthy', value: expired ? `expired ${inWords(left)} ago` : `valid for ${inWords(left)}`, note: 'Agents renew their short-lived certificates on their own.' })
    if (expired) add({ id: 'cert', tone: 'down', title: 'The agent’s certificate has expired', detail: `It expired ${inWords(left)} ago. An agent that is up renews it a long time before this, so it has not been able to renew.`, fix: 'Check that the agent can write its identity Secret (the agent reports identity_secret_unwritable when it cannot), and that its clock is right.', command: logCommand(a) })
    else if (soon) add({ id: 'cert-soon', tone: 'attention', title: 'The agent’s certificate is about to expire and has not been renewed', detail: `${inWords(left)} left.`, fix: 'The agent renews well before this. If it does not within a few minutes, its log says why.', command: logCommand(a) })
  }
  if (a.clockSkewMs !== undefined) {
    const warn = skewWarning(a.clockSkewMs)
    checks.push({ group: 'Certificate and clock', id: 'clock', label: 'Clock against the server’s', tone: warn ? 'attention' : 'healthy', value: skewLabel(a.clockSkewMs), note: warn })
    if (warn) add({ id: 'clock', tone: 'attention', title: 'The cluster’s clock differs from the server’s', detail: warn, fix: 'Fix time synchronisation on the nodes the agent runs on.' })
  }

  // --- what it can see ---
  if (d) {
    const note = effectiveNote(d)
    checks.push({ group: 'What it can see', id: 'tier', label: 'Access', tone: note ? 'attention' : 'healthy', value: `${tierName(d.effectiveTier)} (approved ${tierName(d.approvedTier).toLowerCase()}, installed up to ${tierName(d.installedTier).toLowerCase()})`, note })
    if (note) add({ id: 'tier', tone: 'notice', title: 'The agent collects less than was approved', detail: note, fix: d.effectiveTier < d.installedTier ? 'It applies the approved access on its next connection.' : 'Only the cluster’s owner can widen the install, with the upgrade command on the agent’s details.' })
    const failing = d.informers.filter((i) => !i.synced || i.lastError)
    checks.push({ group: 'What it can see', id: 'watches', label: 'Watches read', tone: failing.length ? 'attention' : d.informers.length === 0 ? 'unknown' : 'healthy', value: d.informers.length === 0 ? 'none yet' : `${d.informers.length - failing.length} of ${d.informers.length}`, note: failing.map((i) => `${i.name}: ${i.lastError || 'not read yet'}`).join('; ') || undefined })
    for (const c of COLLECTORS) {
      const s = collectorState(d.collectors.find((x) => x.name === c.id))
      if (s.tone === 'off') continue
      checks.push({ group: 'What it can see', id: `collector-${c.id}`, label: c.label, tone: s.tone === 'warn' ? 'attention' : s.tone === 'paused' ? 'setup' : 'healthy', value: s.label })
    }
    if (d.scope && d.scope.inScope === 0) add({ id: 'scope', tone: 'attention', title: 'The agent’s scope matches no namespace', detail: d.scope.description, fix: 'Change scope.namespaces or scope.exclude on the install, or clear the exclusions set here.' })

    // --- what it reports ---
    const age = (now - Date.parse(d.reportedAt)) / 1000
    const stale = reportIsStale(age)
    checks.push({ group: 'What it reports', id: 'report', label: 'Its last account of itself', tone: stale ? 'unknown' : 'healthy', value: Number.isFinite(age) ? `${uptimeWords(Math.max(0, age))} ago` : 'unknown', note: stale ? 'Older than three reports: what it said then may no longer be true.' : undefined })
    if (stale && live.state === 'live') add({ id: 'report-stale', tone: 'attention', title: 'The agent is connected but has stopped reporting on itself', detail: 'Its last account of itself is more than 15 minutes old although the stream is open.', fix: 'Restart the agent’s pod if it does not report again soon.', command: logCommand(a) })
    for (const p of sortProblems(d.problems)) {
      const tone: Tone = p.severity === 'error' ? 'down' : p.severity === 'warn' ? 'attention' : 'notice'
      checks.push({ group: 'What it reports', id: `problem-${p.code}`, label: p.code, tone, value: p.severity === 'error' ? 'error' : p.severity === 'warn' ? 'warning' : 'notice', note: p.message })
      add({ id: `problem-${p.code}`, tone: tone as Finding['tone'], title: p.code.replaceAll('_', ' '), detail: p.message })
    }
    if (d.exportHealth) {
      const failingExports = (d.exportHealth.routes ?? []).filter((r) => r.state === 'failing')
      checks.push({ group: 'What it reports', id: 'export', label: 'Telemetry export', tone: failingExports.length ? 'attention' : 'healthy', value: failingExports.length ? `${failingExports.length} failing` : 'no failures', note: failingExports.map((r) => `${r.signal} via ${r.exporter}: ${r.failed} sends failed${r.lastFailedAt ? `, the last at ${new Date(r.lastFailedAt).toLocaleString()}` : ''}`).join('; ') || undefined })
      for (const r of failingExports) add({ id: `export-${r.exporter}-${r.signal}`, tone: 'attention', title: `Telemetry export is failing (${r.signal} via ${r.exporter})`, detail: 'Sends to the destination are failing and none has succeeded since.', fix: 'Check the destination’s address and credentials under Pipeline.' })
    }
  } else {
    checks.push({ group: 'What it reports', id: 'report', label: 'Its account of itself', tone: 'unknown', value: 'none yet', note: 'The agent has not sent a self-report (or is an older agent, or you may not see it).' })
  }

  findings.sort((x, y) => RANK[x.tone] - RANK[y.tone])
  const worst = findings[0]?.tone
  const verdict: Diagnosis['verdict'] = !worst || worst === 'notice' ? (live.state === 'unknown' || !d ? 'unknown' : 'healthy') : worst === 'down' ? 'down' : worst === 'attention' ? 'attention' : worst === 'setup' ? 'setup' : 'unknown'
  const serious = findings.filter((f) => f.tone === 'down' || f.tone === 'attention').length
  const headline = verdict === 'healthy' ? 'Nothing is wrong that this server can see.' : verdict === 'unknown' ? 'Not enough is known to say it is well.' : serious === 1 ? findings[0].title : `${serious} things need attention, the first being: ${findings[0].title.charAt(0).toLowerCase()}${findings[0].title.slice(1)}`
  return { verdict, headline, findings, checks }
}

/** The diagnosis as plain text, for pasting into a ticket: no addresses, no secrets, nothing the person could not read on the screen. */
export function diagnosisText(a: Agent, extras: AgentExtras, dx: Diagnosis, now: number): string {
  const lines = [
    `Agent ${a.name} (${a.id}), version ${a.version}${a.kubernetesVersion ? `, Kubernetes ${a.kubernetesVersion}` : ''}${extras.protocolVersion !== undefined ? `, protocol ${extras.protocolVersion}` : ''}`,
    `Diagnosed ${new Date(now).toISOString()}: ${dx.headline}`,
    '',
  ]
  if (dx.findings.length) {
    lines.push('To do:')
    dx.findings.forEach((f, i) => lines.push(`${i + 1}. [${f.tone}] ${f.title}. ${f.detail}${f.fix ? ` ${f.fix}` : ''}`))
    lines.push('')
  }
  lines.push('Checks:')
  for (const c of dx.checks) lines.push(`- ${c.label}: ${c.value} (${c.tone})${c.note ? ` - ${c.note}` : ''}`)
  return lines.join('\n')
}
