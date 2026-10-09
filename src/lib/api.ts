import { normalizeServerState, type ServerState } from './discovered'
import type { OperatorDestination, OperatorDestinationEntry, ReceiverAuth, RegionalOperator, SignalGrant, TelemetryIntent } from './types'
import { normalizeSettings, normalizeSnapshot, type AppSettings, type ChangeEvent, type DependencySeriesPoint, type HistoryIndex, type Snapshot, type TrafficRate } from './history'
import type { DecisionLogEntry } from './placement/deciders'
import type { SelfTelemetryEntity } from './selfHealth'
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

/** What creating or updating a regional operator's scope returns, beyond the operator itself:
 *  `reminders` is informational only (see the operators feature's own design note) - one `helm upgrade`
 *  line per source cluster for pointing that cluster's own agent release at this operator, never applied
 *  on the caller's behalf. `token` and `secretCommand` are present only on create, and only once: the
 *  receiver bearer token is never retrievable again after this response. */
export interface CreatedOperator {
  operator: RegionalOperator
  /** Absent for an operator whose receiver is gated by the client certificate alone (`operator.receiverAuth`
   *  is 'mtls'): there is no receiver token to show. Present, once, for a 'bearer' one. */
  token?: string
  install: string
  /** The `kubectl create secret` for `token` - absent exactly when `token` is. */
  secretCommand?: string
  /** The `kubectl create secret` for the operator's own receiver TLS certificate (server cert + this
   *  org's CA), wherever the operator itself is installed. Absent if minting it failed - see
   *  OperatorTLSBundle's own comment server-side; the receiver bearer token above still works either way. */
  tlsSecretCommand?: string
  reminders: string[]
  /** Present only when the create request asked for the heartbeat (`heartbeat: true`). The install command
   *  already carries its `--set heartbeat.*` flags; this is the one extra Secret it points at, shown once. */
  heartbeatToken?: string
  heartbeatSecretCommand?: string
  heartbeatUrl?: string
  heartbeatIntervalSeconds?: number
  /** Why the heartbeat address will not work as printed (plain HTTP) - show it next to the commands. */
  heartbeatWarning?: string
  /** The `kubectl` that stores this server's certificate authority, when the heartbeat address is served with a private one (the install
   *  command's heartbeat flags name that Secret): run it before the install. Absent when the server's certificate is publicly trusted. */
  heartbeatCaSecretCommand?: string
  /** Only on "install again / renew": restarts the operator's pods. Its certificates reload on their own; a new receiver token or health
   *  credential is read once at start. */
  restartCommand?: string
  /** Present only when the operator exports to ANOTHER operator (the central one in front of FUSION, usually): the
   *  `kubectl create secret` holding the client certificate that operator's receiver requires, issued just now from
   *  its own CA. Run it before the install command, which already points the exporter at it. */
  exportSecretCommand?: string
  exportTarget?: ExportTarget
}

/** Where an operator that exports to another operator sends, and whether that works from another cluster. */
export interface ExportTarget {
  operatorId: string
  name: string
  endpoint: string
  /** False when the target is only addressable inside the server's own cluster (the central operator, until the
   *  server is installed with a public address for it): an operator in another cluster cannot reach it. */
  reachableFromOtherClusters: boolean
}

/** What POST /operators/{id}/heartbeat returns: the heartbeat credential, minted now and never shown again,
 *  and the one-time commands that put it to use. `rotated` is true when the operator already had one (the old
 *  one stopped working at once; the secret command then replaces the existing Secret, and the collector has to
 *  restart to present the new value - `heartbeatRestartCommand`). */
export interface OperatorHeartbeatEnabled {
  operator: RegionalOperator
  rotated: boolean
  heartbeatToken: string
  heartbeatSecretCommand: string
  heartbeatUpgradeCommand: string
  heartbeatUrl: string
  heartbeatIntervalSeconds: number
  heartbeatRestartCommand?: string
  heartbeatWarning?: string
  /** As CreatedOperator.heartbeatCaSecretCommand. */
  heartbeatCaSecretCommand?: string
}

/** What revoking or deleting an operator hands back: revoking does not stop the receiver (there is no channel to it), so the
 *  server returns the command that removes it. Absent when the server has none to give (and on an older server, which answers 204). */
