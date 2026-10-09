import { ChevronRight } from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { ICON_SM } from '@/components/ui/primitives'

/**
 * A closed-by-default section inside a wizard step: the same box and chevron as "More options" in the connect wizard. `defaultOpen` is read
 * once, so a person's own click is never fought by a value that changes under it (an existing setting opens it on arrival, nothing else does).
 */
export default function Disclosure({ title, hint, defaultOpen = false, children, testId }: { title: string; hint?: string; defaultOpen?: boolean; children: ReactNode; testId?: string }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <details className="group rounded-lg border border-nb-850 bg-nb-925" open={open} onToggle={(e) => setOpen(e.currentTarget.open)} data-testid={testId}>
      <summary className="flex cursor-pointer select-none items-center gap-1.5 px-4 py-3 text-sm font-medium text-nb-300 marker:content-none">
        <ChevronRight size={ICON_SM} className="shrink-0 text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
        {title}
        {hint && <span className="font-normal text-nb-500">({hint})</span>}
      </summary>
      <div className="space-y-3 border-t border-nb-850 p-3">{children}</div>
    </details>
  )
}
