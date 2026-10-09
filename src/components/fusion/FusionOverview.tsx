import clsx from 'clsx'
import { CircleCheck } from 'lucide-react'
import { Link } from 'react-router-dom'
import { type useFusion } from '@/components/fusion/useFusion'
import { FusionSection, ROWS } from '@/components/fusion/FusionSection'
import { StatusChip } from '@/components/fusion/StatusChip'
import { DiskBar, FillsIn } from '@/components/fusion/StoreBars'
import { buttonClass } from '@/components/ui/buttonClass'
import { Button, CopyButton, ICON_MD, Waiting } from '@/components/ui/primitives'
import { type FusionComponent, type FusionRetention } from '@/lib/api'
import { fusionSentence, type FusionProblem, partHealth } from '@/lib/fusionStatus'

const PART_NOTE: Record<FusionComponent['component'], string> = {
  central: 'gateway into FUSION: the one door in',
  metrics: 'metrics',
  logs: 'logs',
  traces: 'traces',
  grafana: 'dashboards, already connected to the three stores',
}

/** The one thing to do about a problem, as the kind of control it is. */
function Action({ problem, onRefresh }: { problem: FusionProblem; onRefresh: () => void }) {
  const a = problem.action
  if (a.kind === 'link') return <Link className={buttonClass('secondary', 'sm')} to={a.to}>{a.label}</Link>
  if (a.kind === 'copy') return <CopyButton text={a.text} label={a.label} />
  return <Button size="sm" onClick={onRefresh}>{a.label}</Button>
}

/** What needs attention first, then what FUSION is made of. Problems are derived from what the server reports (see fusionProblems); each one
 *  says what is wrong in a sentence and has one thing to do about it. */
export function FusionOverview({ fusion, problems, retention, now }: { fusion: ReturnType<typeof useFusion>; problems: FusionProblem[]; retention: FusionRetention | null; now: number }) {
  const status = fusion.status!
  const sentence = fusionSentence(status, now)
  const parts = status.components ?? []
  const store = (c: FusionComponent) => retention?.available ? retention.stores.find((s) => s.component === c.component) : undefined
  return (
    <>
      <FusionSection title="What needs attention" testId="fusion-attention">
        {sentence.kind === 'starting' ? (
          <div className="rounded-xl border border-nb-850 bg-nb-925 px-5 py-4 text-sm text-nb-400" data-testid="fusion-status">
            {/* The one spinner of the page: the parts below show a still glyph. */}
            <Waiting testId="fusion-waiting"><span>{sentence.text}</span></Waiting>
            <p className="mt-1 text-xs text-nb-500">Usually under two minutes - you can leave this page; collectors keep buffering and catch up.</p>
          </div>
        ) : problems.length === 0 ? (
          <div className="flex items-center gap-2 rounded-xl border border-nb-850 bg-nb-925 px-5 py-4 text-sm text-nb-400" data-testid="fusion-fine">
            <CircleCheck size={ICON_MD} className="shrink-0 text-ok" aria-hidden /> Nothing needs your attention.
          </div>
        ) : (
          <ul className="space-y-3">
            {problems.map((p) => (
              <li key={p.id} className={clsx('rounded-xl border px-5 py-4', p.health === 'broken' ? 'border-bad/30 bg-bad/5' : 'border-warn/30 bg-warn/5')} data-testid={`fusion-problem-${p.id}`} data-health={p.health}>
                <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2 text-sm font-medium text-nb-300">
                      <StatusChip status={p.health} /> {p.title}
                    </div>
                    <p className="mt-1.5 max-w-2xl text-sm text-nb-500">{p.detail}</p>
                  </div>
                  <Action problem={p} onRefresh={() => void fusion.refresh()} />
                </div>
              </li>
            ))}
          </ul>
        )}
      </FusionSection>

      {parts.length > 0 && (
        <FusionSection title="Stores and central operator" description="Metrics, logs and traces each have a store; every sender goes through the central operator." testId="fusion-stores">
          <ul className={ROWS} data-testid="fusion-parts">
            {parts.map((c) => {
              const s = store(c)
              return (
                <li key={c.component} className="flex flex-wrap items-center gap-x-3 gap-y-0.5 px-4 py-3">
                  <span className="flex-1 text-nb-300 sm:w-36 sm:flex-none">{c.label}</span>
                  <span className="order-3 w-full min-w-0 text-nb-500 sm:order-none sm:w-auto sm:flex-1">
                    {PART_NOTE[c.component]}
                    {c.reason && c.ready < c.desired && <span className="ml-2 text-nb-400" data-testid={`fusion-reason-${c.component}`}>{c.reason}</span>}
                  </span>
                  {s && <span className="order-3 flex w-full flex-wrap items-center gap-x-4 gap-y-1 sm:order-none sm:w-auto"><FillsIn s={s} /><DiskBar s={s} /></span>}
                  <span className="ml-auto sm:ml-0 sm:flex sm:w-32 sm:justify-end"><StatusChip status={partHealth(c, sentence.kind, problems)} data-testid={`fusion-part-${c.component}`} /></span>
                </li>
              )
            })}
          </ul>
          {status.central && (
            <p className="mt-3 max-w-3xl text-xs leading-relaxed text-nb-500" data-testid="fusion-exposure">
              {status.central.exposed ? (
                <>The central operator is reachable from other clusters at <code className="font-mono text-nb-400">{status.central.endpoint}</code>. Anything that sends needs a client certificate from it; the three stores are never exposed.</>
              ) : (
                <>The central operator is reachable inside this cluster only (<code className="font-mono text-nb-400">{status.central.endpoint}</code>), so a regional operator in another cluster cannot send to it yet. To allow that, expose the central operator and record its address: <span className="text-nb-400">Address</span> on its row in Pipeline.</>
              )}
            </p>
          )}
        </FusionSection>
      )}
    </>
  )
}
