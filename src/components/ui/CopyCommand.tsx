import clsx from 'clsx'
import { Check, Copy } from 'lucide-react'
import { useRef } from 'react'
import { ICON_MD, MANUAL_COPY_HINT, useCopy } from '@/components/ui/primitives'
import { useServer } from '@/store/server'

/** A one-line command with a copy button. Shared wherever the app hands someone an exact command to run: the
 *  widen hint here, the harden and teardown commands on the Agents page. */
export function CopyCommand({ text, stale = false, multiline = false, testId = 'helm-command', label = 'Copy the command' }: { text: string; /** What the copy button is called for assistive technology: say which command, where several sit together. */ label?: string; /** Out of date: shown dimmed and not copyable, rather than hand out a command that is wrong now. */ stale?: boolean; /** Keep the command's own line breaks on screen (a certificate's PEM would otherwise run together). */ multiline?: boolean; testId?: string }) {
  const { state, copy } = useCopy()
  const code = useRef<HTMLElement>(null)
  // A command with line breaks of its own (a Secret applied from a here-document, a helm command continued with backslashes) keeps them:
  // run together they are not something a person can read before pasting. A long one scrolls inside its box instead of stretching the dialog.
  return (
    <div className={clsx('mt-1 flex items-start gap-2 rounded border border-nb-850 bg-nb-950 px-2 py-1.5', stale && 'opacity-50')} data-stale={stale || undefined}>
      <code ref={code} className={clsx('min-w-0 flex-1 break-words font-mono text-[11px] text-nb-300', (multiline || text.includes('\n')) && 'max-h-72 overflow-y-auto whitespace-pre-wrap')} data-testid={testId}>{text}</code>
      <button
        type="button"
        className="shrink-0 rounded p-1 text-nb-500 hover:bg-nb-930 hover:text-nb-300 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-nb-500"
        aria-label={label}
        disabled={stale}
        // Where the browser will not copy (no Clipboard API on a plain-http page), the command is left selected and the person told to press Ctrl+C.
        // What is run next changes the cluster (a collector, telemetry): the state is read faster for a while, so the result shows up sooner.
        onClick={() => {
          void copy(text, code.current)
          void useServer.getState().boostPolling()
        }}
      >
        {state === 'copied' ? <Check size={ICON_MD} aria-hidden /> : <Copy size={ICON_MD} aria-hidden />}
      </button>
      {state === 'manual' && <span role="status" className="max-w-[7rem] shrink-0 text-[11px] leading-tight text-warn" data-testid="copy-manual-hint">{MANUAL_COPY_HINT}</span>}
    </div>
  )
}