/** One certificate the server issued for an operator, as its ledger records it: who holds it and when it ends. Never the certificate or its key. */
export interface IssuedCertificate {
  serial: string
  /** 'receiver' is the operator's own server certificate; 'client' is one a sender (a cluster, or another operator) presents. */
  kind: 'receiver' | 'client'
  subject: string
  /** The cluster or operator that holds a client certificate; absent for a receiver certificate. */
  sender?: string
  issuedBy: string
  issuedAt: string
  notBefore: string
  notAfter: string
  state: 'ok' | 'expired'
}

export interface OperatorRemoval {
  uninstall?: string
}

/** Options for creating a regional operator. `heartbeat` opts it in to reporting its health to this server;
 *  it is sent explicitly (never left to the server's default) so what the person saw is what was asked for. */
export interface CreateOperatorOptions {
  heartbeat?: boolean
  /** Stamped on everything the operator forwards, next to continuum.operator.id/.name. Half-filled rows are
   *  the caller's to drop (cleanTags); the server validates the rest. */
  labels?: { key: string; value: string }[]
  /** How the operator's Service is made reachable from other clusters. Only sets the install command's
   *  service.type; the resulting address is recorded afterwards (setOperatorAddress). Default 'cluster'. */
  exposure?: OperatorExposure
}

/** cluster: this cluster only. loadbalancer / nodeport: the install command makes the Service one. */
export type OperatorExposure = 'cluster' | 'loadbalancer' | 'nodeport'

export interface UpdatedOperatorScope {
  operator: RegionalOperator
  reminders: string[]
  /** As CreatedOperator.exportSecretCommand / exportTarget: present only for an operator-to-operator destination. */
  exportSecretCommand?: string
  exportTarget?: ExportTarget
}

/** One workload FUSION runs: the three stores and the central operator in front of them. */
export interface FusionComponent {
  component: 'metrics' | 'logs' | 'traces' | 'central' | 'grafana'
  label: string
  desired: number
  ready: number
  /** Why this part is not ready, in a few plain words ("Pulling the image", "Waiting for a volume", "No node has room"). Absent when ready or unknown. */
  reason?: string
}

/** GET /fusion: the bundled FUSION's switch. `available` is false when this server cannot switch it (the reason says
 *  why, in `message`); `state` is then 'off'. */
export interface FusionStatus {
  available: boolean
  reason?: 'not-configured' | 'not-installed' | 'no-access' | 'other-org'
  state: 'off' | 'starting' | 'running' | 'attention'
  message?: string
  since?: string
  /** When FUSION last received data (RFC 3339); absent while unknown or before the first. */
  lastDataAt?: string
  components?: FusionComponent[]
  central?: { operatorId: string; endpoint: string; exposed: boolean; exists: boolean; service?: string; namespace?: string }
  /** Whether this server serves the shared data API (and its access tokens) for this organisation. */
  data?: boolean
  /** Same-origin paths of the web pages FUSION serves through this server, present only for a page that is up right now. */
  links?: { prometheus?: string; grafana?: string }
}

export type FusionSignal = 'metrics' | 'logs' | 'traces'

/** One store's retention and the volume it lives on (GET /fusion/retention). Sizes are bytes. */
export interface FusionRetentionStore {
  component: FusionSignal
  label: string
  /** The setting as the store takes it ("15d", "168h"); `days` is it in whole days, rounded up, and `exactDays` says it was a whole number of them. */
  value: string
  days: number
  exactDays: boolean
  minDays: number
  maxDays: number
  /** False when the volume could not be read; then none of the sizes mean anything. */
  volumeKnown: boolean
  /** What was asked of the cluster, and what it has made available so far (they differ while a volume is being grown). */
  volumeBytes: number
  capacityBytes: number
  storageClass?: string
  /** The cluster's answer to a trial resize: false when the storage class does not allow volumes to grow (`growNote` says so); absent when not known. */
  canGrow?: boolean
  growNote?: string
  /** True on storage that does not hold a volume to its size (local-path and other host directories): the size is only what was asked for. */
  sizeNotEnforced?: boolean
  resizing?: boolean
  resizeNote?: string
  /** Measured use: `volume` is the kubelet's count for the whole volume, `database` Prometheus' account of its data. Absent when not measured. */
  usedBytes?: number
  usedSource?: 'volume' | 'database'
  /** Growth to plan on, and how many days of data it is based on. Absent while there is too little data to tell. */
  bytesPerDay?: number
  dataDays?: number
  /** The part of the volume the data should stay within; the volume a retention needs is bytesPerDay * days / share. */
  share: number
  /** Prometheus' own size cap, and how many days of data it keeps at today's growth. */
  sizeLimitBytes?: number
  sizeLimitDays?: number
}

