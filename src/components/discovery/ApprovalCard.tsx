import { ChevronRight, Copy, KeyRound, ShieldCheck, ShieldX } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button, ErrorBanner, Input, Pill } from '@/components/ui/primitives'
import TierLevels from '@/components/TierLevels'
import { api, ApiError } from '@/lib/api'
import { CODE_LOG_COMMAND, cleanCode, codeComplete, formatCode, fromPaste } from '@/lib/approvalCode'
import { ipScope, ipScopeLabel } from '@/lib/present'
import { ACCESS_TIERS, type AccessTier, type Agent } from '@/lib/types'
import { useApprovalLocks } from '@/store/approvalLocks'
import { useServer } from '@/store/server'

export const KUBECTL_UID = "kubectl get namespace kube-system -o jsonpath='{.metadata.uid}'"

export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}

export function CopyButton({ text, label = 'Copy' }: { text: string; label?: string }) {
  const [done, setDone] = useState(false)
  return (
    <Button
      size="sm"
      className="whitespace-nowrap"
      onClick={async () => {
        setDone(await copyText(text))
        setTimeout(() => setDone(false), 1500)
      }}
    >
      <Copy size={12} /> {done ? 'Copied' : label}
    </Button>
  )
}

/** "in 23 h", "in 40 min", for when a waiting request runs out. */
function expiresIn(iso?: string, now = Date.now()): string | undefined {
  if (!iso) return undefined
  const ms = new Date(iso).getTime() - now
  if (ms <= 0) return 'expiring now'
  return ms < 5_400_000 ? `in ${Math.max(1, Math.round(ms / 60_000))} min` : `in ${Math.round(ms / 3_600_000)} h`
}

/**
 * The human decision that lets an agent in. The agent prints a short approval code in its own log, which only
 * someone who can read that cluster's pod log can see; the person types it here. Approving the wrong request
 * (two clusters waiting at once, a look-alike name, a token that leaked) then fails, because the code belongs to
 * exactly one request. Five wrong codes reject the request.
 *
 * An older agent shows no code: for it, the person confirms the start of the cluster fingerprint instead, and
 * the card says so.
 */
