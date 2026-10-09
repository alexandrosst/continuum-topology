import { ShieldCheck } from 'lucide-react'
import { MeterBar } from '@/components/ui/MeterBar'
import RowMenu, { type RowMenuItem } from '@/components/operators/RowMenu'
import StateChip from '@/components/operators/StateChip'
import { Button, ChipList, CopyIconButton, ICON_SM, Pill, Table, Td, Th, TierBadge } from '@/components/ui/primitives'
import { certLife } from '@/lib/certLifetime'
import { addressCell, KIND_LABEL, lastDataText, type CertCell, type ComponentRow } from '@/lib/operatorsView'

/** Fixed widths that add up to what the page has beside the sidebar at 1280 px, so the row's menu is on screen without scrolling the table
 *  sideways; the State column takes the rest, because it is the one that holds a sentence. */
const COLS = ['w-64', '', 'w-20', 'w-44', 'w-36', 'w-28', 'w-12']

/** What the certificate column shows: a thin bar of how much of the 30-day lifetime is left, and words only when something is wrong. Operator
 *  certificates renew themselves, so that is the normal state and says nothing; an agent's own has no date, so it is just a quiet mark. */
function CertLife({ cert, now }: { cert: CertCell; now: number }) {
  if (cert.kind === 'none') return <span className="text-nb-700" aria-label="None">—</span>
  const life = certLife(cert, now)
  if (!life) return <span role="img" aria-label="Renews automatically" title="Renews automatically"><ShieldCheck size={ICON_SM} className="text-nb-500" aria-hidden /></span>
  return (
    <span className="inline-flex flex-wrap items-center gap-x-2 gap-y-1 align-middle text-xs" title={life.text}>
      <MeterBar pct={life.fraction * 100} tone={life.tone} label="Certificate lifetime" valueText={life.text} />
      {life.legacy && <Pill title="Issued for longer than the 30 days new certificates last, so it does not renew the way they do" className="text-[11px] text-nb-500">legacy</Pill>}
      {life.exception && <span className={life.tone === 'bad' ? 'text-bad' : 'text-warn'}>{life.exception}</span>}
    </span>
  )
}

/** A mobile cell names itself (the header is gone there); on a wide screen the header does it. */
const Label = ({ children }: { children: string }) => <span className="mr-1 text-nb-500 md:hidden">{children}</span>

/**
 * Every component of the data path in one table, whatever its kind: the same columns, the same state words and the same menu in the same
 * place on every row. Below the md breakpoint each row is a card with its state first.
 */
export default function ComponentsTable({ rows, now, menuFor, onWhatToDo }: { rows: ComponentRow[]; now: number; menuFor: (row: ComponentRow) => RowMenuItem[]; onWhatToDo: (row: ComponentRow) => void }) {
  return (
    <Table cols={COLS} stack data-testid="components-table">
      <thead>
        <tr><Th>Name</Th><Th>State</Th><Th>Version</Th><Th>Certificate</Th><Th>Sends to</Th><Th>Last data</Th><Th className="sticky right-0 bg-nb-925" actionsLabel="Actions" /></tr>
      </thead>
      <tbody>
        {rows.map((r) => {
          const addr = r.operator && !r.ended && r.kind === 'regional' ? addressCell(r.operator) : undefined
          const { state, reason, todo } = r.verdict
          return (
            <tr key={r.id} className="group relative hover:bg-nb-930/60 max-md:border-b max-md:border-nb-850/60 max-md:px-1 max-md:py-3" data-testid={r.operator ? `operator-${r.operator.name}` : `component-${r.id}`} data-kind={r.kind} data-state={state}>
              <Td valign="top" className="max-md:block max-md:border-0 max-md:py-0 max-md:pr-12">
                <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  <span className="min-w-0 truncate font-medium text-nb-300" title={r.name}>{r.name}</span>
                  {r.cluster && <TierBadge tier={r.cluster.tier} />}
                </div>
                <div className="mt-0.5 truncate text-xs text-nb-500">
                  {r.kind === 'central' ? 'Receives data for FUSION' : KIND_LABEL[r.kind]}
                  {r.cluster ? ` · ${r.cluster.name}` : ''}
                  {r.operator && r.kind === 'regional' ? ` · ${r.operator.sourceClusterIds.length} cluster${r.operator.sourceClusterIds.length === 1 ? '' : 's'}` : ''}
                </div>
                {addr?.kind === 'set' && (
                  <div className="mt-0.5 flex items-center gap-1 text-xs" data-testid={`component-address-${r.id}`}>
                    <span className="min-w-0 truncate font-mono text-nb-400" title={addr.address}>{addr.address}</span>
                    <CopyIconButton text={addr.address} title={`Copy the address of ${r.name}`} />
                  </div>
                )}
                {(r.operator?.labels?.length ?? 0) > 0 && <div className="mt-1"><ChipList items={r.operator!.labels!.map((l) => `${l.key}=${l.value}`)} max={3} /></div>}
              </Td>
              <Td valign="top" className="max-md:block max-md:border-0 max-md:pb-0 max-md:pt-2">
                <StateChip state={state} />
                {reason && state !== 'healthy' && <p className="mt-1 text-xs leading-relaxed text-nb-400" data-testid={`component-reason-${r.id}`}>{reason}</p>}
                {todo && (
                  <Button size="sm" className="mt-2" onClick={() => onWhatToDo(r)} aria-label={`What to do about ${r.name}`} data-testid={`component-todo-${r.id}`}>What to do</Button>
                )}
              </Td>
              <Td valign="top" className="whitespace-nowrap text-xs text-nb-500 max-md:inline-block max-md:border-0 max-md:pb-0 max-md:pt-2"><Label>Version</Label>{r.version ? `v${r.version}` : <span className="text-nb-700">—</span>}</Td>
              <Td valign="top" className="text-xs max-md:block max-md:border-0 max-md:pb-0 max-md:pt-1"><Label>Certificate</Label><CertLife cert={r.cert} now={now} /></Td>
              <Td valign="top" className="text-xs text-nb-400 max-md:block max-md:border-0 max-md:pb-0 max-md:pt-1"><Label>Sends to</Label><span className="break-words font-mono md:block md:truncate" title={r.sendsTo}>{r.sendsTo}</span></Td>
              <Td valign="top" className="whitespace-nowrap text-xs text-nb-500 max-md:block max-md:border-0 max-md:pb-0 max-md:pt-1"><Label>Last data</Label><span title={r.lastData ? new Date(r.lastData).toLocaleString() : undefined}>{lastDataText(r.lastData, now)}</span></Td>
              <Td valign="top" className="sticky right-0 bg-nb-925 text-right group-hover:bg-nb-930 max-md:absolute max-md:right-1 max-md:top-2 max-md:border-0 max-md:bg-transparent max-md:p-0">
                <RowMenu ariaLabel={`Actions for ${r.name}`} items={menuFor(r)} testId={r.operator ? `operator-menu-${r.operator.name}` : `component-menu-${r.id}`} />
              </Td>
            </tr>
          )
        })}
      </tbody>
    </Table>
  )
}