export interface FusionRetention {
  available: boolean
  /** Why it cannot be set from here: `unmanaged` (installed without the server managing it), `forbidden` (the server's Role lacks the grant), ... */
  reason?: string
  message?: string
  running: boolean
  stores: FusionRetentionStore[]
  warnings?: string[]
}

/** What to change for one store; a missing field is left alone. */
export interface FusionRetentionChange {
  days?: number
  /** The size to grow the volume to, in GiB. Volumes can grow but not shrink. */
  volumeGiB?: number
}
export type FusionRetentionRequest = Partial<Record<FusionSignal, FusionRetentionChange>>

/** A read-only credential for the shared data API over FUSION. The secret is never in this; see CreatedFusionAccessToken. */
export interface FusionAccessToken {
  id: string
  name: string
  signals: FusionSignal[]
  /** Empty = every namespace (and every cluster, for `clusters`). */
  namespaces: string[]
  clusters: string[]
  createdBy: string
  createdAt: string
  expiresAt: string
  lastUsedAt?: string
}

export interface NewFusionAccessToken {
  name: string
  signals: FusionSignal[]
  namespaces: string[]
  clusters: string[]
  expiresInDays: number
}

/** The one answer that carries the token's secret: shown once, then only the details are ever returned again. */
export interface CreatedFusionAccessToken {
  token: string
  details: FusionAccessToken
}

/** What GET /telemetry-intents/{id}/command returns: the `--set` flags a person runs against the
 *  intent's own agent to actually point its bundled local operator's export at what the intent grants,
 *  plus (for a destination chained to a regional operator) the `kubectl create secret` for a freshly
 *  minted mTLS client certificate - see admin_telemetry_intents.go's telemetryIntentCommand for exactly
 *  what each covers. Built fresh on every call (adminRole, unlike the rest of this feature), never cached:
 *  an operator-chained destination mints a new client certificate each time. */
export interface TelemetryIntentCommand {
  installFragment: string
  secretCommands: string[]
  /** Only for an operator destination: how that operator's receiver authenticates this agent. 'mtls' means the
   *  client certificate in `secretCommands` is the only credential; anything else (including absent) means a
   *  bearer token is also expected, supplied by the person. */
  receiverAuth?: ReceiverAuth
  /** For every regional operator the command involves, how its receiver authenticates this agent - the same as
   *  `receiverAuth`, but per operator, for an install that sends signal types to different ones. */
  operators?: Record<string, ReceiverAuth>
  /** Where the agent's Helm release runs, as the server knows it (or the chart's defaults). Both the Secrets
   *  above and the upgrade command must use it, so the release finds what was created for it. */
  namespace?: string
  release?: string
}

/** What minting a quick-start gateway token returns - the plaintext, once, plus when it was minted and
 *  expires. Never retrievable again after this response (see api.mintGatewayToken). */
export interface MintedGatewayToken {
  token: string
  createdAt: string
  expiresAt: string
}

/** What checking a quick-start gateway token's status returns - never the secret, only whether one
 *  exists and, if so, whether it has already lapsed (see api.gatewayTokenStatus). */
