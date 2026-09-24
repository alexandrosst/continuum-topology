import { normalizeServerState, type ServerState } from './discovered'
import { normalizeSettings, normalizeSnapshot, type AppSettings, type ChangeEvent, type HistoryIndex, type Snapshot, type TrafficRate } from './history'
import type { EffectiveModel } from './provenance'

export class ApiError extends Error {
  status: number
  /** The parsed JSON body of an error response, when there was one (a 409 carries the current revision). */
  body?: Record<string, unknown>
  constructor(status: number, message: string, body?: Record<string, unknown>) {
    super(message)
    this.status = status
    this.body = body
  }
}

/** True when `login` stopped short of a session because the account also needs a two-factor code: `pending`
 *  is what `login2FA` must be called with next, `methods` is which of them apply (`totp` covers an
 *  authenticator app and recovery codes together, since login2FA takes either back in the same field). */
export function twoFactorPending(err: unknown): err is ApiError & { body: { pending: string; methods: TwoFactorMethod[] } } {
  return err instanceof ApiError && typeof err.body?.pending === 'string'
}

export type TwoFactorMethod = 'totp' | 'email' | 'webauthn'

export interface ServerInfo {
  orgId: string
  orgName: string
  role: Role
  agentAddress: string
  /** How agentAddress is exposed (loadbalancer, nodeport, gateway, clusterip), or empty on an older install. See lib/exposure. */
  agentExposure: string
  /** This server's own Helm release name and namespace, when it knows them, for an exact (not fill-in-the-blank)
   *  upgrade command in Settings → Server address. Empty on an older install. */
  releaseName: string
  releaseNamespace: string
  caPin: string
  version: string
  /** Highest access tier this server release will grant. */
  implementedTier: number
  /**
   * What the install command needs. `chartRef` is the registry chart it pulls (empty when it names the file this
   * server hands out, `chartFile`). `imageRegistry`, `imageTag` and `imageDigest` are where the one agent image comes
   * from, as the organisation's Settings → Installation (or else the server's flags) say; with no registry
   * (`imagesConfigured` false) the chart's own image name applies and nothing here is published for you. A digest
   * pins the image; a tag alone is mutable.
   */
  install?: { chartFile: string; chartRef: string; chartVersion: string; imagesConfigured: boolean; imageRegistry: string; imageTag: string; imageDigest: string }
  /**
   * Whether this server can suggest a location from an agent's connecting address. Off by default: the operator
   * has to bring their own offline database (see Settings). Cloud region codes and city names typed into labels
   * still work as placement signals either way; this only covers the IP-based one.
   */
  geoip?: { enabled: boolean; database?: string; description?: string; builtAt?: string; attribution?: string; publicIpFallback?: boolean; asn?: boolean }
}

export interface CreatedToken {
  token: string
  install: string
  meta: { id: string; name: string; tier: number; createdAt: string; expiresAt: string }
}

/** What a person may do inside one organisation, weakest first. */
export type Role = 'viewer' | 'editor' | 'admin' | 'owner'
export const ROLES: Role[] = ['viewer', 'editor', 'admin', 'owner']
export const ROLE_LABEL: Record<Role, string> = { viewer: 'Viewer', editor: 'Editor', admin: 'Administrator', owner: 'Owner' }
export const ROLE_HELP: Record<Role, string> = {
  viewer: 'Can look at everything in the organisation, and change nothing.',
  editor: 'Can also edit the shared topology: manual records, overrides, decisions.',
  admin: 'Can also connect clusters, approve agents, change settings, and manage editors and viewers.',
  owner: 'Can do everything, including managing owners and administrators and deleting the organisation.',
}
/** Does `have` reach at least `need`? An undefined role reaches nothing. */
export const atLeast = (have: Role | undefined, need: Role) => !!have && ROLES.indexOf(have) >= ROLES.indexOf(need)

/** Which roles `by` may give to or take from another person. */
export const grantable = (by: Role | undefined): Role[] => (by === 'owner' ? ROLES : by === 'admin' ? ['viewer', 'editor'] : [])

/** One passkey or security key registered to an account. id is what rename/remove address it by; it never
 *  carries anything that could be used to sign in with it. */
