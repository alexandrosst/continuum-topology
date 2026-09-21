/**
 * Where install commands pull the agent image and chart from. There is no built-in registry: an operator sets one
 * (Settings → Installation, or the server's --image-registry), or the chart's own image name applies. The checks
 * here mirror the server's (backend/internal/server/images.go), which has the final say; they exist so a typo is
 * caught while typing instead of after a round trip.
 */

export interface ImageSettings {
  registry: string
  tag: string
  digest: string
}

export const emptyImage: ImageSettings = { registry: '', tag: '', digest: '' }

/** The chart's own image name, used when no registry is configured. */
export const CHART_DEFAULT_REPOSITORY = 'continuum/continuum'

const HOST = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$/
const PART = /^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$/
const TAG = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$/
const DIGEST = /^sha256:[0-9a-f]{64}$/

/** Surrounding spaces and trailing slashes are dropped, as the server does. */
export const cleanRegistry = (v: string): string => v.trim().replace(/\/+$/, '')

/** What is wrong with a registry, in words a person can act on; '' when it is fine (or empty). */
export function registryProblem(raw: string): string {
  const v = cleanRegistry(raw)
  if (!v) return ''
  if (v.length > 255) return 'At most 255 characters.'
  if (v.includes('://')) return 'Write it without a scheme: registry.example.com/team, not https://registry.example.com/team.'
  if (v !== v.toLowerCase()) return 'Must be lowercase.'
  if (/[\s@?#,'"\\$`;&|<>(){}*~!]/.test(v)) return 'May not contain spaces or the characters @ ? # , and shell symbols.'
  const parts = v.split('/')
  if (!HOST.test(parts[0])) return `"${parts[0]}" is not a registry host (registry.example.com, registry.example.com:5000, or a Docker Hub name).`
  for (const p of parts.slice(1)) if (!PART.test(p)) return `"${p}" is not a valid path segment: lowercase letters and digits, with single . _ - between them.`
  return ''
}

export function tagProblem(raw: string): string {
  const v = raw.trim()
  if (!v || TAG.test(v)) return ''
  return 'Letters, digits, _ . - only, up to 128 characters, and it cannot start with . or -.'
}

export function digestProblem(raw: string): string {
  const v = raw.trim()
  if (!v || DIGEST.test(v)) return ''
  return 'Must be sha256: followed by 64 lowercase hex digits.'
}

export interface ImageProblems {
  registry: string
  tag: string
  digest: string
}

/** Per-field problems; every value '' means the settings can be saved. A tag or digest needs a registry. */
export function imageProblems(s: ImageSettings): ImageProblems {
  const p: ImageProblems = { registry: registryProblem(s.registry), tag: tagProblem(s.tag), digest: digestProblem(s.digest) }
  if (!cleanRegistry(s.registry)) {
    if (!p.tag && s.tag.trim()) p.tag = 'Set a registry first: a tag names an image inside it.'
    if (!p.digest && s.digest.trim()) p.digest = 'Set a registry first: a digest names an image inside it.'
  }
  return p
}

export const imageOk = (p: ImageProblems): boolean => !p.registry && !p.tag && !p.digest

/** Where an OCI registry keeps things under a registry setting; a bare name is a Docker Hub namespace. Mirrors OCIBase in Go. */
export function ociBase(registry: string): string {
  const r = registry.trim().replace(/^\/+|\/+$/g, '')
  for (const p of ['docker.io/', 'index.docker.io/', 'registry-1.docker.io/']) if (r.startsWith(p)) return 'registry-1.docker.io/' + r.slice(p.length)
  const first = r.split('/')[0]
  if (/[.:]/.test(first) || first === 'localhost') return r
  return 'registry-1.docker.io/' + r
}

export interface ImagePreview {
  /** False: nothing is set anywhere, and the chart's own image name and the served chart file apply. */
  configured: boolean
  /** Whether the values come from the server's flags instead of the organisation's own setting. */
  fromServer: boolean
  /** The image repository the install command sets (or the chart's default). */
  repository: string
  /** The tag the image is pulled by, when one is set. */
  tag: string
  /** The digest the image is pinned to, when one is set; the tag is then ignored. */
  digest: string
  /** repository@digest, or repository:tag, or the bare repository (the chart's own appVersion tag). */
  reference: string
  /** The digest form, when pinned. */
  pinnedReference: string
  /** oci://…/continuum-agent, or '' when the chart is the file this server serves. */
  chartRef: string
}

/** What an install command will use: the organisation's own values when it has a registry, else the server's defaults. */
export function previewImage(own: ImageSettings, serverDefaults: ImageSettings = emptyImage): ImagePreview {
  const useOwn = !!cleanRegistry(own.registry)
  const s = useOwn ? { registry: cleanRegistry(own.registry), tag: own.tag.trim(), digest: own.digest.trim() } : { registry: cleanRegistry(serverDefaults.registry), tag: serverDefaults.tag.trim(), digest: serverDefaults.digest.trim() }
  const configured = !!s.registry
  const repository = configured ? `${s.registry}/continuum` : CHART_DEFAULT_REPOSITORY
  const pinnedReference = s.digest ? `${repository}@${s.digest}` : ''
  return {
    configured,
    fromServer: configured && !useOwn,
    repository,
    tag: s.tag,
    digest: s.digest,
    reference: pinnedReference || (s.tag ? `${repository}:${s.tag}` : repository),
    pinnedReference,
    chartRef: configured ? `oci://${ociBase(s.registry)}/continuum-agent` : '',
  }
}