export interface GatewayTokenStatus {
  active: boolean
  expired?: boolean
  createdAt?: string
  expiresAt?: string
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

/** One personal access token: a long-lived secret for calling the HTTP API without a signed-in browser
 *  (a script, a CI job, curl). Never carries the secret itself - only what settings needs to tell one
 *  apart from another before revoking it. */
export interface ApiToken {
  id: string
  name: string
  createdAt: string
  lastUsedAt?: string
}

/** The one-time answer to creating a token: the same fields as ApiToken, plus the secret itself. It is
 *  shown once, here, and never appears in a later call - listApiTokens returns only ApiToken. */
export interface CreatedApiToken extends ApiToken {
  token: string
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
  /** Whether this account may read and change the server's SMTP configuration: true only for an owner of
   *  the default organisation (the one created with the first account) - see Settings → Email. */
  canManageMail: boolean
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

/** One event connected to a version by the graph's own EXPLAINS edge: why it looks the way it does, as
 * opposed to Timeline.events, which is everything noticed about the record regardless of whether it
 * produced this particular version. */
export interface TimelineExplain {
  id: number
  kind: string
  detail?: string
  cause?: string
}

/** One period during which a record looked a certain way. `to` is absent while it is current. */
export interface TimelineVersion {
  from: string
  to?: string
  name: string
  status?: string
  changes: TimelineChange[]
  explains?: TimelineExplain[]
}

/** One entity a graph walk found or a structural diff reported, in the graph's own schema-agnostic
 * shape - whatever kind it is, known to this UI or not. */
export interface GraphEntity {
  kind: string
  id: string
  name?: string
  status?: string
  cluster?: string
  doc: unknown
}

/** One entity Dependents or Dependencies reached, alongside how many hops away it was. */
export interface ReachedEntity extends GraphEntity {
  hops: number
}

/** How one entity looked different between two moments a structural diff compared. */
export interface EntityDiff {
  kind: string
  id: string
  name?: string
  changes: TimelineChange[]
}

/** What changed across the whole estate between two moments: entities added, entities removed, and
 * entities present at both that looked different by the second one. */
export interface StructuralDiff {
  from: string
  to: string
  added: GraphEntity[]
  removed: GraphEntity[]
  changed: EntityDiff[]
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

/** One row of the decision log, as the server stores and returns it - see DecisionLogEntry in
 *  lib/placement/deciders.ts for what produces one and backend/internal/store/store.go's DecisionLog for
 *  the full field-by-field rationale. Read-only from here: there is no UI to edit or delete a row. */
export interface DecisionLogRow {
  id: string
  at: string
  recordedBy: string
  deciderId: string
  deciderName: string
  deciderKind: 'builtin' | 'external'
  serviceId: string
  serviceName: string
  from: string
  to: string
  reason?: string
  benefit: number
  confidence: string
  verdict: string
  beforeCost: number
  afterCost: number
  migrationCost: number
}

export interface WorkspaceRevision {
  rev: number
  at: string
  by: string
  bytes: number
}

/** The server's outgoing-mail (SMTP) configuration, server-wide rather than per organisation - see
 *  Settings → Email. Only visible to and settable by an owner of the default organisation
 *  (`User.canManageMail`); the password is never sent back, only `passwordSet`. */
export interface MailConfig {
  host: string
  port: string
  username: string
  from: string
  passwordSet: boolean
  /** Derived: same as `host` being non-empty, for convenience. */
  enabled: boolean
}

/** Where the server lives. An empty url means the address this page was loaded from. The session is a cookie, so there is no token here. */
export interface Conn {
  url: string
  /** The organisation the call is about. Everything except accounts and invitations belongs to one. */
  org?: string
}

/** Routes that are about the person or the server, not about one organisation. */
const GLOBAL = /^\/api\/v1\/(auth\/|server$|orgs$|invites\/(preview|accept)$|mail$)/

// No request in this file had a timeout: a stalled connection (a laptop sleep/wake, a proxy silently
// dropping a long-lived keep-alive) left its fetch() promise pending forever - never resolving, never
// rejecting - rather than failing loudly. For a one-off call that surfaces as a spinner that never stops;
// for the server store's 2-5s poll (server.ts's refresh()) it silently froze the whole app's picture of the
// world at whatever it last saw, with nothing to show for it but idle network waits (no error, no CPU cost -
// exactly what makes it easy to miss). 30s is generous enough for a slow link or a large payload (the
// effective model response is tens of KB) while still eventually giving up rather than waiting forever.
const REQUEST_TIMEOUT_MS = 30_000

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
      signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
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
  serverInfo: (c: Conn) => call<{ registration: Registration; version: string; sso?: boolean }>(c, 'GET', '/api/v1/server'),
  me: (c: Conn) => call<Session>(c, 'GET', '/api/v1/auth/me'),
  login: (c: Conn, username: string, password: string) => call<Session>(c, 'POST', '/api/v1/auth/login', { username, password }),
  // Trusted-header SSO: succeeds only when the server has --admin-sso-header configured AND its reverse proxy
  // asserted an identity that matches an existing account - see serverInfo's `sso` flag, which is what tells the
  // UI it's worth trying this before showing the ordinary sign-in form. A 401 (no header, unknown identity, or
  // the feature is off) is the everyday case, not a bug, so callers should expect and swallow it quietly.
  ssoLogin: (c: Conn) => call<Session>(c, 'GET', '/api/v1/auth/sso'),
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
  // Personal API tokens: a self-service list, entirely separate from the session/passkey machinery above -
  // revoking or creating one never touches (or returns) the signed-in session itself.
  listApiTokens: (c: Conn) => call<ApiToken[]>(c, 'GET', '/api/v1/auth/tokens'),
  createApiToken: (c: Conn, name: string) => call<CreatedApiToken>(c, 'POST', '/api/v1/auth/tokens', { name }),
  revokeApiToken: (c: Conn, id: string) => call<void>(c, 'POST', `/api/v1/auth/tokens/${encodeURIComponent(id)}/revoke`),

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
  /**
   * Like `model` above, but conditional: pass the ETag from a previous response and the server answers 304
   * with no body when the model's content hasn't changed since (see twin.go's `Admin.model`, which was
   * always built for exactly this - `state.generatedAt` changes on the clock every poll regardless of
   * content, so a consumer that naively re-fetches on every tick, as effectiveModel.ts used to, was paying
   * for the full ~70KB payload every 2-5s for as long as anything on screen needed it, never once hitting
   * this cheap path). Returns `undefined` on a 304 - the caller keeps whatever model it already has -
   * otherwise the fresh model and its new ETag to remember for next time.
   */
  modelIfChanged: async (c: Conn, etag: string | undefined): Promise<{ model: EffectiveModel; etag: string | null } | undefined> => {
    let res: Response
    try {
      res = await fetch(`${c.url.replace(/\/$/, '')}${scoped(c, '/api/v1/model')}`, {
        credentials: 'include',
        headers: { 'X-Requested-With': 'continuum-ui', ...(etag ? { 'If-None-Match': etag } : {}) },
        signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS),
      })
    } catch {
      throw new ApiError(0, `Cannot reach the server at ${c.url || 'this address'}. Check the address and that it is running.`)
    }
    if (res.status === 304) return undefined
    const data = await res.json().catch(() => ({}))
    if (!res.ok) throw new ApiError(res.status, (data as { error?: string }).error ?? `Request failed (${res.status})`, data as Record<string, unknown>)
    return { model: data as EffectiveModel, etag: res.headers.get('ETag') }
  },
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

