import { ExternalLink, Stethoscope } from 'lucide-react'
import { useMemo } from 'react'
import { CopyCommand } from '@/components/ui/CopyCommand'
import { StatusBadge, StatusPip, type StatusTone } from '@/components/ui/Status'
import { Button, CopyButton, Modal, ICON_SM } from '@/components/ui/primitives'
import { OpenError, useOpenPage } from '@/features/fusion/components/OpenPages'
import { useFusion } from '@/features/fusion/components/useFusion'
import { diagnoseAgent, diagnosisText, type Check, type Diagnosis } from '@/lib/diagnose'
import type { AgentExtras } from '@/lib/consent'
import type { Agent } from '@/lib/types'
import { useServer } from '@/store/server'

const GROUPS: Check['group'][] = ['Connection', 'Server and agent agree', 'Certificate and clock', 'What it can see', 'What it reports']

/** The look of a verdict: the same six tones every status in the product has. */
const VERDICT_CARD: Record<Diagnosis['verdict'], string> = {
  healthy: 'border-ok/30 bg-ok/5',
  attention: 'border-warn/30 bg-warn/5',
  down: 'border-bad/30 bg-bad/5',
  setup: 'border-info/30 bg-info/5',
  unknown: 'border-nb-850 bg-nb-925',
}

/**
 * Diagnose: one place that says what is wrong with an agent and in what order to deal with it, from what the server already holds (its
 * stream, its conditions, its certificate, what it reports about itself). It does not reach into the cluster. Every line names what was
 * looked at, fine or not, so "nothing is wrong" is something that was checked.
 */
export function DiagnoseDrawer({ agent, extras, now, onClose }: { agent: Agent; extras: AgentExtras; now: number; onClose: () => void }) {
  const dx = useMemo(() => diagnoseAgent(agent, extras, now), [agent, extras, now])
  const isAdmin = useServer((s) => s.isAdmin())
  const refresh = useServer((s) => s.refresh)
  const fusion = useFusion(isAdmin)
  const page = useOpenPage()
  const grafana = !!fusion.status?.links?.grafana && !!agent.clusterId
  return (
    <Modal open side onClose={onClose} title={`Diagnose ${agent.name}`} description="What the server knows about this agent right now, worst first. It reads what the server holds; it does not reach into the cluster." width="max-w-2xl">
      <div className="space-y-6" data-testid="diagnose">
        <section className={`rise-in rounded-xl border px-5 py-4 ${VERDICT_CARD[dx.verdict]}`} style={{ '--i': 0 } as React.CSSProperties} data-testid="diagnose-verdict" data-verdict={dx.verdict}>
          <div className="flex flex-wrap items-center gap-2">
            <StatusBadge tone={dx.verdict} />
            <span className="text-sm text-nb-300">{dx.headline}</span>
          </div>
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <CopyButton text={diagnosisText(agent, extras, dx, now)} label="Copy summary" />
            <Button size="sm" onClick={() => void refresh()}>Check again</Button>
            {grafana && (
              <Button size="sm" disabled={page.opening} onClick={() => void page.open('grafana', { dashboard: 'ikhnos-self', cluster: agent.clusterId })} data-testid="diagnose-grafana">
                <ExternalLink size={ICON_SM} aria-hidden /> Its charts in Grafana
              </Button>
            )}
          </div>
          <OpenError message={page.error} />
        </section>

        {dx.findings.length > 0 && (
          <section aria-labelledby="dx-todo">
            <h3 id="dx-todo" className="mb-2 text-sm font-medium text-nb-300">What to do</h3>
            <ol className="space-y-3" data-testid="diagnose-findings">
              {dx.findings.map((f, i) => (
                <li key={f.id} className="rise-in rounded-xl border border-nb-850 bg-nb-925 px-4 py-3" style={{ '--i': i + 1 } as React.CSSProperties} data-testid="diagnose-finding" data-tone={f.tone} data-finding={f.id}>
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs tabular-nums text-nb-600">{i + 1}</span>
                    <StatusBadge tone={f.tone} />
                    <span className="text-sm font-medium text-nb-300">{f.title}</span>
                  </div>
                  <p className="mt-1.5 break-words text-sm text-nb-400">{f.detail}</p>
                  {f.fix && <p className="mt-1 text-sm text-nb-300"><span className="text-nb-500">What to do: </span>{f.fix}</p>}
                  {f.command && <CopyCommand text={f.command} multiline label={`Copy the command for: ${f.title}`} testId="diagnose-command" />}
                </li>
              ))}
            </ol>
          </section>
        )}

        <section aria-labelledby="dx-checked">
          <h3 id="dx-checked" className="mb-2 text-sm font-medium text-nb-300">Everything that was checked</h3>
          <div className="space-y-4">
            {GROUPS.map((g) => {
              const rows = dx.checks.filter((c) => c.group === g)
              if (rows.length === 0) return null
              return (
                <div key={g}>
                  <h4 className="mb-1 text-xs font-medium uppercase tracking-wide text-nb-500">{g}</h4>
                  <ul className="divide-y divide-nb-850/60 rounded-xl border border-nb-850 bg-nb-925">
                    {rows.map((c) => (
                      <li key={c.id} className="flex items-start gap-3 px-4 py-2.5 text-sm" data-testid="diagnose-check" data-check={c.id} data-tone={c.tone}>
                        <span className="mt-1.5"><StatusPip tone={c.tone} /></span>
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-baseline justify-between gap-x-4">
                            <span className="text-nb-300">{c.label}</span>
                            <span className="break-words text-right text-nb-400">{c.value}</span>
                          </div>
                          {c.note && <p className="mt-0.5 break-words text-xs text-nb-500">{c.note}</p>}
                        </div>
                      </li>
                    ))}
                  </ul>
                </div>
              )
            })}
          </div>
        </section>
      </div>
    </Modal>
  )
}

/** The button that opens it. A small, plain control: it sits in a row beside the agent’s other actions. */
export function DiagnoseButton({ onClick, tone }: { onClick: () => void; tone?: StatusTone }) {
  return (
    <Button size="sm" onClick={(e) => { e.stopPropagation(); onClick() }} data-testid="diagnose-open" title="What is wrong with this agent, and what to do about it">
      <Stethoscope size={ICON_SM} aria-hidden /> Diagnose {tone && tone !== 'healthy' && tone !== 'unknown' && <StatusPip tone={tone} pulse={tone === 'down'} />}
    </Button>
  )
}