export interface Passkey {
  id: string
  name: string
  createdAt: string
  lastUsedAt?: string
}

export interface User {
  id: string
  username: string
  mustChangePassword: boolean
  createdAt: string
  lastLogin?: string
  twoFactorEnabled: boolean
  /** Shown even while unverified, so Settings can say "verifying jane@example.com...". Absent until set. */
  email?: string
  emailVerified: boolean
  emailOtpEnabled: boolean
  /** Whether this server can send mail at all - when false, email-OTP isn't offered regardless of the above. */
  mailConfigured: boolean
  /** Every passkey/security key on the account, oldest first. Unlike the other two methods there is no
   *  separate enabled flag: having at least one of these is what turns "webauthn" on as a sign-in method. */
  passkeys: Passkey[]
}

export interface OrgRef {
  id: string
  name: string
  role: Role
}

export interface Session {
  user: User
  orgs: OrgRef[]
}

export type Registration = 'open' | 'invite' | 'closed'

export interface Member {
  id: string
  username: string
  role: Role
  joinedAt: string
  lastLogin?: string
  you?: boolean
}

export interface Invite {
  id: string
  role: Role
  label?: string
  createdBy: string
  createdAt: string
  expiresAt: string
  used: boolean
  usedBy?: string
  expired?: boolean
}

export interface InvitePreview {
  organisation: string
  role: Role
  expiresAt: string
}

export interface WorkspaceMeta {
  rev: number
  updatedAt: string
  updatedBy: string
  /** What the server changed in the document, when it did (observed records removed from an older workspace). */
  note?: string
  /** The newest document format this server reads and writes. */
  formatVersion?: number
}

export interface WorkspaceDoc extends WorkspaceMeta {
  /** Absent when nothing was ever saved (rev 0). */
  data?: unknown
}

/** Where history is kept and whether that place is healthy. Only administrators receive `error` and `stats`. */
export interface StorageInfo {
  backend: 'sqlite' | 'neo4j'
  enabled: boolean
  connected?: boolean
  ready?: boolean
  buffering?: boolean
  error?: string
  stats?: { snapshots: number; versions: number; entities: number; events: number; audit: number; oldest?: string; newest?: string }
}

export interface TimelineChange {
  field: string
  from?: unknown
  to?: unknown
}

/** One period during which a record looked a certain way. `to` is absent while it is current. */
export interface TimelineVersion {
  from: string
  to?: string
  name: string
  status?: string
  changes: TimelineChange[]
}

export interface AuditRow {
  id: number
  at: string
  actor: string
  action: string
  targetKind?: string
  targetId?: string
  detail?: string
}

export interface Timeline {
  kind: string
  id: string
  versions: TimelineVersion[]
  events: ChangeEvent[]
  audit: AuditRow[]
}

export interface WorkspaceRevision {
  rev: number
  at: string
  by: string
  bytes: number
}

/** Where the server lives. An empty url means the address this page was loaded from. The session is a cookie, so there is no token here. */
export interface Conn {
  url: string
  /** The organisation the call is about. Everything except accounts and invitations belongs to one. */
  org?: string
}

/** Routes that are about the person or the server, not about one organisation. */
const GLOBAL = /^\/api\/v1\/(auth\/|server$|orgs$|invites\/(preview|accept)$)/

export function scoped(c: Conn, path: string): string {
  if (GLOBAL.test(path) || path.startsWith('/api/v1/orgs/')) return path
  if (!c.org) throw new ApiError(0, 'No organisation is selected.')
  return path.replace('/api/v1/', `/api/v1/orgs/${encodeURIComponent(c.org)}/`)
}