  // regional operators: a fleet of standalone aggregation points, separate from the live agent connection
  // model above - see lib/types.ts's own RegionalOperator doc comment. CRUD only, like tokens/invites/
  // members: no live status to poll, so these are ordinary one-shot calls, not part of `state`.
  listOperators: (c: Conn) => call<RegionalOperator[]>(c, 'GET', '/api/v1/operators'),
  /** The destination picker's read model: active operators (and the central one, even before FUSION was switched on),
   *  no secrets, readable by editors. See OperatorDestinationEntry. */
  listOperatorDestinations: (c: Conn) => call<OperatorDestinationEntry[]>(c, 'GET', '/api/v1/operator-destinations'),
  getOperator: (c: Conn, id: string) => call<RegionalOperator>(c, 'GET', `/api/v1/operators/${encodeURIComponent(id)}`),
  createOperator: (c: Conn, name: string, sourceClusterIds: string[], destination: OperatorDestination, options: CreateOperatorOptions = {}) =>
    call<CreatedOperator>(c, 'POST', '/api/v1/operators', { name, sourceClusterIds, destination, heartbeat: options.heartbeat === true, labels: options.labels ?? [], exposure: options.exposure ?? 'cluster' }),
  /** Records where other clusters reach the operator ('' clears it). The server checks it is a host:port. */
  setOperatorAddress: (c: Conn, id: string, address: string) => call<RegionalOperator>(c, 'POST', `/api/v1/operators/${encodeURIComponent(id)}/address`, { address }),
  /** Mints (or rotates) the operator's heartbeat credential: adminRole, audited, and the credential is shown
   *  once in this response only. */
  enableOperatorHeartbeat: (c: Conn, id: string) => call<OperatorHeartbeatEnabled>(c, 'POST', `/api/v1/operators/${encodeURIComponent(id)}/heartbeat`),
  updateOperatorScope: (c: Conn, id: string, sourceClusterIds: string[], destination: OperatorDestination) =>
    call<UpdatedOperatorScope>(c, 'POST', `/api/v1/operators/${encodeURIComponent(id)}/scope`, { sourceClusterIds, destination }),
  // FUSION: part of the server, switched on and off here. enable also prepares the central operator (its CA and
  // certificates) and starts everything; disable stops it and keeps the data.
  getFusion: (c: Conn) => call<FusionStatus>(c, 'GET', '/api/v1/fusion'),
  enableFusion: (c: Conn) => call<FusionStatus>(c, 'POST', '/api/v1/fusion/enable'),
  /** The path that opens one of FUSION's own pages in a new tab, with a ticket that works once for thirty seconds: the session cookie does not come with a link into a new tab, so the link carries its own way in. */
  openFusionPage: (c: Conn, page: 'grafana' | 'prometheus') => call<{ path: string }>(c, 'POST', '/api/v1/fusion/pages', { page }),
  disableFusion: (c: Conn) => call<FusionStatus>(c, 'POST', '/api/v1/fusion/disable'),
  listFusionTokens: (c: Conn) => call<FusionAccessToken[]>(c, 'GET', '/api/v1/fusion/tokens'),
  createFusionToken: (c: Conn, req: NewFusionAccessToken) => call<CreatedFusionAccessToken>(c, 'POST', '/api/v1/fusion/tokens', req),
  revokeFusionToken: (c: Conn, id: string) => call<void>(c, 'DELETE', `/api/v1/fusion/tokens/${encodeURIComponent(id)}`),
  getFusionRetention: (c: Conn) => call<FusionRetention>(c, 'GET', '/api/v1/fusion/retention'),
  setFusionRetention: (c: Conn, req: FusionRetentionRequest) => call<FusionRetention>(c, 'PUT', '/api/v1/fusion/retention', req),
  /** `force` is the explicit "yes, what depends on it breaks": without it the server answers 409 while anything does (see RegionalOperator.usedBy). */
  revokeOperator: (c: Conn, id: string, reason: string, force = false) =>
    call<OperatorRemoval | undefined>(c, 'POST', `/api/v1/operators/${encodeURIComponent(id)}/revoke`, force ? { reason, force: true } : { reason }),
  deleteOperator: (c: Conn, id: string, force = false) =>
    call<OperatorRemoval | undefined>(c, 'DELETE', `/api/v1/operators/${encodeURIComponent(id)}${force ? '?force=true' : ''}`),
  /** Install again, or renew the certificates: re-issues the receiver and client certificates from the operator's own CA and
   *  answers like createOperator. Operators that have a heartbeat secret or a receiver token get a new one (they are only kept hashed). */
  /** What was issued for the operator, newest first. Records start with the ledger: certificates from before it are not listed. */
  listOperatorCertificates: (c: Conn, id: string) => call<{ certificates: IssuedCertificate[] }>(c, 'GET', `/api/v1/operators/${encodeURIComponent(id)}/certificates`),
  reinstallOperator: (c: Conn, id: string) => call<CreatedOperator>(c, 'POST', `/api/v1/operators/${encodeURIComponent(id)}/install`),

