import clsx from 'clsx'
import { useEffect, useState } from 'react'
import { exportReach, exportRows, type ExportRow } from '@/lib/exportHealth'
import { uptimeWords, type AgentDiagnostics, type ExportState } from '@/lib/consent'
import { TONE_CLASS } from '@/lib/provenance'

const MODALITY_LABEL = { metrics: 'Metrics', logs: 'Logs', traces: 'Traces' } as const

const STATE_TONE: Record<ExportState, keyof typeof TONE_CLASS> = { waiting: 'muted', exporting: 'ok', silent: 'muted', failing: 'bad' }
const STATE_LABEL: Record<ExportState, string> = { waiting: 'Waiting for data', exporting: 'Sending', silent: 'Quiet', failing: 'Failing' }

const ago = (iso?: string, now = Date.now()) => (iso ? `${uptimeWords((now - Date.parse(iso)) / 1000)} ago` : undefined)

/** The sentence under a row's state: what the agent saw and, where it matters, what to make of it. */
function detail(r: ExportRow, now: number): string {
  const sent = ago(r.lastSentAt, now)
  const failed = ago(r.lastFailedAt, now)
  switch (r.state) {
    case 'exporting':
      return `The destination accepted data ${sent}.${r.failed > 0 ? ` ${r.failed.toLocaleString()} sends failed since the collectors started.` : ''}`
    case 'failing':
      return `${r.failed.toLocaleString()} sends failed (the last ${failed}) and none got through since. The destination is refusing the data or cannot be reached: check its address, protocol, credential and certificate.`
    case 'silent':
      return `${r.sent.toLocaleString()} sent since the collectors started, but nothing in the last few minutes${sent ? ` (the last ${sent})` : ''}. That is normal when nothing is producing ${MODALITY_LABEL[r.modality].toLowerCase()} right now.`
    default:
      return 'Nothing has gone out yet. It shows here as soon as the first data does, which can take a minute or two after the collectors start.'
  }
}

/**
 * Whether each signal type is actually reaching its destination, from what the agent read off the collectors'
 * own export counters. "Sending" means the destination accepted the data (the collector counts a send only
 * when the destination acknowledged it), not that it is visible in that backend's own UI.
 */
export default function ExportHealth({ installed, diagnostics, testIdPrefix = 'export-health', now: fixedNow }: { installed: string[]; diagnostics?: AgentDiagnostics; testIdPrefix?: string; now?: number }) {
  // "5 min ago" must keep moving while the page is open; a test pins it.
  const [clock, setClock] = useState(() => Date.now())
  useEffect(() => {
    if (fixedNow !== undefined) return
    const t = setInterval(() => setClock(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [fixedNow])
  const now = fixedNow ?? clock
  if (installed.length === 0) return null
  const reach = exportReach(diagnostics)
  const h = diagnostics?.exportHealth
  // Diagnostics are sent when something changes and at least every few minutes: much older than that and the
  // agent has stopped reporting, so what follows is what it last said, not what is true now.
  const staleFor = diagnostics ? (now - Date.parse(diagnostics.reportedAt)) / 1000 : 0
  return (
    <div className="mt-3" data-testid={testIdPrefix}>
      <div className="mb-1.5 text-sm font-medium text-nb-300">Is data arriving?</div>
      {h && staleFor > 15 * 60 && (
        <p className="mb-1.5 text-xs text-warn" data-testid={`${testIdPrefix}-stale`}>
          The agent last reported {uptimeWords(staleFor)} ago, so this is what it last saw.
        </p>
      )}
      {reach === 'not-reported' || !h ? (
        <p className="text-xs text-nb-500" data-testid={`${testIdPrefix}-none`}>
          {diagnostics ? 'This agent is not reporting export health: its install predates it, or telemetry.health is off.' : 'Not reported yet.'}
        </p>
      ) : reach === 'unreadable' ? (
        <p role="alert" className="text-xs text-nb-400" data-testid={`${testIdPrefix}-unreadable`}>
          The agent cannot read the collectors&apos; export counters{h.lastError ? ` (${h.lastError})` : ''}, so this cannot say whether data is arriving. A NetworkPolicy that stops the agent reaching the collectors, or collectors that are not running, are the usual causes.
        </p>
      ) : (
        <ul className="space-y-1.5">
          {exportRows(installed, h).map((r) => (
            <li key={r.modality} className="flex items-start gap-2 text-xs" data-testid={`${testIdPrefix}-${r.modality}`} data-state={r.state}>
              <span className={clsx('mt-px shrink-0 rounded border px-1.5 py-0.5 font-medium', TONE_CLASS[STATE_TONE[r.state]])} data-testid={`${testIdPrefix}-${r.modality}-state`}>
                {STATE_LABEL[r.state]}
              </span>
              <span className="min-w-0 text-nb-500">
                <span className="text-nb-300">{MODALITY_LABEL[r.modality]}</span>
                {r.exporters.length > 0 && <> via <span className="font-mono text-nb-400">{r.exporters.join(', ')}</span></>}. {detail(r, now)}
              </span>
            </li>
          ))}
          {h.podsFailed > 0 && (
            <li className="text-xs text-nb-500" data-testid={`${testIdPrefix}-partial`}>
              {h.podsFailed} of {h.podsReached + h.podsFailed} collector reads failed on the last attempt{h.lastError ? ` (${h.lastError})` : ''}; the rest still count.
            </li>
          )}
        </ul>
      )}
    </div>
  )
}
