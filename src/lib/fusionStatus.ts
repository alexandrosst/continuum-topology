import type { FusionComponent, FusionRetention, FusionStatus } from '@/lib/api'
import { fullness, neededBytes, retentionVerdict, volumeGiB } from '@/lib/fusionRetention'
import { ago } from '@/lib/observed'

/** The id of the server-owned regional operator in front of FUSION (backend CentralOperatorID). */
export const CENTRAL_OPERATOR_ID = 'op-central'

/** 'checking' = the first answer has not arrived; 'unavailable' = this server cannot switch FUSION at all. */
export type FusionKind = 'checking' | 'unavailable' | 'off' | 'starting' | 'running' | 'attention'

/** The one word for each state, used by every place that names FUSION's state (the panel, the central row, the picker), so
 *  they cannot drift apart. */
const LABEL: Record<FusionKind, string> = {
  checking: 'Checking',
  unavailable: 'Unavailable',
  starting: 'Starting',
  running: 'Running',
  off: 'Off',
  attention: 'Needs attention',
}
export const fusionLabel = (kind: FusionKind): string => LABEL[kind]

/** "12 s ago" where `ago` would only say "just now": the point of showing when data last arrived is to see it moving. */
function dataAge(iso: string, now: number): string {
  const s = Math.max(0, Math.round((now - Date.parse(iso)) / 1000))
  return s < 60 ? `${s} s ago` : ago(iso, now)
}

/** FUSION's state as one sentence, the way an operator's health is one: the words carry the meaning, the dot repeats it. */
export function fusionSentence(s: FusionStatus | null, now = Date.now()): { kind: FusionKind; text: string } {
  if (!s) return { kind: 'checking', text: 'Checking FUSION…' }
  if (!s.available) return { kind: 'unavailable', text: s.message ?? 'FUSION cannot be switched from this server.' }
  // Grafana is a convenience on top of the stores: it can come up late, or be switched off in the release, without FUSION being "starting".
  const parts = (s.components ?? []).filter((c) => c.component !== 'grafana')
  const up = parts.filter((c) => c.desired > 0 && c.ready >= c.desired).length
  const wanted = parts.filter((c) => c.desired > 0).length
  switch (s.state) {
    case 'running':
      return { kind: 'running', text: `Running - ${s.lastDataAt ? `last data ${dataAge(s.lastDataAt, now)}` : 'waiting for first data'}` }
    case 'starting':
      return { kind: 'starting', text: `Starting - ${up} of ${wanted} parts are up.` }
    case 'attention':
      return { kind: 'attention', text: s.message ?? 'Needs attention.' }
    default:
      return { kind: 'off', text: 'Off - nothing is running. Anything already saved stays on its volumes.' }
  }
}

/** Whether a regional operator that sends to the central operator has somewhere to send right now. */
export function fusionUsable(s: FusionStatus | null): boolean {
  return !!s && s.available && (s.state === 'running' || s.state === 'starting')
}

/** "Last data 12 s ago", or "No data yet": what the status line says about whether anything is arriving. */
export function lastDataText(s: FusionStatus, now = Date.now()): string {
  return s.lastDataAt ? `Last data ${dataAge(s.lastDataAt, now)}` : 'No data yet'
}

/** The four words every status is one of (glyph, word and colour), plus the two states that are not a verdict: coming up, and switched off. */
export type Health = 'healthy' | 'attention' | 'broken' | 'unknown'
export type Shown = Health | 'starting' | 'off'
export const HEALTH_WORD: Record<Shown, string> = { healthy: 'Healthy', attention: 'Needs attention', broken: 'Not working', unknown: 'Unknown', starting: 'Starting', off: 'Off' }

/** The one thing to do about a problem: go somewhere, copy a command, or look again. */
export type ProblemAction = { kind: 'link'; label: string; to: string } | { kind: 'copy'; label: string; text: string } | { kind: 'refresh'; label: string }

export interface FusionProblem {
  id: string
  /** The part it is about, so its row can carry the same verdict. */
  part?: FusionComponent['component']
  health: 'attention' | 'broken'
  /** What is wrong, in one sentence; `detail` says what it means and what to do. */
  title: string
  detail: string
  action: ProblemAction
}

/** No data for this long while FUSION runs is a problem: collectors batch for seconds, not minutes. */
export const NO_DATA_AFTER_MS = 5 * 60_000
/** A FUSION that has run this long without ever receiving data has nothing sending to it. */
const NO_DATA_YET_AFTER_MS = 10 * 60_000

