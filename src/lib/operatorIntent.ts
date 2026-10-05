import { api, ApiError, type Conn, type TelemetryIntentCommand } from './api'
import { operatorReceiverEndpoint } from './destinationCatalog'
import { TELEMETRY_SIGNALS, type ScopeOverrideInput, type TelemetryInput } from './install'
import { telemetrySecretCommand, telemetryUpgradeCommand, type InstallInfo } from './consent'
import type { OperatorDestination, ReceiverAuth, SignalGrant, TelemetryIntent } from './types'

/*
 * The client half of "point this agent at a regional operator". The operator's receiver is mutual TLS, so
 * the endpoint the wizard writes into the draft is not enough: a client certificate has to be issued, and
 * the server also composes the provenance flags (`telemetry.resource.*`) that tie the export to a telemetry
 * intent. That all lives behind POST .../telemetry-intents/{id}/command (admin only, audited, reissues a
 * certificate on every call), which needs an active intent for the agent first. This file turns the draft
 * into that intent, calls it, and assembles the one block a person pastes.
 */

/** The operator the draft sends to, or '' - only when the endpoint still IS that operator's receiver. A
 *  stale id (the endpoint was edited by a path that forgot to clear it) must never trigger a certificate. */
export function exportOperatorId(t: TelemetryInput): string {
  const id = t.exportOperatorId
  // Each signal type with its own destination is handled lane by lane, never as the one operator.
  if (!id || t.exportSplit) return ''
  return t.exportEndpoint.trim() === operatorReceiverEndpoint({ id }) ? id : ''
}

/** The signals the draft turns on, as the grants a TelemetryIntent records. `source` follows
 *  store.SignalGrant's vocabulary: 'bundle-kepler' / 'bundle-dcgm' for a signal whose exporter the chart
 *  deploys itself, 'existing' for one scraping something already running (the draft's own energySource /
 *  acceleratorsSource), and 'builtin' for every signal with no source choice of its own. */
export function intentSignals(t: TelemetryInput): SignalGrant[] {
  const on = t as unknown as Record<string, boolean>
  return TELEMETRY_SIGNALS.filter((s) => on[s.id]).map((s) => ({
    id: s.id,
    source: s.id === 'energy' ? t.energySource : s.id === 'accelerators' ? t.acceleratorsSource : 'builtin',
  }))
}

/**
 * The namespaces/exclude a TelemetryIntent records for this draft. The server only stores them (they are
 * bookkeeping of what was granted - the namespaces actually collected are set by the `--set` flags in the
 * generated command), so the rule is: the recorded scope must never be narrower than what the command will
 * really collect. Only application metrics, application logs and traces have a namespace scope of their own;
 * every other signal is cluster-wide or per-node and ignores namespaces entirely. Hence:
 *  - namespaces: the union of the scoped signals' included namespaces, but EMPTY ("every namespace the
 *    agent's tier and consent allow") as soon as any signal that is on collects everything - a cluster-wide
 *    one, or a scoped one whose own list is empty;
 *  - exclude: only namespaces that EVERY signal that is on excludes (the intersection) - one left out by
 *    one signal but collected by another is not out of scope. Any cluster-wide signal makes it empty.
 * Both lists come back sorted and de-duplicated.
 */
export function intentScope(t: TelemetryInput): { namespaces: string[]; exclude: string[] } {
  const on = t as unknown as Record<string, boolean>
  const scopes: Record<string, ScopeOverrideInput | undefined> = {
    applicationMetrics: t.applicationMetricsScope,
    applicationLogs: t.applicationLogsScope,
    traces: t.tracesScope,
  }
  const enabled = TELEMETRY_SIGNALS.filter((s) => on[s.id])
  let everything = enabled.length === 0
  const included = new Set<string>()
  let excluded: Set<string> | undefined
  for (const s of enabled) {
    const sc = scopes[s.id]
    if (!sc) {
      everything = true
      excluded = new Set()
      continue
    }
    if (sc.namespaces.length === 0) everything = true
    for (const n of sc.namespaces) included.add(n)
    const prev = excluded
    excluded = prev ? new Set(sc.exclude.filter((n) => prev.has(n))) : new Set(sc.exclude)
  }
  return { namespaces: everything ? [] : [...included].sort(), exclude: [...(excluded ?? [])].sort() }
}

/** The draft the CLIENT-built half of the command is built from. The receiver is mutual TLS over OTLP/gRPC, so
 *  whatever a previous destination left behind (HTTP, skip-verify) must not reach the command; the server's
 *  fragment states the mTLS side. For an operator whose receiver is gated by the client certificate alone
 *  (`receiverAuth` 'mtls') the credential header and Secret are dropped as well: there is no receiver token to
 *  present, so a Secret name left in the draft (from before the operator was chosen, say) must not produce a
 *  Secret command or `auth.*` flags for one. Any other value keeps them - the bearer token is still expected. */
export const operatorCommandDraft = (t: TelemetryInput, receiverAuth?: ReceiverAuth): TelemetryInput => ({
  ...t,
  exportProtocol: 'grpc',
  exportInsecure: false,
  ...(receiverAuth === 'mtls' ? { exportAuthHeaderName: '', exportAuthSecretName: '', exportAuthSecretKey: '' } : {}),
})

