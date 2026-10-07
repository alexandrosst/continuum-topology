import { Antenna } from 'lucide-react'
import { OperatorHealth } from '@/components/operators/OperatorHealth'
import { Button, EmptyState, ICON_SM, LiveDot, Pill, Table, Td, Th, Waiting } from '@/components/ui/primitives'
import { TELEMETRY_SIGNALS } from '@/lib/consent'
import { exportSummary } from '@/lib/exportHealth'
import { ageOf } from '@/lib/history'
import { CENTRAL_OPERATOR_ID } from '@/lib/fusionStatus'
import { intentDestination, type LocalRow } from '@/lib/operatorsView'
import type { RegionalOperator } from '@/lib/types'

/** Add up to what the page has beside the sidebar at 1280 px, so the Configure button is on screen without scrolling the table. */
const COLS = ['w-40', 'w-48', 'w-44', 'w-36', 'w-32', 'w-24']

/** What the table says about an agent that was asked for telemetry and has not reported it running. */
const PENDING_TEXT = 'Nothing reported running yet'

/** What an agent runs, as chips that wrap inside their cell (a nowrap list overflows into the next column at this width); the first few,
 *  then "+N" with the rest in its title. */
function SignalPills({ labels }: { labels: string[] }) {
  const shown = labels.slice(0, 4)
  if (labels.length === 0) return <span className="text-nb-700">—</span>
  return (
    <>
      {shown.map((l) => <Pill key={l} className="text-xs text-nb-400">{l}</Pill>)}
      {labels.length > shown.length && <span className="text-xs text-nb-500" title={labels.join(', ')}>+{labels.length - shown.length}</span>}
    </>
  )
}

/**
 * The Local tab: every approved agent that runs telemetry, or has been asked to. What each runs comes from what the agent reports; where it
 * sends comes from the request that was made of it, so a command that was just run shows here before its first report - as one
 * waiting banner above the table (the page's single spinner) and a hollow ring on the row, not a spinner per row.
 */
export default function LocalOperatorsTab({ rows, operators, canConfigure, onConfigure, now = Date.now() }: { rows: LocalRow[]; operators: RegionalOperator[]; canConfigure: boolean; onConfigure: (agentId?: string) => void; now?: number }) {
  if (rows.length === 0) {
    return (
      <EmptyState
        title="No local operators running yet"
        description="A local operator is an already-connected cluster's agent with at least one telemetry signal turned on. Configure one to see it here."
        action={canConfigure ? <Button variant="primary" onClick={() => onConfigure()} data-testid="local-configure"><Antenna size={ICON_SM} /> Configure telemetry</Button> : undefined}
      />
    )
  }
  const pending = rows.filter((r) => r.pending).length
  return (
    <div className="space-y-3">
      {pending > 0 && (
        <p className="text-sm text-nb-400" data-testid="local-waiting">
          <Waiting>
            Waiting for {pending} {pending === 1 ? 'cluster' : 'clusters'} to report. The first report can take a few minutes after the command is run.
          </Waiting>
        </p>
      )}
      <Table cols={COLS} data-testid="local-operators-table">
        <thead>
          <tr><Th>Cluster</Th><Th>Signals</Th><Th>Destination</Th><Th>Data</Th><Th>Last reported</Th><Th className="sticky right-0 bg-nb-925" /></tr>
        </thead>
        <tbody>
          {rows.map((r) => {
            const signals = r.pending ? (r.intent?.signals.map((s) => s.id) ?? []) : r.installed
            const dest = intentDestination(r.intent)
            const target = dest?.operatorId ? operators.find((o) => o.id === dest.operatorId) : undefined
            const data = r.pending ? { kind: 'starting' as const, text: PENDING_TEXT } : exportSummary(r.installed, r.diagnostics)
            return (
              <tr key={r.agent.id} className="group hover:bg-nb-930/60" data-testid={`local-operator-${r.agent.name}`}>
                <Td className="text-nb-300">
                  <div className="truncate" title={r.cluster?.name ?? r.agent.name}>{r.cluster?.name ?? r.agent.name}</div>
                  <div className="truncate text-xs text-nb-500" title={r.agent.name}>{r.agent.name}</div>
                </Td>
                <Td>
                  <div className="flex flex-wrap items-center gap-1.5">
                    <SignalPills labels={TELEMETRY_SIGNALS.filter((s) => signals.includes(s.id)).map((s) => s.label)} />
                    {r.drifted && (
                      <Pill title="What this agent reports running is not what was asked of it: the install was changed, or the command has not been run again." className="text-xs text-warn">
                        <span data-testid={`local-drifted-${r.agent.name}`}>Drifted</span>
                      </Pill>
                    )}
                  </div>
                </Td>
                <Td className="text-nb-400">
                  {!dest ? (
                    <span className="text-nb-500">Not recorded</span>
                  ) : target ? (
                    <div data-testid={`local-destination-${r.agent.name}`}>
                      <div className="truncate" title={target.name}>{target.name}</div>
                      <OperatorHealth operator={target} central={target.id === CENTRAL_OPERATOR_ID} />
                    </div>
                  ) : (
                    <span className="block truncate font-mono text-xs" title={dest.label} data-testid={`local-destination-${r.agent.name}`}>{dest.label}</span>
                  )}
                </Td>
                <Td>
                  <span className="inline-flex items-center gap-1.5 text-xs text-nb-400" data-testid={`local-data-${r.agent.name}`} data-state={data.kind}>
                    <LiveDot kind={data.kind} /> {data.text}
                  </span>
                </Td>
                <Td className="text-nb-500">{r.reportedAt ? <span title={new Date(r.reportedAt).toLocaleString()}>{ageOf(r.reportedAt, now)}</span> : 'Not yet'}</Td>
                <Td className="sticky right-0 bg-nb-925 text-right group-hover:bg-nb-930">
                  {canConfigure && <Button size="sm" aria-label={`Configure ${r.cluster?.name ?? r.agent.name}`} onClick={() => onConfigure(r.agent.id)}>Configure</Button>}
                </Td>
              </tr>
            )
          })}
        </tbody>
      </Table>
    </div>
  )
}
