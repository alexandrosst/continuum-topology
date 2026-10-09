import clsx from 'clsx'
import { AlertTriangle, CheckCircle2, HelpCircle, XCircle, type LucideIcon } from 'lucide-react'
import { useEffect, useState } from 'react'
import { ICON_SM } from '@/components/ui/primitives'
import type { AgentDiagnostics } from '@/lib/consent'
import { arrivalCheck, HEALTH_WORD, type Health } from '@/lib/telemetrySetup'

const GLYPH: Record<Health, { icon: LucideIcon; tone: string }> = {
  healthy: { icon: CheckCircle2, tone: 'text-ok' },
  attention: { icon: AlertTriangle, tone: 'text-warn' },
  broken: { icon: XCircle, tone: 'text-bad' },
  unknown: { icon: HelpCircle, tone: 'text-nb-500' },
}

/** A state in the product's one vocabulary: glyph, word and colour, never colour alone. */
export function HealthWord({ health, testId }: { health: Health; testId?: string }) {
  const { icon: Icon, tone } = GLYPH[health]
  return (
    <span className={clsx('inline-flex shrink-0 items-center gap-1 text-xs font-medium', tone)} data-testid={testId} data-health={health}>
      <Icon size={ICON_SM} aria-hidden />
      {HEALTH_WORD[health]}
    </span>
  )
}

/**
 * "Check that data arrives": whether what was just set up reaches its destination, from what the agent reports (the same report that is
 * polled everywhere else, so this changes by itself when the first data goes out). Says what is wrong in one sentence and what to do about it.
 */
export default function ArrivalCheck({ wanted, diagnostics, connected, cluster, testId = 'arrival', passive = false, now: fixedNow }: { wanted: string[]; diagnostics?: AgentDiagnostics; connected?: boolean; cluster: string; testId?: string; /** Not right after a command: no first-data patience to run out, so "no data yet" is never called overdue. */ passive?: boolean; /** Pins the clock, for a test. */ now?: number }) {
  // How long this has been looked at decides when "no data yet" stops being normal; it also keeps "2 min ago" moving while the dialog stays open.
  const [since] = useState(() => Date.now())
  const [clock, setClock] = useState(() => Date.now())
  useEffect(() => {
    if (fixedNow !== undefined) return
    const t = setInterval(() => setClock(Date.now()), 15_000)
    return () => clearInterval(t)
  }, [fixedNow])
  const now = fixedNow ?? clock
  const a = arrivalCheck({ wanted, diagnostics, connected, waited: passive ? 0 : (now - since) / 1000, now, cluster })
  return (
    <div className="rounded-xl border border-nb-850 bg-nb-925" data-testid={testId} data-health={a.health}>
      <div className="flex flex-wrap items-start gap-x-3 gap-y-1 px-4 py-3">
        <HealthWord health={a.health} testId={`${testId}-state`} />
        <div className="min-w-0 flex-1 basis-60 text-sm text-nb-300" role="status">
          {a.headline}
          {a.todo && <p className="mt-1 text-xs text-nb-400" data-testid={`${testId}-todo`}><span className="font-medium text-nb-300">What to do: </span>{a.todo}</p>}
        </div>
      </div>
      {a.rows.length > 0 && (
        <ul className="divide-y divide-nb-850 border-t border-nb-850">
          {a.rows.map((r) => (
            <li key={r.modality} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-4 py-2.5 text-xs" data-testid={`${testId}-${r.modality}`} data-health={r.health}>
              <span className="w-16 shrink-0 text-sm text-nb-300">{r.label}</span>
              <HealthWord health={r.health} />
              <span className="shrink-0 text-nb-400">{r.lastData}</span>
              <span className="min-w-0 basis-60 text-nb-500">{r.sentence}</span>
            </li>
          ))}
        </ul>
      )}
      {a.partial && <p className="border-t border-nb-850 px-4 py-2 text-xs text-nb-500" data-testid={`${testId}-partial`}>{a.partial}</p>}
    </div>
  )
}
