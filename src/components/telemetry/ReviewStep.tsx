import type { ReactNode } from 'react'
import type { ReviewNotes } from '@/lib/telemetrySetup'

/** One block of the review: a heading and short sentences. */
function Block({ title, items, testId }: { title: string; items: string[]; testId: string }) {
  return (
    <section className="px-4 py-3" data-testid={testId}>
      <h4 className="text-sm font-medium text-nb-200">{title}</h4>
      <ul className="mt-1.5 space-y-1 text-sm text-nb-300">
        {items.map((t) => (
          <li key={t} className="flex gap-2"><span className="mt-2 size-1 shrink-0 rounded-full bg-nb-600" aria-hidden />{t}</li>
        ))}
      </ul>
    </section>
  )
}

/**
 * The review in plain words, before any command is shown: what changes in the cluster, what does not, and how to stop. `children` are what stops
 * the command from being made (a destination that cannot receive, none chosen yet), each with its way forward.
 */
export default function ReviewStep({ notes, cluster, children, testIdPrefix }: { notes: ReviewNotes; cluster: string; children?: ReactNode; testIdPrefix: string }) {
  return (
    <div className="space-y-3" data-testid={`${testIdPrefix}-guided-step-review`}>
      <div className="divide-y divide-nb-850 rounded-xl border border-nb-850 bg-nb-925">
        <Block title={`What changes in ${cluster}`} items={notes.changes} testId={`${testIdPrefix}-review-changes`} />
        <Block title="What does not change" items={notes.unchanged} testId={`${testIdPrefix}-review-unchanged`} />
        <Block title="If you stop" items={notes.stop} testId={`${testIdPrefix}-review-stop`} />
      </div>
      {children}
    </div>
  )
}
