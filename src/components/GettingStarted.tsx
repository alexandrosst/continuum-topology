import clsx from 'clsx'
import { Check, X } from 'lucide-react'
import { useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import { buttonClass } from '@/components/ui/buttonClass'
import { Button, Pill } from '@/components/ui/primitives'
import { deriveChecklist, dismiss, wasDismissed, type Checklist, type ChecklistStep } from '@/lib/checklist'
import { useServer } from '@/store/server'
import { useTopology } from '@/store/topology'

/**
 * Where a new organisation stands, from what the server holds. Null when the list does not apply: no server in use, a
 * person who cannot connect clusters, or the server has not answered yet (so nothing false flashes first). It is also
 * null once a cluster is live: the list has done its job and leaves no gap behind.
 */
export function useGettingStarted(): { checklist: Checklist; dismissed: boolean; dismiss: () => void } | null {
  const connected = useServer((s) => s.status === 'connected')
  const admin = useServer((s) => s.isAdmin())
  const org = useServer((s) => s.orgId)
  const info = useServer((s) => s.info)
  const loaded = useServer((s) => s.state !== undefined)
  const { agents, clusters, nodes } = useTopology()
  const [dismissedNow, setDismissedNow] = useState(false)
  const checklist = useMemo(
    () =>
      deriveChecklist({
        agents,
        clusters,
        probedNodes: nodes.filter((n) => n.probed && !n.deletedAt).length,
      }),
    [agents, clusters, nodes],
  )
  if (!connected || !admin || !org || !info || !loaded || checklist.finished) return null
  return { checklist, dismissed: dismissedNow || wasDismissed(org), dismiss: () => { dismiss(org); setDismissedNow(true) } }
}

const MARK = 'grid size-6 shrink-0 place-items-center rounded-full text-xs font-medium tabular-nums'

function Marker({ step, n }: { step: ChecklistStep; n: number }) {
  return (
    <span
      aria-hidden
      className={clsx(
        MARK,
        step.state === 'done' && 'bg-emerald-400/15 text-emerald-300',
        step.state === 'current' && 'border border-accent bg-accent-soft text-accent',
        step.state === 'todo' && 'border border-nb-700 text-nb-500',
      )}
    >
      {step.state === 'done' ? <Check size={14} /> : n}
    </span>
  )
}

const STATE_WORD = { done: 'Done', current: 'Next step', todo: 'Not yet' } as const

function Action({ step, onConnect }: { step: ChecklistStep; onConnect: () => void }) {
  const a = step.action
  if (!a) return null
  const variant = step.state === 'current' ? 'primary' : 'secondary'
  const cls = buttonClass(variant, 'sm', 'whitespace-nowrap')
  const testId = `gs-action-${step.id}`
  switch (a.kind) {
    case 'approval':
      return <Link to={`/agents#approval-${a.agentId}`} className={cls} data-testid={testId}>{a.label}</Link>
    case 'agents':
      return <Link to="/agents" className={cls} data-testid={testId}>{a.label}</Link>
    case 'connect':
      return <Button size="sm" variant={variant} onClick={onConnect} data-testid={testId}>{a.label}</Button>
  }
}

/**
 * The first-run list. `card` sits on a page and can be dismissed; `hero` stands in for an empty page and is
 * centred, with room for what else the page offers below it.
 */
export default function GettingStarted({ checklist, onConnect, onDismiss, variant = 'card', children }: { checklist: Checklist; onConnect: () => void; onDismiss?: () => void; variant?: 'card' | 'hero'; children?: React.ReactNode }) {
  const total = checklist.steps.length
  return (
    <section aria-labelledby="gs-title" className={clsx('rounded-xl border border-nb-850 bg-nb-925', variant === 'hero' && 'w-full max-w-2xl')} data-testid="getting-started">
      <div className="flex items-start justify-between gap-3 border-b border-nb-850 px-5 py-4">
        <div>
          <h2 id="gs-title" className="text-sm font-medium text-white">Getting started</h2>
          <p className="mt-0.5 text-xs text-nb-500" data-testid="gs-progress">{checklist.done} of {total} done. Each step is checked against your server.</p>
        </div>
        {onDismiss && (
          <button onClick={onDismiss} aria-label="Dismiss getting started" title="Dismiss" className="-mr-1.5 -mt-1 rounded p-1.5 text-nb-500 hover:bg-nb-940 hover:text-nb-300" data-testid="gs-dismiss">
            <X size={16} aria-hidden />
          </button>
        )}
      </div>
      <ol className="divide-y divide-nb-850/70">
        {checklist.steps.map((s, i) => (
          <li key={s.id} className="flex flex-wrap items-start gap-x-3 gap-y-2 px-5 py-3.5" data-testid={`gs-step-${s.id}`} data-state={s.state} aria-current={s.state === 'current' ? 'step' : undefined}>
            <Marker step={s} n={i + 1} />
            <div className="min-w-0 flex-1 basis-56">
              <div className="flex flex-wrap items-center gap-2">
                <span className={clsx('text-sm font-medium', s.state === 'done' ? 'text-nb-400' : 'text-white')}>{s.title}</span>
                {s.optional && <Pill>optional</Pill>}
                <span className="sr-only">{STATE_WORD[s.state]}</span>
              </div>
              <p className="mt-0.5 text-sm text-nb-500">{s.line}</p>
            </div>
            <div className="ml-9 sm:ml-0">
              <Action step={s} onConnect={onConnect} />
            </div>
          </li>
        ))}
      </ol>
      {children}
    </section>
  )
}
