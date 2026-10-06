import { CopyCommand } from '@/components/agents/AgentInsight'

/** What health reporting sends, in one sentence both the create form, the confirmation and the created
 *  screen can lean on: not a telemetry payload, only an availability check, and only when opted in. */
export const HEARTBEAT_WHAT = "an availability check: the collector's own health result, once a minute, sent to this server's health endpoint. It carries no telemetry - nothing you relay, no logs, no traces, no cluster data."

/** The commands that put a heartbeat credential to use, in the order to run them: the Secret first (the
 *  chart reads it), then the helm upgrade that turns the heartbeat on, then - after a rotation - the restart
 *  that makes the collector pick the new value up. Shown once: the credential is never retrievable again. */
export function HeartbeatCommands({ secretCommand, upgradeCommand, restartCommand, caSecretCommand, warning, url, intervalSeconds, rotated, embedded = false, testId }: { embedded?: boolean; /** The Secret holding this server's certificate authority, when the heartbeat address uses a private one. */ caSecretCommand?: string; secretCommand: string; upgradeCommand?: string; restartCommand?: string; warning?: string; url: string; intervalSeconds: number; rotated?: boolean; testId: string }) {
  return (
    <div className="space-y-3" data-testid={testId}>
      {/* Inside a numbered step the page already says it once and the step is named for it: neither is repeated. */}
      {!embedded && (
        <p className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid={`${testId}-once`}>
          The health credential is shown only now - the server keeps only a hash of it. If it is lost, rotate it to get a new one.
        </p>
      )}
      {warning && <p role="alert" className="rounded-md border border-warn/30 bg-warn/10 px-3 py-2 text-xs leading-relaxed text-warn" data-testid={`${testId}-warning`}>{warning}</p>}
      <p className="text-xs text-nb-500">
        The operator will report to <code className="break-all font-mono text-nb-400">{url}</code> every {intervalSeconds} seconds.
      </p>
      <div>
        {!embedded && <div className="mb-1 text-xs text-nb-500">{rotated ? 'Replace the health credential Secret' : 'Create the health credential Secret first'}</div>}
        <CopyCommand text={secretCommand} testId={`${testId}-secret`} label="Copy the health credential Secret command" />
      </div>
      {caSecretCommand && (
        <div>
          <div className="mb-1 text-xs text-nb-500">The Secret that lets the operator trust this server&apos;s certificate</div>
          <CopyCommand text={caSecretCommand} testId={`${testId}-ca`} label="Copy the certificate authority Secret command" />
        </div>
      )}
      {upgradeCommand && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Then turn health reporting on for the running release</div>
          <CopyCommand text={upgradeCommand} testId={`${testId}-upgrade`} label="Copy the health reporting upgrade command" />
        </div>
      )}
      {restartCommand && (
        <div>
          <div className="mb-1 text-xs text-nb-500">Then restart the collector so it presents the new credential (until then it logs a 401 on every attempt)</div>
          <CopyCommand text={restartCommand} testId={`${testId}-restart`} label="Copy the collector restart command" />
        </div>
      )}
    </div>
  )
}