/** The endpoint the server's fragment sets, when it sets one - compared with the draft's by the panel. */
export function fragmentEndpoint(fragment: string): string | undefined {
  return /--set(?:-string)? telemetry\.export\.otlp\.endpoint=(\S+)/.exec(fragment)?.[1]
}

/**
 * The one block to paste: the server's Secret command(s) first (a failure there stops everything after it),
 * then the credential Secret if the draft names one (never for a certificate-only operator, see
 * operatorCommandDraft), then the normal upgrade command with the server's
 * fragment appended. The fragment repeats `telemetry.export.otlp.endpoint` with the same value the client
 * writes (both are `<operator id>.continuum-system.svc:4317`); helm takes the last, so if the two ever
 * differed the server's would win - the panel says so (see fragmentEndpoint).
 */
export function operatorCommandBlock(opts: { install: InstallInfo | undefined; draft: TelemetryInput; measurementsOn?: boolean; result: TelemetryIntentCommand }): string {
  const d = operatorCommandDraft(opts.draft, opts.result.receiverAuth)
  const target = { namespace: opts.result.namespace, release: opts.result.release }
  const upgrade = `${telemetryUpgradeCommand(opts.install, d, opts.measurementsOn, target).trimEnd()} \\\n  ${opts.result.installFragment}`
  const cred = telemetrySecretCommand(d, opts.measurementsOn, target)
  return [...opts.result.secretCommands, ...(cred ? [cred] : []), upgrade].join(' && \\\n')
}

const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((x, i) => x === b[i])
const grantKeys = (gs: SignalGrant[]) => gs.map((s) => `${s.id}:${s.source}`).sort()

/**
 * Makes sure this agent's one active telemetry intent says what the draft says, creating it when there is
 * none. The server allows one active intent per agent, so an existing one is updated, never duplicated.
 * Returns the intent and whether it was created or updated.
 *
 * Updating has an ordering catch: the server checks the operator's accepted modalities against the intent's
 * CURRENT signals when the destination changes, and against the CURRENT destination when the signals change.
 * When the operator is the same one already set, only the scope is sent. Otherwise the destination goes
 * first, and if the server refuses that, the scope first - a pair the server would accept in neither order
 * (a traces-only operator swapped for a metrics-only one) surfaces the second refusal.
 */
export async function ensureOperatorIntent(conn: Conn, o: { agentId: string; operatorId: string; name: string; draft: TelemetryInput }): Promise<{ intent: TelemetryIntent; action: 'created' | 'updated' }> {
  const signals = intentSignals(o.draft)
  const { namespaces, exclude } = intentScope(o.draft)
  const destination: OperatorDestination = { kind: 'operator', endpoint: operatorReceiverEndpoint({ id: o.operatorId }), targetOperatorId: o.operatorId }
  const existing = (await api.listTelemetryIntents(conn, o.agentId)).find((i) => i.status === 'active')
  if (!existing) {
    return { intent: await api.createTelemetryIntent(conn, o.agentId, o.name.slice(0, 80), namespaces, exclude, signals, destination), action: 'created' }
  }
  const sameDestination = existing.destination.kind === 'operator' && existing.destination.targetOperatorId === o.operatorId
  const sameScope = sameList(existing.namespaces, namespaces) && sameList(existing.exclude, exclude) && sameList(grantKeys(existing.signals), grantKeys(signals))
  const setScope = async () => {
    if (!sameScope) await api.updateTelemetryIntentScope(conn, existing.id, namespaces, exclude, signals)
  }
  const setDestination = async () => {
    await api.updateTelemetryIntentDestination(conn, existing.id, destination)
  }
  if (sameDestination) await setScope()
  else {
    try {
      await setDestination()
      await setScope()
    } catch (e) {
      // Only a refusal (a 4xx other than 403) is worth the other order; a 5xx or a network failure (status 0)
      // would fail the same way.
      if (!(e instanceof ApiError) || e.status < 400 || e.status >= 500 || e.status === 403) throw e
      await setScope()
      await setDestination()
    }
  }
  return { intent: existing, action: 'updated' }
}

/** The whole action: ensure the intent, then ask the server for the commands (which reissues the certificate). */
export async function generateOperatorCommands(conn: Conn, o: { agentId: string; operatorId: string; name: string; draft: TelemetryInput }): Promise<{ result: TelemetryIntentCommand; action: 'created' | 'updated' }> {
  const { intent, action } = await ensureOperatorIntent(conn, o)
  return { result: await api.getTelemetryIntentCommand(conn, intent.id), action }
}

/** A server refusal in words a person can act on, always carrying the server's own message verbatim. */
export function explainIntentError(e: unknown): string {
  if (!(e instanceof ApiError)) return e instanceof Error && e.message ? e.message : 'Could not generate the commands.'
  if (e.status === 0) return e.message
  const lead =
    e.status === 403 ? 'Only an administrator can generate these commands.'
    : e.status === 409 ? 'This agent already has a conflicting telemetry intent.'
    : e.status === 404 ? 'The server could not find this agent or operator (it may have been removed or revoked).'
    : e.status < 500 ? 'The server refused this destination for the signals you turned on.'
    : 'The server could not generate the commands.'
  return `${lead} ${e.message}`.trim()
}
