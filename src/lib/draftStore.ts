import { emptyTelemetry, type TelemetryInput } from './install'

/**
 * An unfinished telemetry draft, kept for the length of the browser session so that nothing chosen in the wizard is lost to a
 * reload, a click on a link or a route change. It is a safety net, not the way work is kept: dialogs opened over the wizard (a new
 * operator, an address) leave it where it is. Everything here may fail - storage can be blocked or full - and then simply does
 * nothing: the wizard works the same without it.
 *
 * `basis` says what the draft was started from (the signals the agent reported running): a draft made against an install that has
 * since changed is not offered back, because what it would "keep as installed" is no longer true.
 */
const KEY = (agentId: string) => `ikhnos.telemetry-draft.${agentId}`

export function saveDraft(agentId: string, basis: string, draft: TelemetryInput): void {
  try {
    sessionStorage.setItem(KEY(agentId), JSON.stringify({ basis, draft }))
  } catch {
    // blocked or full: the draft just is not kept
  }
}

export function loadDraft(agentId: string, basis: string): TelemetryInput | null {
  try {
    const raw = sessionStorage.getItem(KEY(agentId))
    if (!raw) return null
    const parsed = JSON.parse(raw) as { basis?: string; draft?: Partial<TelemetryInput> }
    if (parsed.basis !== basis || !parsed.draft || typeof parsed.draft !== 'object') return null
    // Over the empty draft, so a field added since it was saved has a value instead of undefined.
    return { ...emptyTelemetry, ...parsed.draft }
  } catch {
    return null
  }
}

export function clearDraft(agentId: string): void {
  try {
    sessionStorage.removeItem(KEY(agentId))
  } catch {
    // nothing to clear
  }
}