const CONSEQUENCE: Record<FusionComponent['component'], string> = {
  metrics: 'Metrics are not being stored.',
  logs: 'Logs are not being stored.',
  traces: 'Traces are not being stored.',
  central: 'Nothing can reach FUSION from other clusters.',
  grafana: 'Dashboards are not available.',
}
/** What to try for the reasons the server gives for a part that will not start (see explainPod in the Go server). */
const TRY: Record<string, string> = {
  'Cannot download the image': 'Check that the cluster can reach the image registry.',
  'Waiting for a volume': 'Check that the cluster can create volumes (its storage class).',
  'No node has room': 'Free room on a node, or add one.',
  'No suitable node': 'Check the node selectors and taints.',
  'Ran out of memory and keeps restarting': 'Give it more memory.',
}

/** What is wrong with FUSION, worst first, from what the server reports: a part that is down, a volume that is filling, nothing arriving.
 *  Each says what is wrong in a sentence and offers one thing to do; an empty list is a healthy FUSION. */
export function fusionProblems(s: FusionStatus | null, retention: FusionRetention | null, now = Date.now()): FusionProblem[] {
  if (!s?.available || s.state === 'off') return []
  const out: FusionProblem[] = []
  if (s.state === 'attention') {
    const down = (s.components ?? []).filter((c) => c.desired > 0 && c.ready < c.desired)
    const ns = s.central?.namespace
    for (const c of down) {
      const reason = c.reason ?? ''
      out.push({
        id: `down-${c.component}`, part: c.component, health: 'broken', title: `${c.label} is not running`,
        detail: [reason && `${reason}.`, CONSEQUENCE[c.component], TRY[reason]].filter(Boolean).join(' '),
        action: ns ? { kind: 'copy', label: 'Copy the kubectl command', text: `kubectl -n ${ns} get pods` } : { kind: 'refresh', label: 'Check again' },
      })
    }
    if (down.length === 0) out.push({ id: 'status', health: 'attention', title: s.message ?? 'FUSION needs attention.', detail: 'The server could not tell which part is at fault.', action: { kind: 'refresh', label: 'Check again' } })
  }
  const change = { kind: 'link', label: 'Change retention', to: '/fusion/settings' } as const
  for (const st of retention?.available ? retention.stores : []) {
    const f = fullness(st)
    const need = neededBytes(st, st.days)
    if (f) {
      out.push({
        id: `disk-${st.component}`, part: st.component, health: 'attention', title: `${st.label}'s volume is ${f.pct}% full`,
        detail: `${f.level === 'critical' ? 'It may stop accepting data. ' : ''}${st.canGrow === false ? 'This volume cannot be grown, so keep fewer days.' : 'Keep fewer days, or grow the volume.'}`, action: change,
      })
    } else if (st.volumeKnown && need !== null && need > st.volumeBytes) {
      out.push({ id: `fit-${st.component}`, part: st.component, health: 'attention', title: `${st.label} keeps more than its volume will hold`, detail: retentionVerdict(st, st.days, volumeGiB(st)).text, action: change })
    }
  }
  if (s.central?.warnings?.length) {
    out.push({ id: 'central-address', part: 'central', health: 'attention', title: "The central operator's address may not work", detail: s.central.warnings.join(' '), action: { kind: 'link', label: 'Open Pipeline', to: '/pipeline' } })
  }
  if (s.state === 'running') {
    const last = s.lastDataAt ? Date.parse(s.lastDataAt) : NaN
    const up = s.since ? Date.parse(s.since) : NaN
    const open = { kind: 'link', label: 'Open Pipeline', to: '/pipeline' } as const
    if (now - last > NO_DATA_AFTER_MS) out.push({ id: 'no-data', health: 'attention', title: `No new data: the last data arrived ${ago(s.lastDataAt, now)}`, detail: 'Collectors keep what they cannot deliver for a while, then drop it.', action: open })
    else if (!s.lastDataAt && now - up > NO_DATA_YET_AFTER_MS) out.push({ id: 'no-data', health: 'attention', title: 'No data has arrived yet', detail: 'FUSION is running but nothing is sending to it.', action: open })
  }
  return out.sort((a, b) => Number(b.health === 'broken') - Number(a.health === 'broken'))
}

/** FUSION as a whole, in one of the shown states. */
export function fusionHealth(s: FusionStatus | null, problems: FusionProblem[]): Shown {
  if (!s?.available) return 'unknown'
  if (s.state === 'off' || s.state === 'starting') return s.state
  if (problems.some((p) => p.health === 'broken')) return 'broken'
  return problems.length > 0 ? 'attention' : 'healthy'
}

/** One part's state: its own, with whatever is wrong with it counted. `overall` is FUSION's own verdict: a part that is not ready while
 *  FUSION as a whole needs attention has stopped coming up, and "Starting" would keep promising what is not happening. */
export function partHealth(c: FusionComponent, overall: FusionKind, problems: FusionProblem[]): Shown {
  if (c.desired === 0) return 'off'
  if (c.ready < c.desired) return overall === 'attention' ? 'broken' : 'starting'
  const own = problems.filter((p) => p.part === c.component)
  return own.some((p) => p.health === 'broken') ? 'broken' : own.length > 0 ? 'attention' : 'healthy'
}
