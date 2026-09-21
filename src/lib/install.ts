/**
 * The install command the server prints, with optional in-cluster parts switched on.
 * They are separate opt-ins because they are the only parts of Continuum that run on every node.
 */
function withFlag(install: string, on: boolean, key: string): string {
  if (!on || install.includes(`${key}=true`)) return install
  return `${install.trimEnd()} \\\n  --set ${key}=true`
}

export const withNodeProbe = (install: string, on: boolean): string => withFlag(install, on, 'nodeProbe.enabled')

/** The traffic observer: counts which workloads talk to which. Needs the agent's highest access tier. */
export const withFlowObserver = (install: string, on: boolean): string => withFlag(install, on, 'flowObserver.enabled')

/** Path measurements: the agent may time TCP connections to addresses the server names (no data is sent). */
export const withMeasurements = (install: string, on: boolean): string => withFlag(install, on, 'measurements.enabled')

/* ---------- Which namespaces the agent may report ---------- */

export interface ScopeInput {
  /** Only these namespaces (plus system ones, which are read for detection but never shown). Empty: all. */
  namespaces: string[]
  /** Never these. Wins over everything else. */
  exclude: string[]
  /** Or the namespaces carrying this label, as `key=value` (or just `key`). Added to `namespaces`. */
  selector: string
}

export const emptyScope: ScopeInput = { namespaces: [], exclude: [], selector: '' }

/** "shop, payments  ops" → ["shop","payments","ops"]: split on commas, spaces and newlines, dropping empties and repeats. */
export const splitNames = (text: string): string[] => [...new Set(text.split(/[\s,]+/).map((s) => s.trim()).filter(Boolean))]

const DNS_LABEL = /^[a-z0-9]([-a-z0-9]{0,61}[a-z0-9])?$/
/** A Kubernetes label key: optional DNS-subdomain prefix, then a name of at most 63 characters. */
const LABEL_KEY = /^([a-z0-9]([-a-z0-9.]{0,251}[a-z0-9])?\/)?[A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?$/
/** The agent only reads labels on its allow-list, so it refuses a selector on any other key rather than silently matching nothing. */
const SELECTABLE_EXACT = ['app', 'k8s-app', 'istio-injection', 'istio.io/rev', 'istio.io/dataplane-mode']
const SELECTABLE_PREFIX = ['continuum.io/', 'app.kubernetes.io/', 'topology.kubernetes.io/']
export const selectableKey = (k: string) => SELECTABLE_EXACT.includes(k) || SELECTABLE_PREFIX.some((p) => k.startsWith(p))

const LABEL_VALUE = /^([A-Za-z0-9]([-A-Za-z0-9_.]{0,61}[A-Za-z0-9])?)?$/

/** What is wrong with the scope, in words a person can act on; empty when it is fine. */
export function scopeProblems(s: ScopeInput): string[] {
  const out: string[] = []
  for (const n of [...s.namespaces, ...s.exclude]) if (!DNS_LABEL.test(n)) out.push(`"${n}" is not a valid namespace name`)
  if (s.selector.trim()) {
    const [k, ...rest] = s.selector.trim().split('=')
    if (!LABEL_KEY.test(k)) out.push(`"${k}" is not a valid label key`)
    else if (!selectableKey(k)) out.push(`The agent only reads a fixed list of labels, so select on one starting with continuum.io/ (for example continuum.io/scope=yes), not "${k}"`)
    else if (rest.length > 1 || !LABEL_VALUE.test(rest[0] ?? '')) out.push('Write the label as key=value')
  }
  return out
}

export const scopeActive = (s: ScopeInput) => s.namespaces.length > 0 || s.exclude.length > 0 || !!s.selector.trim()

/** Helm treats commas in a --set value as list separators: escape them. */
const helmList = (xs: string[]) => `'{${xs.join(',')}}'`

/** Adds the scope to the install command. Without one the agent reports every namespace, as before. */
export function withScope(install: string, s: ScopeInput): string {
  if (!scopeActive(s) || scopeProblems(s).length) return install
  let cmd = install.trimEnd()
  if (s.namespaces.length) cmd += ` \\\n  --set scope.namespaces=${helmList(s.namespaces)}`
  if (s.exclude.length) cmd += ` \\\n  --set scope.exclude=${helmList(s.exclude)}`
  if (s.selector.trim()) cmd += ` \\\n  --set-string scope.selector='${s.selector.trim()}'`
  return cmd
}
