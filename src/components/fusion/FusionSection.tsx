import clsx from 'clsx'
import { type ReactNode, useId } from 'react'

/** One titled block of a FUSION tab: the heading and its one-line explanation in the Settings page's style, the block's actions on the right. */
export function FusionSection({ title, description, actions, children, testId }: { title: string; description?: ReactNode; actions?: ReactNode; children?: ReactNode; testId?: string }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="mb-8" data-testid={testId}>
      <div className={clsx('flex flex-wrap items-end justify-between gap-x-4 gap-y-2', children && 'mb-3')}>
        <div>
          <h2 id={id} className="text-sm font-medium text-nb-300">{title}</h2>
          {description && <p className="mt-1 max-w-2xl text-sm text-nb-500">{description}</p>}
        </div>
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  )
}

/** The rows of a FUSION block: a card whose lines are divided, stacking on a narrow screen. */
export const ROWS = 'divide-y divide-nb-850 rounded-xl border border-nb-850 bg-nb-925 text-sm'