  // telemetry intents: the server-side counterpart of one agent's bundled local-operator telemetry grant
  // (scope + signals + destination) - see lib/types.ts's own TelemetryIntent doc comment. CRUD like
  // operators above, plus `command`, which (unlike every other call here) needs adminRole: it mints
  // operator client credential material, not just reads or writes a record.
  listTelemetryIntents: (c: Conn, agentId?: string) =>
    call<TelemetryIntent[]>(c, 'GET', `/api/v1/telemetry-intents${agentId ? `?agentId=${encodeURIComponent(agentId)}` : ''}`),
  getTelemetryIntent: (c: Conn, id: string) => call<TelemetryIntent>(c, 'GET', `/api/v1/telemetry-intents/${encodeURIComponent(id)}`),
  createTelemetryIntent: (c: Conn, agentId: string, name: string, namespaces: string[], exclude: string[], signals: SignalGrant[], destination: OperatorDestination, routes?: NonNullable<TelemetryIntent['routes']>) =>
    call<TelemetryIntent>(c, 'POST', '/api/v1/telemetry-intents', routes ? { agentId, name, namespaces, exclude, signals, destination, routes } : { agentId, name, namespaces, exclude, signals, destination }),
  updateTelemetryIntentScope: (c: Conn, id: string, namespaces: string[], exclude: string[], signals: SignalGrant[]) =>
    call<TelemetryIntent>(c, 'POST', `/api/v1/telemetry-intents/${encodeURIComponent(id)}/scope`, { namespaces, exclude, signals }),
  /** `routes` replaces the per-signal-type destinations together with the default (an empty object clears them);
   *  left out, they are untouched. */
  updateTelemetryIntentDestination: (c: Conn, id: string, destination: OperatorDestination, routes?: NonNullable<TelemetryIntent['routes']>) =>
    call<TelemetryIntent>(c, 'POST', `/api/v1/telemetry-intents/${encodeURIComponent(id)}/destination`, routes ? { destination, routes } : { destination }),
  revokeTelemetryIntent: (c: Conn, id: string, reason: string) => call<void>(c, 'POST', `/api/v1/telemetry-intents/${encodeURIComponent(id)}/revoke`, { reason }),
  deleteTelemetryIntent: (c: Conn, id: string) => call<void>(c, 'DELETE', `/api/v1/telemetry-intents/${encodeURIComponent(id)}`),
  getTelemetryIntentCommand: (c: Conn, id: string) => call<TelemetryIntentCommand>(c, 'POST', `/api/v1/telemetry-intents/${encodeURIComponent(id)}/command`),

