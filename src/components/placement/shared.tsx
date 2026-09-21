import clsx from 'clsx'
import type { ReactNode } from 'react'
import { VERDICT_HELP, VERDICT_ICON, VERDICT_LABEL, VERDICT_TONE, type MoveVerdict } from '@/lib/movability'
import { basisText } from '@/lib/placement/engine'
import type { Evaluation, RttBasis } from '@/lib/placement/types'
import { TONE_CLASS } from '@/lib/provenance'

export const fmtMs = (n: number) => (Number.isFinite(n) ? (n < 10 ? n.toFixed(1) : Math.round(n).toString()) : '?')
export const pts = (n: number) => (Math.abs(n) >= 100 ? Math.round(n).toString() : n.toFixed(1))

const CONF: Record<Evaluation['confidence'], string> = {
  high: 'border-emerald-400/30 bg-emerald-400/10 text-emerald-300',
  medium: 'border-amber-400/30 bg-amber-400/10 text-amber-300',
  low: 'border-red-400/30 bg-red-400/10 text-red-300',
  none: 'border-nb-700 bg-nb-850 text-nb-300',
}
const CONF_HELP: Record<Evaluation['confidence'], string> = {
  high: 'Built mostly on measured traffic and measured or declared round trips.',
  medium: 'Some of the traffic or round trips are estimates.',
  low: 'Mostly estimates or unknowns. Treat as a hint, and measure before acting.',
  none: 'Something that decides this is not known at all. There is nothing to base a recommendation on.',
}

const CONF_LABEL: Record<Evaluation['confidence'], string> = { high: 'Well evidenced', medium: 'Partly estimated', low: 'Mostly guessed', none: 'No evidence' }

/** How far to trust an answer: the weakest fact that decides it. The word is always there; colour only adds to it. */
export function Confidence({ level }: { level: Evaluation['confidence'] }) {
  return (
    <span
      className={clsx('inline-flex items-center whitespace-nowrap rounded-md border px-2 py-0.5 text-xs', CONF[level], (level === 'low' || level === 'none') && 'border-dashed')}
      title={CONF_HELP[level]}
      data-testid="confidence"
      data-level={level}
    >
      <span className="sr-only">Confidence: </span>
      {CONF_LABEL[level]}
    </span>
  )
}

export function Verdict({ v }: { v: MoveVerdict }) {
  const Icon = VERDICT_ICON[v]
  return (
    <span className={clsx('inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border px-2 py-0.5 text-xs', TONE_CLASS[VERDICT_TONE[v]])} title={VERDICT_HELP[v]}>
      <Icon size={12} aria-hidden /> {VERDICT_LABEL[v]}
    </span>
  )
}

export function Basis({ b }: { b: RttBasis }) {
  const tone = b === 'measured' || b === 'same-cluster' ? 'text-emerald-300' : b === 'declared' || b === 'same-site' ? 'text-nb-400' : b === 'estimated' ? 'text-amber-300' : 'text-red-300'
  return <span className={clsx('text-xs', tone)}>{basisText(b)}</span>
}

export function Card({ title, aside, children, className }: { title?: string; aside?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={clsx('rounded-xl border border-nb-850 bg-nb-925 p-5', className)}>
      {(title || aside) && (
        <div className="mb-3 flex items-center gap-3">
          {title && <h3 className="text-sm font-medium text-white">{title}</h3>}
          {aside && <div className="ml-auto">{aside}</div>}
        </div>
      )}
      {children}
    </section>
  )
}
