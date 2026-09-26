import clsx from 'clsx'
import { ChevronDown, ChevronRight, CircleHelp, Check, X } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { EvidenceChip, ObservationChip } from '@/components/ui/primitives'
import { formatFactValue, type Class, type FactRow, type Fix, type Verdict } from '@/lib/advice'
import { factName, inputsSummary, obsOfState } from '@/lib/placement/why'
import { ageLabel } from '@/lib/provenance'

/* ---------- the verdict, never by colour alone: an icon, a word and a border style ---------- */

const FIT: Record<Verdict, { label: string; tone: string; Icon: typeof Check; help: string }> = {
  fits: { label: 'Fits', tone: 'border-ok/40 bg-ok/10 text-ok', Icon: Check, help: 'Every check holds, even at the pessimistic end of every figure that is not exact.' },
  cantTell: { label: 'Can’t tell', tone: 'border-dashed border-warn/60 bg-warn/10 text-warn', Icon: CircleHelp, help: 'Something that decides it is not known, or too uncertain to certify. It is not a yes and it is not a no.' },
  doesNotFit: { label: 'Does not fit', tone: 'border-bad/40 bg-bad/10 text-bad', Icon: X, help: 'Something rules it out, even at the optimistic end of every figure that is not exact.' },
}

/** Fits / Can't tell / Does not fit. */
export function FitBadge({ verdict, className }: { verdict: Verdict; className?: string }) {
  const { label, tone, Icon, help } = FIT[verdict]
  return (
    <span className={clsx('inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-0.5 text-xs font-medium', tone, className)} title={help} data-testid="fit-badge" data-verdict={verdict}>
      <Icon size={12} aria-hidden /> {label}
    </span>
  )
}

/* ---------- hedging ---------- */

/** Shown wherever advice is only a hint: low or no confidence. Says so before anything else does. */
export function Hedge({ level, inputs, verdict }: { level: 'low' | 'none' | 'medium' | 'high'; inputs: { class: Class }[]; verdict?: Verdict }) {
  if (level !== 'low' && level !== 'none') return null
  const s = inputsSummary(inputs)
  return (
    <p className="flex items-start gap-2 rounded-lg border border-dashed border-warn/50 bg-warn/5 px-3 py-2 text-sm text-warn" role="note" data-testid="hedge" data-level={level}>
      <CircleHelp size={15} className="mt-0.5 shrink-0" aria-hidden />
      <span>
        <span className="font-medium">Not enough evidence to recommend</span>
        {s && <> — {s}</>}.{verdict === 'cantTell' && ' Whether it fits could not be told.'} Treat this as a hint, not the best option, and check before acting.
      </span>
    </p>
  )
}

/* ---------- why: the facts used ---------- */

function FactLine({ f, now }: { f: FactRow; now: number }) {
  const age = ageLabel(f.observedAt, now)
  const info = obsOfState(f.state)
  const value = f.value === null ? 'unknown' : formatFactValue(f.value, f.unit)
  return (
    <li className="flex flex-wrap items-baseline gap-x-2 gap-y-1 py-2 text-xs" data-testid="fact" data-attribute={f.attribute} data-confidence={f.confidence} data-known={f.value !== null}>
      <span className="text-nb-300">
        {factName(f.attribute.split(':')[0])}
        {f.entityName && <span className="text-nb-500"> · {f.entityName}</span>}
      </span>
      <span className={clsx('tabular-nums', f.value === null ? 'italic text-warn' : 'text-nb-300')} data-testid="fact-value">{value}</span>
      {f.low !== undefined && f.low !== null && f.high !== undefined && f.high !== null && f.value !== null && (
        <span className="tabular-nums text-nb-500">({formatFactValue(f.low, f.unit)} to {formatFactValue(f.high, f.unit)})</span>
      )}
      <EvidenceChip level={f.confidence} always why={f.evidence} />
      <span className="text-nb-500">from {f.source}</span>
      <ObservationChip info={info} />
      {age && <span className={clsx('text-nb-500', f.aged && 'text-warn')}>{f.aged ? 'not confirmed for ' : 'confirmed '}{age}{f.aged ? '' : ' ago'}</span>}
      {f.evidence && <span className="basis-full text-nb-500">{f.evidence}</span>}
    </li>
  )
}

/** What would change an answer, and what to do to find out. */
function Changes({ lines, fixes }: { lines: string[]; fixes: Fix[] }) {
  return (
    <>
      {lines.length > 0 && (
        <div className="mt-3" data-testid="would-change">
          <div className="text-[11px] font-medium uppercase tracking-wide text-nb-500">What would change this</div>
          <ul className="mt-1 list-disc space-y-1 pl-4 text-xs text-nb-300">
            {lines.map((l) => (
              <li key={l}>{l}</li>
            ))}
          </ul>
        </div>
      )}
      {fixes.length > 0 && (
        <div className="mt-3" data-testid="fixes">
          <div className="text-[11px] font-medium uppercase tracking-wide text-nb-500">To find out</div>
          <ul className="mt-1 space-y-1 text-xs">
            {fixes.map((x) => (
              <li key={x.action + x.text} className="text-nb-300">
                {x.link ? (
                  <Link to={x.link} className="text-accent hover:underline" data-testid="fix-link">{x.text}</Link>
                ) : (
                  x.text
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </>
  )
}

/**
 * "Why": the facts an answer used, each with its source, how sure it is, its observation state and its age, then what
 * would change the answer and what would make an unknown known. Collapsed until asked for.
 */
export function Why({ facts, wouldChange, fixes = [], sensitivity = [], now, label = 'Why', defaultOpen, children }: {
  facts: FactRow[]
  wouldChange: string[]
  fixes?: Fix[]
  /** "still fits if free capacity is off by up to 22 %" */
  sensitivity?: string[]
  now: number
  label?: string
  defaultOpen?: boolean
  children?: ReactNode
}) {
  const [open, setOpen] = useState(!!defaultOpen)
  const id = useId()
  const n = facts.length
  return (
    <div className="mt-2 text-xs" data-testid="why">
      <button type="button" onClick={() => setOpen((o) => !o)} className="inline-flex items-center gap-1 text-nb-400 hover:text-nb-300" aria-expanded={open} aria-controls={id} data-testid="why-toggle">
        {open ? <ChevronDown size={13} aria-hidden /> : <ChevronRight size={13} aria-hidden />} {label}
        {n > 0 && <span className="text-nb-600">({n} fact{n === 1 ? '' : 's'})</span>}
      </button>
      {open && (
        <div id={id} className="mt-2 rounded-lg border border-nb-850 bg-nb-930 px-3 py-2" data-testid="why-panel">
          {sensitivity.length > 0 && (
            <ul className="mb-1 space-y-0.5 text-nb-300" data-testid="sensitivity">
              {sensitivity.map((s) => (
                <li key={s}>{s}</li>
              ))}
            </ul>
          )}
          {n > 0 ? (
            <ul className="divide-y divide-nb-850/60">
              {facts.map((f, i) => (
                <FactLine key={`${f.attribute}|${f.entity ?? ''}|${i}`} f={f} now={now} />
              ))}
            </ul>
          ) : (
            <p className="text-nb-500">No facts were needed: nothing about the target decides this.</p>
          )}
          <Changes lines={wouldChange} fixes={fixes} />
          {children}
        </div>
      )}
    </div>
  )
}