async function call<T>(c: Conn, method: string, path: string, body?: unknown, headers: Record<string, string> = {}): Promise<T> {
  let res: Response
  try {
    res = await fetch(`${c.url.replace(/\/$/, '')}${scoped(c, path)}`, {
      method,
      credentials: 'include',
      headers: {
        'X-Requested-With': 'continuum-ui',
        ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...headers,
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    })
  } catch {
    throw new ApiError(0, `Cannot reach the server at ${c.url || 'this address'}. Check the address and that it is running.`)
  }
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new ApiError(res.status, (data as { error?: string }).error ?? `Request failed (${res.status})`, data as Record<string, unknown>)
  return data as T
}

export const api = {
  // session and accounts
  serverInfo: (c: Conn) => call<{ registration: Registration; version: string }>(c, 'GET', '/api/v1/server'),
  me: (c: Conn) => call<Session>(c, 'GET', '/api/v1/auth/me'),
  login: (c: Conn, username: string, password: string) => call<Session>(c, 'POST', '/api/v1/auth/login', { username, password }),
  // Called after `login` throws with `twoFactorPending(err)` true, with the code from an authenticator app
  // (or one of the account's recovery codes) and the `pending` token that error carried.
  login2FA: (c: Conn, pending: string, code: string) => call<Session>(c, 'POST', '/api/v1/auth/login/2fa', { pending, code }),
  // Mails a fresh code for a pending sign-in that offers 'email' as a method; the code comes back through
  // login2FA's `code` field exactly like a TOTP or recovery code.
  requestLoginEmailCode: (c: Conn, pending: string) => call<void>(c, 'POST', '/api/v1/auth/login/2fa/email', { pending }),
  register: (c: Conn, username: string, password: string, opts: { org?: string; invite?: string } = {}) =>
    call<Session>(c, 'POST', '/api/v1/auth/register', { username, password, ...(opts.org ? { org: opts.org } : {}), ...(opts.invite ? { invite: opts.invite } : {}) }),
  logout: (c: Conn) => call<void>(c, 'POST', '/api/v1/auth/logout'),
  changePassword: (c: Conn, current: string, next: string) => call<Session>(c, 'POST', '/api/v1/auth/password', { current, new: next }),
  // Two-factor authentication (TOTP): setup2FA hands back a fresh secret (and its otpauth:// URI, for a
  // copyable key rather than a QR code) that isn't active until enable2FA confirms one code from it - at
  // which point it returns this account's one-time recovery codes. disable2FA needs the current password.
  setup2FA: (c: Conn) => call<{ secret: string; otpauthUrl: string }>(c, 'POST', '/api/v1/auth/2fa/setup'),
  enable2FA: (c: Conn, code: string) => call<{ recoveryCodes: string[] }>(c, 'POST', '/api/v1/auth/2fa/enable', { code }),
  disable2FA: (c: Conn, password: string) => call<Session>(c, 'POST', '/api/v1/auth/2fa/disable', { password }),
  // Email as a second factor: requestEmailVerification mails a code to a (possibly new) address and shows it
  // right away, unverified; confirmEmail proves the account can read it. Changing the address this way resets
  // verification and turns email-OTP back off (enforced server-side), so re-verifying is always required after
  // a change. enableEmailOTP needs no code of its own since verification already proved deliverability;
  // disableEmailOTP needs the current password and leaves the address itself verified.
  requestEmailVerification: (c: Conn, email: string) => call<Session>(c, 'POST', '/api/v1/auth/email/request', { email }),
  confirmEmail: (c: Conn, code: string) => call<Session>(c, 'POST', '/api/v1/auth/email/confirm', { code }),
  enableEmailOTP: (c: Conn) => call<Session>(c, 'POST', '/api/v1/auth/email-otp/enable'),
  disableEmailOTP: (c: Conn, password: string) => call<Session>(c, 'POST', '/api/v1/auth/email-otp/disable', { password }),
  // Passkeys (WebAuthn): begin* returns the options object exactly as the server's provider built it - hand
  // it straight to lib/webauthn's createPasskey/getPasskey, and send what they return back as `response`
  // unchanged. Registering and signing in with a passkey are each two calls for that reason: the browser
  // ceremony has to happen between them.
  beginPasskeyRegistration: (c: Conn) => call<unknown>(c, 'POST', '/api/v1/auth/webauthn/register/begin'),
  finishPasskeyRegistration: (c: Conn, name: string, response: unknown) => call<Session>(c, 'POST', '/api/v1/auth/webauthn/register/finish', { name, response }),
  renamePasskey: (c: Conn, id: string, name: string) => call<Session>(c, 'POST', `/api/v1/auth/webauthn/${encodeURIComponent(id)}/rename`, { name }),
  removePasskey: (c: Conn, id: string, password: string) => call<Session>(c, 'POST', `/api/v1/auth/webauthn/${encodeURIComponent(id)}/remove`, { password }),
  // The login half needs no session yet, same as login/login2FA: `pending` is the token login() returned.
  beginPasskeyLogin: (c: Conn, pending: string) => call<unknown>(c, 'POST', '/api/v1/auth/login/2fa/webauthn/begin', { pending }),
  finishPasskeyLogin: (c: Conn, pending: string, response: unknown) => call<Session>(c, 'POST', '/api/v1/auth/login/2fa/webauthn/finish', { pending, response }),

  // organisations, members, invitations
  orgs: (c: Conn) => call<OrgRef[]>(c, 'GET', '/api/v1/orgs'),
  createOrg: (c: Conn, name: string) => call<OrgRef>(c, 'POST', '/api/v1/orgs', { name }),
  previewInvite: (c: Conn, token: string) => call<InvitePreview>(c, 'POST', '/api/v1/invites/preview', { token }),
  acceptInvite: (c: Conn, token: string) => call<OrgRef>(c, 'POST', '/api/v1/invites/accept', { token }),
  renameOrg: (c: Conn, name: string) => call<void>(c, 'POST', '/api/v1/rename', { name }),
  deleteOrg: (c: Conn, confirm: string) => call<void>(c, 'POST', '/api/v1/delete', { confirm }),
  leaveOrg: (c: Conn) => call<void>(c, 'POST', '/api/v1/leave'),
  members: (c: Conn) => call<Member[]>(c, 'GET', '/api/v1/members'),
  setMemberRole: (c: Conn, id: string, role: Role) => call<void>(c, 'POST', `/api/v1/members/${id}/role`, { role }),
  removeMember: (c: Conn, id: string) => call<void>(c, 'POST', `/api/v1/members/${id}/remove`),
  invites: (c: Conn) => call<Invite[]>(c, 'GET', '/api/v1/invites'),
  createInvite: (c: Conn, role: Role, label: string) => call<{ token: string; invite: Invite }>(c, 'POST', '/api/v1/invites', { role, label }),
  revokeInvite: (c: Conn, id: string) => call<void>(c, 'DELETE', `/api/v1/invites/${id}`),

  info: (c: Conn) => call<ServerInfo>(c, 'GET', '/api/v1/info'),
  state: (c: Conn) => call<Partial<ServerState>>(c, 'GET', '/api/v1/state').then(normalizeServerState),
  /** The effective model: declared and observed, with the state of every record and the provenance of every attribute. */
  model: (c: Conn) => call<EffectiveModel>(c, 'GET', '/api/v1/model'),
  createToken: (c: Conn, name: string, tier: number) => call<CreatedToken>(c, 'POST', '/api/v1/tokens', { name, tier }),
  // `code` is the approval code the agent printed in its log (for an older agent without one: the start of the cluster fingerprint).
  approve: (c: Conn, id: string, code: string, tier: number) => call<void>(c, 'POST', `/api/v1/agents/${id}/approve`, { code, tier }),
  reject: (c: Conn, id: string, reason: string) => call<void>(c, 'POST', `/api/v1/agents/${id}/reject`, { reason }),
  revoke: (c: Conn, id: string, reason: string) => call<void>(c, 'POST', `/api/v1/agents/${id}/revoke`, { reason }),
  // consent: an editor may only narrow what an approved agent shares, up to the ceiling its install (Helm access.tier) allows.
  // A tier above the ceiling is refused with a 400 whose body carries `helm`, the command the cluster's owner runs to raise it.
  setAgentTier: (c: Conn, id: string, tier: number) => call<{ accessTier: number; installedTier: number }>(c, 'POST', `/api/v1/agents/${id}/tier`, { tier }),
  setAgentConsent: (c: Conn, id: string, consent: { pausedCollectors: string[]; excludedNamespaces: string[] }) =>
    call<{ pausedCollectors: string[]; excludedNamespaces: string[] }>(c, 'POST', `/api/v1/agents/${id}/consent`, consent),

  // workspace: the human layer of the topology, shared by everyone who signs in
  workspace: (c: Conn) => call<WorkspaceDoc>(c, 'GET', '/api/v1/workspace'),
  workspaceMeta: (c: Conn) => call<WorkspaceMeta>(c, 'GET', '/api/v1/workspace?meta=1'),
  saveWorkspace: (c: Conn, rev: number, doc: unknown) => call<WorkspaceMeta>(c, 'PUT', '/api/v1/workspace', doc, { 'If-Match': String(rev) }),

  // settings, history and decisions (viewers read; administrators change settings and force a recording)
  settings: (c: Conn) => call<Partial<AppSettings>>(c, 'GET', '/api/v1/settings').then(normalizeSettings),
  // `deciderConfigured`, `deciderSecretSet` and `imageDefaults` are derived by the server; it refuses a document
  // that carries them back. `deciderSecret` (set a new one) and `clearDeciderSecret` (remove it) are write-only:
  // never part of `AppSettings` (a GET never carries the secret to round-trip), only ever sent on the way in.
  saveSettings: (c: Conn, s: Partial<AppSettings> & { deciderSecret?: string; clearDeciderSecret?: boolean }) => {
    const { deciderConfigured: _derived, deciderSecretSet: _secretSet, imageDefaults: _defaults, ...body } = s
    return call<Partial<AppSettings>>(c, 'PUT', '/api/v1/settings', body).then(normalizeSettings)
  },
  history: (c: Conn, since?: string) =>
    call<Partial<HistoryIndex>>(c, 'GET', `/api/v1/history${since ? `?since=${encodeURIComponent(since)}` : ''}`).then((h) => ({ points: h.points ?? [], snapshotMinutes: h.snapshotMinutes ?? 5, retentionDays: h.retentionDays ?? 30 }) as HistoryIndex),
  snapshot: (c: Conn, at: string) => call<{ at: string; topology?: Partial<Snapshot['topology']> }>(c, 'GET', `/api/v1/history/snapshot?at=${encodeURIComponent(at)}`).then(normalizeSnapshot),
  traffic: (c: Conn, hours: number) => call<{ hours: number; snapshots: number; rates?: TrafficRate[] }>(c, 'GET', `/api/v1/history/traffic?hours=${hours}`).then((r) => ({ ...r, rates: r.rates ?? [] })),
  events: (c: Conn, q: { since?: string; until?: string; kind?: string; cluster?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<{ events?: ChangeEvent[] }>(c, 'GET', `/api/v1/events${p.size ? `?${p}` : ''}`).then((r) => r.events ?? [])
  },
  storage: (c: Conn) => call<StorageInfo>(c, 'GET', '/api/v1/storage'),
  timeline: (c: Conn, kind: string, id: string) => call<Timeline>(c, 'GET', `/api/v1/timeline?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}`),
  audit: (c: Conn, q: { actor?: string; action?: string; target?: string; since?: string; until?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<{ source: 'graph' | 'local'; rows?: AuditRow[] }>(c, 'GET', `/api/v1/audit${p.size ? `?${p}` : ''}`).then((r) => ({ source: r.source, rows: r.rows ?? [] }))
  },
  workspaceRevisions: (c: Conn) => call<{ revisions?: WorkspaceRevision[] }>(c, 'GET', '/api/v1/workspace/revisions').then((r) => r.revisions ?? []),
  recordNow: (c: Conn) => call<void>(c, 'POST', '/api/v1/history/record'),
  decide: (c: Conn, input: unknown) => call<{ decider: string; result: unknown }>(c, 'POST', '/api/v1/decide', input),
}

/**
 * Is there a Continuum server at this address? True when the answer is JSON with a session
 * (200) or a "sign in" (401). Anything else, such as a static host returning the app's HTML,
 * means no.
 */
export async function probe(c: Conn): Promise<'session' | 'signin' | 'none'> {
  try {
    const res = await fetch(`${c.url.replace(/\/$/, '')}/api/v1/auth/me`, { credentials: 'include', headers: { 'X-Requested-With': 'continuum-ui' } })
    if (!(res.headers.get('content-type') ?? '').includes('application/json')) return 'none'
    if (res.status === 200) return 'session'
    if (res.status === 401) return 'signin'
  } catch {
    /* unreachable */
  }
  return 'none'
}