  // quick-start gateway tokens (see QuickStartBackends.tsx and quickStartGateway.ts): a short-lived
  // bearer secret scoped to one quick-start backend instance, checked entirely by the nginx gateway
  // manifest that backend's token is baked into - never by this server. mintGatewayToken returns the
  // plaintext once, the same "shown only now" convention as createOperator's receiver token;
  // gatewayTokenStatus never does, only whether one exists and when it expires.
  mintGatewayToken: (c: Conn, backendId: string, ttlSeconds?: number) =>
    call<MintedGatewayToken>(c, 'POST', `/api/v1/quick-start/${encodeURIComponent(backendId)}/gateway-token`, { ttlSeconds: ttlSeconds ?? 0 }),
  gatewayTokenStatus: (c: Conn, backendId: string) =>
    call<GatewayTokenStatus>(c, 'GET', `/api/v1/quick-start/${encodeURIComponent(backendId)}/gateway-token`),

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
  // The server's SMTP configuration (see MailConfig): global, not one organisation's, so these two are the
  // only calls in this file that don't need `c.org` at all. Same write-only-password convention as
  // saveSettings' decider secret: omit `password` (or send "") to leave it alone, `clearPassword: true` to
  // remove it, a non-empty `password` to set a new one.
  mailConfig: (c: Conn) => call<MailConfig>(c, 'GET', '/api/v1/mail'),
  saveMailConfig: (c: Conn, m: { host: string; port: string; username: string; from: string; password?: string; clearPassword?: boolean }) =>
    call<MailConfig>(c, 'PUT', '/api/v1/mail', m),
  history: (c: Conn, since?: string) =>
    call<Partial<HistoryIndex>>(c, 'GET', `/api/v1/history${since ? `?since=${encodeURIComponent(since)}` : ''}`).then((h) => ({ points: h.points ?? [], snapshotMinutes: h.snapshotMinutes ?? 5, retentionDays: h.retentionDays ?? 30 }) as HistoryIndex),
  snapshot: (c: Conn, at: string) => call<{ at: string; topology?: Partial<Snapshot['topology']>; agents?: Snapshot['agents'] }>(c, 'GET', `/api/v1/history/snapshot?at=${encodeURIComponent(at)}`).then(normalizeSnapshot),
  traffic: (c: Conn, hours: number) => call<{ hours: number; snapshots: number; rates?: TrafficRate[] }>(c, 'GET', `/api/v1/history/traffic?hours=${hours}`).then((r) => ({ ...r, rates: r.rates ?? [] })),
  // One dependency's RTT/loss/throughput trend across recorded history, for a sparkline - see
  // DependencySeriesPoint for why each field can be missing on a given point.
  dependencySeries: (c: Conn, dependencyId: string, hours: number) =>
    call<{ points?: DependencySeriesPoint[] }>(c, 'GET', `/api/v1/history/dependency/${encodeURIComponent(dependencyId)}/series?hours=${hours}`).then((r) => r.points ?? []),
  // What running Ikhnos itself costs, per entity (the server, and every connected agent) - see
  // admin_telemetry.go's own doc comment for the exact response shape. Entities with no self-telemetry
  // history yet are left out server-side, not returned with an empty samples array.
  selfTelemetry: (c: Conn) => call<SelfTelemetryEntity[]>(c, 'GET', '/api/v1/telemetry/self'),
  events: (c: Conn, q: { since?: string; until?: string; kind?: string; cluster?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<{ events?: ChangeEvent[] }>(c, 'GET', `/api/v1/events${p.size ? `?${p}` : ''}`).then((r) => r.events ?? [])
  },
  storage: (c: Conn) => call<StorageInfo>(c, 'GET', '/api/v1/storage'),
  timeline: (c: Conn, kind: string, id: string) => call<Timeline>(c, 'GET', `/api/v1/timeline?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}`),
  // Dependents/Dependencies walk the graph's relationship edges backward/forward from one entity, as
  // of a moment: "what breaks if this goes down" and "what this needs in order to keep working." hops
  // defaults to 3 server-side when omitted.
  dependents: (c: Conn, kind: string, id: string, at: string, hops?: number) =>
    call<{ at: string; hops: number; reached?: ReachedEntity[] }>(
      c, 'GET', `/api/v1/graph/dependents?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}&at=${encodeURIComponent(at)}${hops ? `&hops=${hops}` : ''}`,
    ).then((r) => ({ ...r, reached: r.reached ?? [] })),
  dependencies: (c: Conn, kind: string, id: string, at: string, hops?: number) =>
    call<{ at: string; hops: number; reached?: ReachedEntity[] }>(
      c, 'GET', `/api/v1/graph/dependencies?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}&at=${encodeURIComponent(at)}${hops ? `&hops=${hops}` : ''}`,
    ).then((r) => ({ ...r, reached: r.reached ?? [] })),
  structuralDiff: (c: Conn, from: string, to: string) =>
    call<Partial<StructuralDiff>>(c, 'GET', `/api/v1/graph/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`).then(
      (r) => ({ from: r.from ?? from, to: r.to ?? to, added: r.added ?? [], removed: r.removed ?? [], changed: r.changed ?? [] }) as StructuralDiff,
    ),
  audit: (c: Conn, q: { actor?: string; action?: string; target?: string; since?: string; until?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<{ source: 'graph' | 'local'; rows?: AuditRow[] }>(c, 'GET', `/api/v1/audit${p.size ? `?${p}` : ''}`).then((r) => ({ source: r.source, rows: r.rows ?? [] }))
  },
  workspaceRevisions: (c: Conn) => call<{ revisions?: WorkspaceRevision[] }>(c, 'GET', '/api/v1/workspace/revisions').then((r) => r.revisions ?? []),
  recordNow: (c: Conn) => call<void>(c, 'POST', '/api/v1/history/record'),
  decide: (c: Conn, input: unknown) => call<{ decider: string; result: unknown }>(c, 'POST', '/api/v1/decide', input),
  // Fire-and-forget from the caller's point of view - see recordDecisionLog in lib/placement/deciders.ts,
  // which is what actually calls this and swallows whatever it rejects with.
  recordDecisions: (c: Conn, entries: DecisionLogEntry[]) => call<{ recorded: number }>(c, 'POST', '/api/v1/decisions', { entries }),
  listDecisions: (c: Conn, limit?: number) =>
    call<{ decisions?: DecisionLogRow[] }>(c, 'GET', `/api/v1/decisions${limit ? `?limit=${limit}` : ''}`).then((r) => r.decisions ?? []),
}

/** What probe() above learns from its one request: whether an Ikhnos server is even there, and - when
 *  someone is already signed in - the session it would otherwise take a second /api/v1/auth/me round trip
 *  to fetch. See probe()'s own doc comment for why that body is worth carrying along instead of discarding
 *  it. */
export type ProbeResult = { status: 'session'; session: Session } | { status: 'signin' } | { status: 'none' }

/**
 * Is there an Ikhnos server at this address? The answer is 'session' when the answer is JSON with a
 * signed-in session (200), 'signin' for a JSON "not signed in" (401), and 'none' for anything else, such as
 * a static host returning the app's HTML.
 *
 * Carries the parsed session body along on a 'session' result rather than just a status flag: this hits the
 * exact same endpoint (`GET /api/v1/auth/me`) that `api.me()` calls, so a caller that already has this
 * result (server.ts's connect(), the common path on every reload once someone is signed in) can use it
 * directly instead of firing a second, wasted request-and-DB-round-trip for data this call already has in
 * hand.
 */
export async function probe(c: Conn): Promise<ProbeResult> {
  try {
    const res = await fetch(`${c.url.replace(/\/$/, '')}/api/v1/auth/me`, { credentials: 'include', headers: { 'X-Requested-With': 'continuum-ui' } })
    if (!(res.headers.get('content-type') ?? '').includes('application/json')) return { status: 'none' }
    if (res.status === 200) {
      const session = (await res.json().catch(() => undefined)) as Session | undefined
      return session ? { status: 'session', session } : { status: 'none' }
    }
    if (res.status === 401) return { status: 'signin' }
  } catch {
    /* unreachable */
  }
  return { status: 'none' }
}
