import { ChevronRight, Loader2 } from 'lucide-react'
import { useEffect, useMemo, useRef, useState } from 'react'
import { Button, ICON_SM, RunStep } from '@/components/ui/primitives'
import { CopyCommand } from '@/components/ui/CopyCommand'
import { ServerCommand } from '@/components/ui/ServerCommand'
import ArrivalCheck from '@/features/pipeline/components/telemetry/ArrivalCheck'
import GuidedWizard from '@/features/pipeline/components/telemetry/GuidedWizard'
import { upgradeRequest, useAgentUpgradeCommand } from '@/lib/agentUpgrade'
import { api, atLeast } from '@/lib/api'
import { describeTelemetryChanges, measurementsRunning, telemetrySecrets, TELEMETRY_CREDENTIAL_VAR, seedTelemetryFromInstalled, TELEMETRY_SIGNALS, type AgentDiagnostics, type InstallInfo, type ReleaseTarget } from '@/lib/consent'
import { clearDraft, loadDraft, saveDraft } from '@/features/pipeline/lib/draftStore'
import { keptSummary, telemetryActive, telemetryProblems, type TelemetryInput } from '@/lib/install'
import { receiverAuthOf } from '@/features/pipeline/lib/operatorHealth'
import { explainIntentError, generateOperatorCommands, operatorCommandDraft, operatorTargets } from '@/features/pipeline/lib/operatorIntent'
import type { ReceiverAuth, TelemetryIntent } from '@/lib/types'
import { useOperators } from '@/lib/useOperators'
import { useConn, useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

/**
 * What telemetry this agent's chart install actually has enabled right now (self-reported by the agent,
 * the same pattern `installedTier` above already uses), plus a "change telemetry" form that builds the
 * `helm upgrade --reset-then-reuse-values` command for it. Like ConsentPanel's widen-hint, this only ever writes a
 * command - nothing here is pushed live, because telemetry is Helm-values-only in this chart, exactly like
 * the access-tier ceiling. There is no save button: the cluster's owner runs the command themselves.
 */
export function TelemetryPanel(props: TelemetryPanelProps) {
  // Everything below is state of ONE agent's draft, in ONE organisation: a panel that is handed another agent (the standalone wizard
  // picks one, a row re-renders for a different one) or whose organisation was switched must start over, not carry the draft, the
  // generated operator command and the "touched" flag of the last.
  const org = useConn().org
  return <TelemetryPanelBody key={`${props.agentId ?? ''}|${org ?? ''}`} {...props} />
}

type TelemetryPanelProps = Parameters<typeof TelemetryPanelBody>[0]

function TelemetryPanelBody({
  diagnostics: d,
  install,
  agentId,
  clusterId,
  target,
  initialScope,
  initialDestination,
  standalone = false,
  onBackToCluster,
  onDone,
  testIdPrefix = 'telemetry-panel',
}: {
  diagnostics?: AgentDiagnostics
  install?: InstallInfo
  /** The agent and cluster this telemetry is being configured for, when the caller knows them - the
   *  destination step uses the cluster to say which regional operator already receives it. */
  agentId?: string
  clusterId?: string
  /** Where this agent's Helm release lives (its own namespace and release name, as it reported them): the commands target it. The chart's
   *  documented defaults when absent, which is only right for an install that kept them. */
  target?: ReleaseTarget
  /** A scope draft handed off from the topology's "Define scope from selection" quick action, or from the
   * standalone telemetry wizard's own picker - pre-fills TelemetryFields' guided wizard and, in inline mode,
   * starts this panel's own disclosure open, so the person doesn't also have to notice and expand it by hand. */
  initialScope?: { name: string; namespaces: string[] }
  /** The operator (or FUSION) to send to, chosen before this opened ("Connect <cluster>"): the destination step starts on it. */
  initialDestination?: string
  /** Skip the "Change telemetry" disclosure chrome and render the form section directly, always open. A
   * modal whose entire purpose is configuring telemetry (the standalone TelemetryWizard) shouldn't hide its
   * own form behind a second disclosure - the inline usage on an already-expanded agent row keeps it. */
  standalone?: boolean
  /** Standalone only: goes back to choosing the cluster. */
  onBackToCluster?: () => void
  /** Standalone only: closes the dialog the setup is in, offered on its last screen. */
  onDone?: () => void
  /** Forwarded to the outer container, the inline disclosure (inline mode only), and TelemetryFields' own
   * testIdPrefix. Needed because an expanded Agents-page row and the standalone wizard can both be mounted
   * for the same agent at once - without a distinguishing prefix they'd share every nested testid. */
  testIdPrefix?: string
}) {
  const installed = d?.installedTelemetry ?? []
  // The commands below name the chart version and registry this server currently resolves: read them again now, so a tab that has been open
  // since before the install settings changed does not print an old one.
  const reloadInfo = useServer((s) => s.reloadInfo)
  useEffect(() => {
    void reloadInfo()
  }, [reloadInfo])
  // What the draft was started from: a saved draft is only offered back against the same install (see draftStore). The reported
  // configuration is part of it - a draft restored against a destination or source that has since changed would "keep as installed" what
  // is no longer there.
  const config = d?.installedTelemetryConfig
  const configKey = JSON.stringify(config ?? null)
  const basis = `${installed.join(',')}|${configKey}`
  const [restored, setRestored] = useState<TelemetryInput | null>(() => (agentId ? loadDraft(agentId, basis) : null))
  const [open, setOpen] = useState(() => !!initialScope || !!initialDestination || restored !== null)
  // Seeded from what the agent actually reports running, not a blank form: the command states every
  // signal explicitly on every call, so a blank draft would silently turn off everything the operator
  // didn't happen to re-check the moment they ran the generated command for an unrelated change. The
  // agent's installedTelemetryConfig (destination, redaction/resourcedetection/traces-sampling, energy/
  // accelerators source) seeds those same fields too, when the agent is new enough to report it - see
  // seedTelemetryFromInstalled's own doc comment for exactly which fields that covers.
  // The agent's active telemetry intent fills in what its self-report leaves out (the scope and destination it was granted), so that
  // reopening does not start from a draft that reads as "everything, nowhere". It arrives after the first paint and only replaces a draft
  // nobody has touched yet.
  const [intent, setIntent] = useState<TelemetryIntent | undefined>(undefined)
  const [draft, setDraft] = useState<TelemetryInput>(() => restored ?? seedTelemetryFromInstalled(installed, config))
  const touched = useRef(restored !== null)
  // What the install reports can arrive after this mounted (the first poll has not answered yet, or a newer report follows an applied
  // command): a draft nobody has edited follows it. Without this a panel opened before the report shows "Installed now: Kepler" above a blank
  // draft whose command would turn Kepler off, and one left open after a command was applied keeps stating the old install's kept values.
  const seedKey = basis + '|' + (intent?.id ?? '')
  const seededKey = useRef(seedKey)
  useEffect(() => {
    if (seededKey.current === seedKey) return
    seededKey.current = seedKey
    if (!touched.current) setDraft(seedTelemetryFromInstalled(installed, config, intent))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seedKey])
  const edit = (v: TelemetryInput) => {
    touched.current = true
    setDraft(v)
    if (agentId) saveDraft(agentId, basis, v)
  }
  const startOver = () => {
    if (agentId) clearDraft(agentId)
    touched.current = false
    setRestored(null)
    setDraft(seedTelemetryFromInstalled(installed, config, intent))
  }
  const measurementsOn = measurementsRunning(d)
  const changes = useMemo(() => describeTelemetryChanges(installed, d?.installedTelemetryConfig, draft), [installed, d?.installedTelemetryConfig, draft])
  // Without measurementsOn, the telemetryProblems check can never see
  // the one validation rule that depends on it - a network-latency signal with measurements off - so the
  // generated command used to build as if that problem didn't exist, silently disagreeing with the
  // role="alert" warning TelemetryFields (right above, given the same measurementsOn) already shows for
  // exactly that case.
  const conn = useConn()
  useEffect(() => {
    if (!agentId) return
    let cancelled = false
    void api
      .listTelemetryIntents(conn, agentId)
      .then((intents) => {
        const active = intents.find((i) => i.status === 'active')
        if (!cancelled) setIntent(active) // the effect above re-seeds a draft nobody has touched

      })
      .catch(() => undefined) // a seed, not a requirement: without it the draft is what the agent reports
    return () => {
      cancelled = true
    }
    // conn's identifying fields, not the object: see GuidedWizard's own operator fetch. The seed is read once per agent, not on every report.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conn.url, conn.org, agentId])
  // Whether the install has telemetry for the command to clear (a restored draft from before this was recorded has no such field).
  const hadTelemetry = draft.hadTelemetry || installed.length > 0 || !!config
  const turningOff = !telemetryActive(draft) && hadTelemetry
  const problems = telemetryActive(draft) ? telemetryProblems(draft, measurementsOn) : []
  // The block to paste is written by the server from this request: the Secrets the credential flags name are created first, and a failure
  // there (credential variable unset) stops everything before anything is upgraded. Who this belongs to is stamped on everything it emits,
  // whatever the destination: the signed-in organisation and this agent's cluster, added here rather than asked for in the wizard.
  const upgradeAsk = useMemo(
    () => upgradeRequest({ target, telemetry: { ...draft, hadTelemetry: hadTelemetry, resourceOrgId: conn.org ?? '', resourceClusterId: clusterId ?? '' }, measurementsOn }),
    [draft, hadTelemetry, measurementsOn, conn.org, clusterId, target],
  )
  const upgrade = useAgentUpgradeCommand(problems.length > 0 ? null : upgradeAsk)

  // A regional operator destination: its receiver wants a client certificate only the server can issue, so
  // there is no ready command - only an explicit "Generate" that asks the server for one (and issues a new
  // certificate every time it runs, which is why it never fires on render or on an edit).
  const isAdmin = useServer((s) => atLeast(s.role, 'admin'))
  // The regional operators this draft sends to: the one destination's, or - sending each signal type separately -
  // those of the signal types that go to one. Each is asked for its name and how its receiver authenticates.
  const targets = useMemo(() => operatorTargets(draft), [draft])
  const operatorId = targets[0]?.id ?? ''
  const targetKey = targets.map((t) => t.id).join(',')
  // Only the names and how each receiver authenticates, for the labels and the credential step; read (and kept current) only while a
  // destination is a regional operator.
  const { operators } = useOperators(isAdmin && targetKey !== '')
  const operatorInfo = useMemo(() => {
    const info: Record<string, { name: string; auth?: ReceiverAuth }> = {}
    for (const id of targetKey ? targetKey.split(',') : []) {
      const op = operators.find((o) => o.id === id)
      if (op) info[id] = { name: op.name, auth: receiverAuthOf(op) }
    }
    return info
  }, [operators, targetKey])
  const operatorLabel = targets.map((t) => operatorInfo[t.id]?.name || t.id).join(' and ')
  const authOf = (id: string): ReceiverAuth | undefined => operatorInfo[id]?.auth
  const operatorAuth = authOf(operatorId)
  // A receiver gated by the client certificate alone has no token to present: a Secret named in the draft is
  // ignored for it (the command builder drops it too, see operatorCommandDraft).
  const operatorMtls = !!operatorId && !draft.exportSplit && operatorAuth === 'mtls'
  const split = draft.exportSplit
  const commandDraft = split ? operatorCommandDraft(draft, undefined, authOf) : operatorMtls ? operatorCommandDraft(draft, 'mtls') : draft
  const operatorSecrets = telemetrySecrets(commandDraft, measurementsOn, target)
  const secrets = telemetrySecrets(draft, measurementsOn, target)
  const needsCredential = split ? operatorSecrets.length > 0 : !operatorMtls && secrets.length > 0
  const operatorProblems = telemetryActive(draft) ? telemetryProblems(commandDraft, measurementsOn) : []
  // Everything the generated block depends on. It is stale the moment any of it differs from what it was
  // generated from, and then shows dimmed and uncopyable rather than a command that is wrong now.
  const snapshot = useMemo(() => JSON.stringify({ draft, install, measurementsOn, agentId, operatorId, targetKey, namespace: target?.namespace, release: target?.release }), [draft, install, measurementsOn, agentId, operatorId, targetKey, target?.namespace, target?.release])
  const [generated, setGenerated] = useState<{ snapshot: string; text: string; action: 'created' | 'updated'; endpointNote?: string } | null>(null)
  const [generating, setGenerating] = useState(false)
  const [genError, setGenError] = useState<{ snapshot: string; message: string } | null>(null)
  const generate = async () => {
    if (generating || !agentId || !operatorId || operatorProblems.length > 0) return
    const asked = snapshot
    setGenerating(true)
    setGenError(null)
    try {
      const { result, action } = await generateOperatorCommands(conn, { agentId, operatorId, name: `Telemetry to ${operatorLabel}`, draft, measurementsOn })
      // The server checks the settings again before it issues a certificate. What it finds is said as it said it; no command is shown.
      if (result.problems?.length || !result.command) throw new Error(result.problems?.[0] ?? 'The server did not write a command.')
      const theirs = split ? undefined : result.endpoint
      setGenerated({
        snapshot: asked,
        text: result.command,
        action,
        endpointNote: theirs && theirs !== draft.exportEndpoint.trim() ? `These commands send to ${theirs}, the address Ikhnos has for ${operatorLabel}.` : undefined,
      })
    } catch (e) {
      setGenError({ snapshot: asked, message: explainIntentError(e) })
    } finally {
      setGenerating(false)
    }
  }
  const p = testIdPrefix
  const staleGenerated = generated !== null && generated.snapshot !== snapshot
  const operatorSection = (
    <div className="space-y-3" data-testid={`${p}-operator`}>
      {!isAdmin ? (
        <p className="text-xs text-nb-500" data-testid={`${p}-operator-admin-note`}>
          An administrator has to generate the commands for an operator destination: they include a client certificate for this cluster, which only an administrator can issue. No command is shown here.
        </p>
      ) : (
        <ol className="space-y-4">
          <RunStep n={1} title={`Generate the commands for ${operatorLabel}`}>
            <p>
              <span className="text-nb-300">{operatorLabel}</span> only accepts clients holding a certificate from your organisation&apos;s CA, so its commands are generated by the server, not built on this page. Generating issues a <span className="text-nb-300">fresh client certificate for this cluster</span> and is recorded in the audit log. The commands include a Secret holding that certificate and its private key; like the operator&apos;s own install output, it is shown once, here, so copy it when you generate it.
            </p>
            {targets.length > 1 && (
              <p className="mt-1" data-testid={`${p}-operator-several`}>
                {targets.length} operators are involved: it issues one certificate for each, shared by every signal type that goes to it ({targets.map((t) => `${operatorInfo[t.id]?.name || t.id}: ${t.lanes.join(' and ')}`).join('; ')}).
              </p>
            )}
            {operatorMtls && (
              <p className="mt-1" data-testid={`${p}-operator-mtls-note`}>
                This operator&apos;s receiver authenticates this cluster by that client certificate alone: no receiver token is needed or asked for.
              </p>
            )}
            {needsCredential && split && (
              <div className="mt-1 space-y-1" data-testid={`${p}-credential-hint`}>
                <p>Each Secret you named is created from a variable in the shell you run the command in - it never passes through this page. For an operator&apos;s receiver token, set it to <code className="font-mono text-nb-400">Bearer</code> followed by the token.</p>
                <ul className="space-y-0.5">
                  {operatorSecrets.map((s) => (
                    <li key={s.name}>
                      Set <code className="font-mono text-nb-400">{s.variable}</code> for {s.lanes.join(' and ')} (Secret <code className="font-mono text-nb-400">{s.name}</code>)
                    </li>
                  ))}
                </ul>
              </div>
            )}
            {needsCredential && !split && (
              <p className="mt-1" data-testid={`${p}-credential-hint`}>
                The receiver token Secret you named is created from <code className="font-mono text-nb-400">{TELEMETRY_CREDENTIAL_VAR}</code>: set it in that shell to <code className="font-mono text-nb-400">Bearer</code> followed by the operator&apos;s token first - it is read from there, so it never passes through this page.
              </p>
            )}
            <div className="mt-2 flex flex-wrap items-center gap-2">
              <Button variant="primary" size="sm" disabled={generating || !agentId || operatorProblems.length > 0} onClick={() => void generate()} data-testid={`${p}-operator-generate`}>
                {generating ? <Loader2 size={ICON_SM} className="animate-spin" aria-hidden /> : null}
                {generating ? 'Generating…' : generated ? `Generate again for ${operatorLabel}` : `Generate commands for ${operatorLabel}`}
              </Button>
              {!agentId && <span data-testid={`${p}-operator-needs-agent`}>Open this from an agent to generate its commands.</span>}
              {agentId && operatorProblems.length > 0 && <span data-testid={`${p}-operator-problem`}>{operatorProblems[0]}</span>}
            </div>
            {genError && genError.snapshot === snapshot && (
              <div role="alert" className="mt-2 rounded-md border border-bad/30 bg-bad/10 px-3 py-2 text-bad" data-testid={`${p}-operator-error`}>
                {genError.message}
              </div>
            )}
          </RunStep>
          <RunStep n={2} title="Run what it generated, in that cluster">
            {generated ? (
              <div data-testid={`${p}-operator-result`} data-stale={staleGenerated || undefined}>
                {staleGenerated ? (
                  <p className="text-warn" role="status" data-testid={`${p}-operator-stale`}>Out of date - generate again. The settings changed after this was generated, so it is no longer copyable.</p>
                ) : (
                  <p role="status" data-testid={`${p}-operator-fresh`}>
                    {generated.action === 'created' ? 'Recorded a telemetry intent for this agent.' : 'Updated this agent’s telemetry intent.'} The cluster&apos;s owner runs this in that cluster. It is safe to run more than once: the Secrets are replaced, not duplicated.
                  </p>
                )}
                {generated.endpointNote && <p className="mt-1" data-testid={`${p}-operator-endpoint-note`}>{generated.endpointNote}</p>}
                <CopyCommand text={generated.text} stale={staleGenerated} multiline testId={`${p}-operator-command`} />
              </div>
            ) : (
              <p>Nothing to run until step 1 has been done.</p>
            )}
          </RunStep>
        </ol>
      )}
    </div>
  )

  // Everything an external (non-operator) destination needs, as the numbered steps the wizard's last screen shows.
  const commandSection = (
    <ol className="space-y-4" data-testid={`${p}-run-steps`}>
      {needsCredential && (
        <RunStep n={1} title="Put the destination’s credential in your shell">
          {secrets.length > 1 || secrets[0]?.lanes.length ? (
            <div data-testid={`${p}-credential-hint`} className="space-y-1.5">
              <p>Each signal type that needs a credential reads it from a variable in the shell you run the command in - it never passes through this page. The command stores each as the Secret named, then upgrades the release.</p>
              <ul className="space-y-0.5">
                {secrets.map((s) => (
                  <li key={s.name}>
                    Set <code className="font-mono text-nb-400">{s.variable}</code> to the credential for {s.lanes.join(' and ')} (Secret <code className="font-mono text-nb-400">{s.name}</code>)
                  </li>
                ))}
              </ul>
            </div>
          ) : (
            <p data-testid={`${p}-credential-hint`}>
              Set <code className="font-mono text-nb-400">{TELEMETRY_CREDENTIAL_VAR}</code> to the destination&apos;s credential in the shell you run the command in - it is read from there, so it never passes through this page. The command stores it as the Secret you named, then upgrades the release.
            </p>
          )}
        </RunStep>
      )}
      <RunStep n={needsCredential ? 2 : 1} title={turningOff ? 'Run the command in that cluster to turn all telemetry off' : 'Run the command in that cluster'}>
        <p>The cluster&apos;s owner runs this. It changes only the telemetry settings and keeps everything else in the release as it is.</p>
        {problems.length > 0 ? (
          // Without a complete draft there is nothing to ask the server for, and a bare upgrade would change nothing: showing it as "the command" would let it be copied and run.
          <p role="status" className="mt-1 text-warn" data-testid={`${p}-command-blocked`}>
            There is no command yet: {problems[0]}
            {problems.length > 1 ? ` (and ${problems.length - 1} more)` : ''}.
          </p>
        ) : (
          <ServerCommand command={upgrade} />
        )}
      </RunStep>
    </ol>
  )

  const { agents, clusters } = useTopology()
  const clusterName = clusters?.find((c) => c.id === clusterId)?.name
  const wanted = TELEMETRY_SIGNALS.filter((s) => (draft as unknown as Record<string, boolean>)[s.id]).map((s) => s.id)
  const form = (
    <GuidedWizard
      value={draft}
      onChange={edit}
      testIdPrefix={testIdPrefix}
      initialScope={initialScope}
      initialDestination={initialDestination}
      agentId={agentId}
      clusterId={clusterId}
      clusterName={clusterName}
      problems={problems}
      review={{ diff: changes, installed: installed.length > 0, kept: hadTelemetry ? keptSummary(draft) : [] }}
      onBackToCluster={onBackToCluster}
      onDone={onDone}
      runSection={
        telemetryActive(draft) || turningOff ? (
          <div className="space-y-3" data-testid={`${p}-run`}>
            {targets.length > 0 && !turningOff ? operatorSection : commandSection}
          </div>
        ) : undefined
      }
      checkSection={<ArrivalCheck wanted={wanted} diagnostics={d} connected={agents?.find((a) => a.id === agentId)?.connected} cluster={clusterName ?? 'the cluster'} testId={`${p}-arrival`} />}
    />
  )

  return (
    <div data-testid={testIdPrefix}>
      {!standalone && (
        <>
          <div className="mb-1.5 text-sm font-medium text-nb-300">Installed now</div>
          {installed.length === 0 ? (
            <p className="text-xs text-nb-500">{d ? 'No telemetry signal is enabled in this install.' : 'Not reported yet.'}</p>
          ) : (
            <ul className="flex flex-wrap gap-1.5" data-testid="telemetry-installed">
              {installed.map((id) => {
                const s = TELEMETRY_SIGNALS.find((x) => x.id === id)
                return (
                  <li key={id} title={s?.what} className="rounded border border-nb-800 bg-nb-930 px-1.5 py-0.5 text-xs text-nb-300">
                    {s?.label ?? id}
                  </li>
                )
              })}
            </ul>
          )}
          {installed.length > 0 && (
            <p className="mt-1.5 text-xs text-nb-500" data-testid="telemetry-installed-note">
              The settings below start from these. The command they create states every signal: the ones left ticked stay on (or turn on), every other one is turned off.
            </p>
          )}

          {installed.length > 0 && (
            <div className="mt-3" data-testid="export-health">
              <div className="mb-1.5 text-sm font-medium text-nb-300">Is data arriving?</div>
              <ArrivalCheck passive wanted={installed} diagnostics={d} connected={agents?.find((a) => a.id === agentId)?.connected} cluster={clusterName ?? 'the cluster'} testId="export-health-check" />
            </div>
          )}

        </>
      )}

      {restored && (
        <p className="mt-3 flex flex-wrap items-center gap-2 text-xs text-nb-500" data-testid={`${testIdPrefix}-restored`}>
          Your unfinished changes from earlier in this session are back.
          <button type="button" className="text-accent hover:underline" onClick={startOver} data-testid={`${testIdPrefix}-start-over`}>Start over</button>
        </p>
      )}

      {standalone ? (
        <div className={restored ? 'mt-3' : undefined}>{form}</div>
      ) : (
        <details className="group mt-3" open={open} onToggle={(e) => setOpen(e.currentTarget.open)} data-testid={`${testIdPrefix}-change`}>
          <summary className="flex cursor-pointer select-none items-center gap-1 text-xs font-medium text-nb-400 hover:text-nb-300 marker:content-none">
            <ChevronRight size={ICON_SM} className="transition-transform group-open:rotate-90" aria-hidden />
            Change telemetry
          </summary>
          <div className="mt-3 rounded-lg border border-nb-850 bg-nb-925 p-3">{form}</div>
        </details>
      )}
    </div>
  )
}