export default function ApprovalCard({ agent, onDone }: { agent: Agent; onDone?: () => void }) {
  const conn = useServer((s) => s.conn)
  const refresh = useServer((s) => s.refresh)
  const locked = useApprovalLocks((s) => s.locked[agent.id])
  const lock = useApprovalLocks((s) => s.lock)
  const dismiss = useApprovalLocks((s) => s.dismiss)
  const max = Math.min(agent.installedTier ?? 2, agent.tierCap ?? 2, 2) as AccessTier
  const legacy = !!agent.legacyEnrollment
  const [tier, setTier] = useState<AccessTier>(max)
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [tried, setTried] = useState<number | undefined>(undefined) // attempts left, as the server last said
  const field = useRef<HTMLInputElement>(null)
  const left = Math.min(agent.approvalAttemptsLeft ?? 5, tried ?? 5)
  const ready = legacy ? input.trim().length >= 8 && agent.fingerprint.startsWith(input.trim()) : codeComplete(input)
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(t)
  }, [])

  const act = async (fn: (c: NonNullable<ReturnType<typeof conn>>) => Promise<void>) => {
    const c = conn()
    if (!c) return setError('Not connected to the server.')
    setBusy(true)
    setError('')
    try {
      await fn(c)
      await refresh()
      onDone?.()
    } catch (e) {
      if (e instanceof ApiError) {
        const l = e.body?.attemptsLeft
        // The attempts left are shown on their own line, so the trailing "(3 attempts left)" is not said twice.
        setError(typeof l === 'number' ? e.message.replace(/\s*\(\d+ attempts? left\)\.?$/, '') : e.message)
        if (typeof l === 'number') setTried(l)
        if (e.body?.locked === true) lock(agent.id)
        else field.current?.select()
      } else {
        setError('Something went wrong.')
      }
    } finally {
      setBusy(false)
    }
  }

  // The request was rejected after too many wrong codes. It is no longer pending, so say what happened.
  if (locked || agent.status === 'rejected') {
    return (
      <div className="rounded-xl border border-red-400/30 bg-red-400/5 px-5 py-4" data-testid="approval-locked" role="alert">
        <div className="flex items-center gap-2 text-sm font-medium text-white">
          <ShieldX size={16} className="text-red-300" /> The request from {agent.name} was rejected
        </div>
        <p className="mt-1 text-sm text-nb-400">
          {agent.reason || 'Too many wrong approval codes were typed.'} The agent has stopped for good. To connect this cluster, create a new install command with a new token and run it again.
        </p>
        <div className="mt-3 flex justify-end">
          <Button size="sm" onClick={() => dismiss(agent.id)}>Dismiss</Button>
        </div>
      </div>
    )
  }

  const expiry = expiresIn(agent.pendingExpiresAt, now)
  const errId = `approval-error-${agent.id}`
  const helpId = `approval-help-${agent.id}`
  const ipLabel = ipScopeLabel(ipScope(agent.connectingIp ?? ''))

  return (
    <div className="rounded-xl border border-amber-400/30 bg-amber-400/5 px-5 py-4" data-testid="approval-card">
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-2">
        <div className="min-w-0">
          <div className="flex items-center gap-2 text-sm font-medium text-white">
            <ShieldCheck size={16} className="shrink-0 text-amber-300" /> <span className="truncate">{agent.name}</span> wants to connect
          </div>
          <p className="mt-1 text-sm text-nb-500">
            An agent enrolled with a valid token. It gets a certificate and can start reporting only after you approve it.
          </p>
        </div>
        <div className="flex shrink-0 flex-wrap justify-end gap-1.5 text-xs text-nb-400">
          {agent.kubernetesVersion && <Pill>Kubernetes {agent.kubernetesVersion}</Pill>}
          {agent.connectingIp && <Pill>from {agent.connectingIp}{ipLabel ? ` · ${ipLabel.toLowerCase()}` : ''}</Pill>}
          <Pill>agent v{agent.version}</Pill>
          {expiry && <Pill>expires {expiry}</Pill>}
        </div>
      </div>

      <div className="mt-4 grid gap-5 md:grid-cols-2">
        <div>
          <div className="text-xs text-nb-500">Cluster fingerprint reported by the agent</div>
          <code className="mt-1 block break-all rounded-md border border-nb-850 bg-nb-950 px-3 py-2 font-mono text-xs text-nb-300" data-testid="approval-fingerprint">{agent.fingerprint}</code>
          <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-nb-500">
            <span>The UID of the cluster’s <code className="font-mono">kube-system</code> namespace:</span>
            <CopyButton text={KUBECTL_UID} label="Copy kubectl command" />
          </div>
          <details className="group mt-3" data-testid="approval-tier" open={tier !== max}>
            <summary className="flex cursor-pointer select-none items-center gap-1 text-xs text-nb-500 marker:content-none">
              <ChevronRight size={12} className="text-nb-500 transition-transform group-open:rotate-90" aria-hidden />
              Access: <span className="text-nb-300">{ACCESS_TIERS.find((t) => t.value === tier)?.label}</span> <span className="text-nb-600">(change)</span>
            </summary>
            <div className="mt-2">
              <TierLevels
                tiers={ACCESS_TIERS.map((t) => t.value).filter((v) => v <= max) as AccessTier[]}
                value={tier}
                onSelect={(t) => setTier(t)}
                size="sm"
                data-testid="approval-tier-pick"
              />
            </div>
          </details>
        </div>

        <div>
          {legacy ? (
            <div data-testid="legacy-approval">
              <div className="mb-1.5 flex items-center gap-2 text-sm font-medium text-nb-300">
                Legacy enrollment <span className="rounded-full border border-amber-400/40 bg-amber-400/10 px-2 py-px text-[11px] font-medium text-amber-300">no approval code</span>
              </div>
              <p className="mb-2 text-xs text-nb-500">
                This agent is an older version and shows no approval code, so the check is weaker: type the first 8 characters of the cluster’s fingerprint, which you read from the cluster itself with the command on the left. Update the agent to get approval codes.
              </p>
              <Input
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder={agent.fingerprint.slice(0, 8)}
                spellCheck={false}
                autoComplete="off"
                aria-label="Fingerprint confirmation"
                aria-invalid={!!error}
                aria-describedby={error ? errId : undefined}
                ref={field}
              />
            </div>
          ) : (
            <div>
              <label htmlFor={`approval-code-${agent.id}`} className="mb-1.5 flex items-center gap-1.5 text-sm font-medium text-nb-300">
                <KeyRound size={14} className="text-amber-300" aria-hidden /> Approval code
              </label>
              <Input
                id={`approval-code-${agent.id}`}
                ref={field}
                value={input}
                onChange={(e) => setInput(formatCode(e.target.value))}
                onPaste={(e) => {
                  e.preventDefault()
                  setInput(fromPaste(e.clipboardData.getData('text')))
                }}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && ready && !busy) act((c) => api.approve(c, agent.id, cleanCode(input), tier))
                }}
                placeholder="K7QM-4TXD"
                maxLength={9}
                spellCheck={false}
                autoComplete="off"
                autoCapitalize="characters"
                autoCorrect="off"
                inputMode="text"
                aria-label="Approval code"
                aria-invalid={!!error}
                aria-describedby={`${helpId}${error ? ` ${errId}` : ''}`}
                className="text-center font-mono text-lg uppercase tracking-[0.3em]"
                data-testid="approval-code"
              />
              <p id={helpId} className="mt-2 text-xs text-nb-500">
                The agent prints it in its own log, and only there. On the cluster, run
                <span className="mt-1.5 flex flex-wrap items-center gap-2">
                  <code className="break-all rounded border border-nb-850 bg-nb-950 px-2 py-1 font-mono text-[11px] text-nb-300">{CODE_LOG_COMMAND}</code>
                  <CopyButton text={CODE_LOG_COMMAND} label="Copy" />
                </span>
                <span className="mt-1.5 block">and look for “enrollment pending: approval code …”. Typing it proves you are looking at this cluster, not another one that is waiting too. Pasting the whole line works.</span>
              </p>
              {left < 5 && (
                <p className="mt-2 text-xs text-amber-300" data-testid="attempts-left">
                  {left} {left === 1 ? 'attempt' : 'attempts'} left. After that the request is rejected and the agent must be installed again with a new token.
                </p>
              )}
            </div>
          )}
        </div>
      </div>

      {error && (
        <ErrorBanner id={errId} className="mt-3" data-testid="approval-error">
          {error}
        </ErrorBanner>
      )}
      <div className="mt-4 flex justify-end gap-2">
        <Button variant="danger" disabled={busy} onClick={() => act((c) => api.reject(c, agent.id, 'rejected in the UI'))}>
          Reject
        </Button>
        <Button variant="primary" disabled={busy || !ready} onClick={() => act((c) => api.approve(c, agent.id, legacy ? input.trim() : cleanCode(input), tier))}>
          Approve
        </Button>
      </div>
    </div>
  )
}
