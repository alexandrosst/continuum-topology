import type { AgentUpgradeAnswer, AgentUpgradeRequest } from './agentUpgrade'
import type { processorSpec } from './telemetrySpec'
import { normalizeServerState, type ServerState } from './discovered'
import type { AddressCandidates, AddressTest, OperatorSenders, OperatorDestination, OperatorDestinationEntry, RegionalOperator, SignalGrant, TelemetryIntent } from './types'
import { normalizeSettings, normalizeSnapshot, type AppSettings, type DependencySeriesPoint, type HistoryIndex, type Snapshot, type TrafficRate } from './history'
import { normalizeLinksLatest, type LinksLatest } from './clusterLinks'
import type { DecisionLogEntry } from './placement/deciders'
import type { ProposalStatus } from './proposals'
import type { SelfTelemetryEntity } from './selfHealth'
import type { EffectiveModel } from './provenance'
import type * as W from './generated/wire'
import type { Body } from './narrow'

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

export type ServerInfo = W.InfoDoc

/** What a new install is asked to switch on (the per-node collectors) and to narrow (the namespaces the agent may report). */
export interface TokenInstallOptions {
  collectors: { nodeProbe: boolean; flowObserver: boolean; measurements: boolean }
  scope: { namespaces: string[]; exclude: string[]; selector: string }
}

export interface CreatedToken {
  token: string
  install: string
  meta: { id: string; name: string; tier: number; createdAt: string; expiresAt: string }
}

/** What creating an operator, or installing it again, returns beyond the operator itself: `reminders` is informational only - one `helm
 *  upgrade` line per source cluster for pointing that cluster's own agent release at this operator, never applied on the caller's behalf.
 *  `token` and `secretCommand` are there for an operator whose receiver is a bearer one only, and only once: an 'mtls' operator
 *  (`operator.receiverAuth`) has no receiver token to show. `tlsSecretCommand` is the Secret for its own receiver certificate (absent if
 *  minting it failed). The heartbeat fields are present only when the request asked for the heartbeat; `exportSecretCommand` and
 *  `exportTarget` only when the operator exports to ANOTHER operator (the central one in front of FUSION, usually), and then the command is
 *  to run before the install. `restartCommand` is only on "install again / renew". */
export type CreatedOperator = W.OperatorInstalledDoc

/** Where an operator that exports to another operator sends, and whether that works from another cluster: `reachableFromOtherClusters` is
 *  false when the target is only addressable inside the server's own cluster (the central operator, until the server is installed with a
 *  public address for it), so an operator in another cluster cannot reach it. */
export type ExportTarget = W.ExportTargetDoc

/** What POST /operators/{id}/heartbeat returns: the heartbeat credential, minted now and never shown again, and the one-time commands that
 *  put it to use. `rotated` is true when the operator already had one (the old one stopped working at once; the secret command then replaces
 *  the existing Secret, and the collector has to restart to present the new value - `heartbeatRestartCommand`). */
export type OperatorHeartbeatEnabled = W.HeartbeatEnabledDoc

/** One certificate the server issued for an operator, as its ledger records it: who holds it and when it ends. Never the certificate or its
 *  key. 'receiver' is the operator's own server certificate; 'client' is one a sender (a cluster, or another operator) presents, and `sender`
 *  names who holds it. */
export type IssuedCertificate = W.IssuedCertDoc

/** What revoking or deleting an operator hands back: revoking does not stop the receiver (there is no channel to it), so the server returns
 *  the command that removes it. Absent when the server has none to give. */
export type OperatorRemoval = W.OperatorUninstallDoc

/** Options for creating a regional operator. `heartbeat` opts it in to reporting its health to this server;
 *  it is sent explicitly (never left to the server's default) so what the person saw is what was asked for. */
export interface CreateOperatorOptions {
  heartbeat?: boolean
  /** Stamped on everything the operator forwards, next to ikhnos.operator.id/.name. Half-filled rows are
   *  the caller's to drop (cleanTags); the server validates the rest. */
  labels?: { key: string; value: string }[]
  /** How the operator's Service is made reachable from other clusters. Only sets the install command's
   *  service.type; the resulting address is recorded afterwards (setOperatorAddress). Default 'cluster'. */
  exposure?: OperatorExposure
  /** The OTel Collector processors to add to the operator's pipelines (lib/telemetrySpec's processorSpec). Chart values only: the server writes them into the install command and keeps nothing. */
  extraProcessors?: ReturnType<typeof processorSpec>[]
}

/** cluster: this cluster only. loadbalancer / nodeport: the install command makes the Service one. */
export type OperatorExposure = W.Exposure

/** What changing an operator's scope returns: the operator, the reminders, and for an operator-to-operator destination the export target and
 *  the Secret to run first (as CreatedOperator's). */
export type UpdatedOperatorScope = W.OperatorScopeChangedDoc

/** One workload FUSION runs: the three stores and the central operator in front of them. */
export type FusionComponent = W.FusionComponent

/** GET /fusion: the bundled FUSION's switch. `available` is false when this server cannot switch it (the reason says
 *  why, in `message`); `state` is then 'off'. */
export type FusionStatus = W.FusionStatus

export type FusionSignal = W.Signal

/** One service of an Ikhnos application, with the signals FUSION has for it (GET /fusion/applications). */
export type FusionApplicationService = W.AppServiceView
/** An Ikhnos application as the data API lists it: what it is made of and which signals FUSION holds for it in the range. */
export type FusionApplication = W.AppView
/** Every service FUSION has data for (its service.name), with the applications it is in; empty for infrastructure that reports under its own name. */
export type FusionServiceItem = W.ServiceDoc
/** What each store answered: `ok`, or why it could not be read (`unavailable`, `error`). */
export type FusionSources = Record<FusionSignal, string>
export type FusionApplications = W.ApplicationsDoc
export type FusionServices = W.ServicesDoc

/** One store's retention and the volume it lives on (GET /fusion/retention). Sizes are bytes. */
export type FusionRetentionStore = W.RetentionStore

export type FusionRetention = W.RetentionDoc

/** What to change for one store; a missing field is left alone. `volumeGiB` is the size to grow the volume to: volumes can grow but not shrink. */
export type FusionRetentionChange = Partial<{ [K in keyof W.RetentionChange]: number }>
export type FusionRetentionRequest = Partial<Record<FusionSignal, FusionRetentionChange>>

/** A read-only credential for the shared data API over FUSION. The secret is never in this; see CreatedFusionAccessToken. */
export type FusionAccessToken = W.FusionTokenDoc

export type NewFusionAccessToken = W.FusionCreateTokenRequest

/** The one answer that carries the token's secret: shown once, then only the details are ever returned again. */
export type CreatedFusionAccessToken = W.FusionCreatedTokenDoc

/** What GET /telemetry-intents/{id}/command returns: the `--set` flags a person runs against the intent's own agent to actually point its
 *  bundled local operator's export at what the intent grants, plus (for a destination chained to a regional operator) the `kubectl create
 *  secret` for a freshly minted mTLS client certificate. Built fresh on every call, never cached: an operator-chained destination mints a new
 *  client certificate each time. `receiverAuth` is for an operator destination only: 'mtls' means the client certificate in `secretCommands`
 *  is the only credential, otherwise a bearer token is also expected, supplied by the person; `operators` says the same per operator, for an
 *  install that sends signal types to different ones. `namespace` and `release` are where the agent's Helm release runs, which the Secrets
 *  and the upgrade command must both use. `command` (the whole block to paste, Secrets first and the upgrade last) and `problems` (what makes
 *  the telemetry unfit to write a command from; no certificate was issued then) are there only when the telemetry was sent; `endpoint` only
 *  for one operator destination: the operator's recorded address, not the page's placeholder. */
export type TelemetryIntentCommand = W.IntentCommandDoc

/** What a person may do inside one organisation, weakest first. */
export type Role = W.Role
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
export type Passkey = W.PasskeyDoc

/** One personal access token: a long-lived secret for calling the HTTP API without a signed-in browser
 *  (a script, a CI job, curl). Never carries the secret itself - only what settings needs to tell one
 *  apart from another before revoking it. */
export type ApiToken = W.ApiTokenDoc

/** The one-time answer to creating a token: the same fields as ApiToken, plus the secret itself. It is
 *  shown once, here, and never appears in a later call - listApiTokens returns only ApiToken. */
export type CreatedApiToken = W.CreatedAPITokenDoc

export type User = W.UserDoc

export type OrgRef = W.OrgDoc

export type Session = W.SessionDoc

export type Registration = W.Registration

export type Member = W.MemberDoc

export type Invite = W.InviteDoc

export type InvitePreview = W.InvitePreviewDoc

/** The saved workspace as the server sends it: its revision and who saved it, and `data` unless only the meta was asked for. */
export type WorkspaceDoc = W.WorkspaceDoc
export type WorkspaceMeta = Omit<W.WorkspaceDoc, 'data'>

/** Where history is kept and whether that place is healthy. Only administrators receive `error` and `stats`. */
export type StorageInfo = W.StorageDoc

export type TimelineChange = W.Change

/** One event connected to a version by the graph's own EXPLAINS edge: why it looks the way it does, as
 * opposed to Timeline.events, which is everything noticed about the record regardless of whether it
 * produced this particular version. */
export type TimelineExplain = W.ExplainingEvent

/** One period during which a record looked a certain way. `to` is absent while it is current. */
export type TimelineVersion = W.TimelineVersion

/** One entity a graph walk found or a structural diff reported, in the graph's own schema-agnostic
 * shape - whatever kind it is, known to this UI or not. */
export type GraphEntity = W.EntitySnapshot

/** One entity Dependents or Dependencies reached, alongside how many hops away it was. */
export type ReachedEntity = W.Reached

/** How one entity looked different between two moments a structural diff compared. */
export type EntityDiff = W.EntityDiff

/** What changed across the whole estate between two moments: entities added, entities removed, and
 * entities present at both that looked different by the second one. */
export type StructuralDiff = W.GraphDiffDoc

export type AuditRow = W.AuditRow
export type AuditSource = W.AuditSource

export type Timeline = W.TimelineDoc

/** One row of the decision log, as the server stores and returns it - see DecisionLogEntry in
 *  lib/placement/deciders.ts for what produces one and backend/internal/store/store.go's DecisionLog for
 *  the full field-by-field rationale. Read-only from here: there is no UI to edit or delete a row. */
export type DecisionLogRow = W.DecisionLogDoc

export type WorkspaceRevision = W.WorkspaceRev

/** The server's outgoing-mail (SMTP) configuration, server-wide rather than per organisation - see
 *  Settings → Email. Only visible to and settable by an owner of the default organisation
 *  (`User.canManageMail`); the password is never sent back, only `passwordSet`. */
export type MailConfig = W.MailConfigDoc

/** Where the server lives. An empty url means the address this page was loaded from. The session is a cookie, so there is no token here. */
export interface Conn {
  url: string
  /** The organisation the call is about. Everything except accounts and invitations belongs to one. */
  org?: string
}

/** Routes that are about the person or the server, not about one organisation. The FUSION data API (what FUSION holds) is the FUSION
 * organisation's own and is served without an organisation in the path; the switch, retention and tokens next to it are per organisation. */
const GLOBAL = /^\/api\/v1\/(auth\/|server$|orgs$|invites\/(preview|accept)$|mail$|fusion\/(applications|services)([?/]|$))/

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
        'X-Requested-With': 'ikhnos-ui',
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
  serverInfo: (c: Conn) => call<W.ServerInfoDoc>(c, 'GET', '/api/v1/server'),
  me: (c: Conn) => call<Session>(c, 'GET', '/api/v1/auth/me'),
  login: (c: Conn, username: string, password: string) => call<Session>(c, 'POST', '/api/v1/auth/login', { username, password } satisfies Body<W.LoginRequest>),
  // Trusted-header SSO: succeeds only when the server has --admin-sso-header configured AND its reverse proxy
  // asserted an identity that matches an existing account - see serverInfo's `sso` flag, which is what tells the
  // UI it's worth trying this before showing the ordinary sign-in form. A 401 (no header, unknown identity, or
  // the feature is off) is the everyday case, not a bug, so callers should expect and swallow it quietly.
  ssoLogin: (c: Conn) => call<Session>(c, 'GET', '/api/v1/auth/sso'),
  // Called after `login` throws with `twoFactorPending(err)` true, with the code from an authenticator app
  // (or one of the account's recovery codes) and the `pending` token that error carried.
  login2FA: (c: Conn, pending: string, code: string) => call<Session>(c, 'POST', '/api/v1/auth/login/2fa', { pending, code } satisfies Body<W.Login2FARequest>),
  // Mails a fresh code for a pending sign-in that offers 'email' as a method; the code comes back through
  // login2FA's `code` field exactly like a TOTP or recovery code.
  requestLoginEmailCode: (c: Conn, pending: string) => call<void>(c, 'POST', '/api/v1/auth/login/2fa/email', { pending } satisfies Body<W.RequestLoginEmailCodeRequest>),
  register: (c: Conn, username: string, password: string, opts: { org?: string; invite?: string } = {}) =>
    call<Session>(c, 'POST', '/api/v1/auth/register', { username, password, ...(opts.org ? { org: opts.org } : {}), ...(opts.invite ? { invite: opts.invite } : {}) } satisfies Body<W.RegisterRequest>),
  logout: (c: Conn) => call<void>(c, 'POST', '/api/v1/auth/logout'),
  changePassword: (c: Conn, current: string, next: string) => call<Session>(c, 'POST', '/api/v1/auth/password', { current, new: next } satisfies Body<W.ChangePasswordRequest>),
  // Two-factor authentication (TOTP): setup2FA hands back a fresh secret (and its otpauth:// URI, for a
  // copyable key rather than a QR code) that isn't active until enable2FA confirms one code from it - at
  // which point it returns this account's one-time recovery codes. disable2FA needs the current password.
  setup2FA: (c: Conn) => call<W.TwoFactorSetupDoc>(c, 'POST', '/api/v1/auth/2fa/setup'),
  enable2FA: (c: Conn, code: string) => call<W.RecoveryCodesDoc>(c, 'POST', '/api/v1/auth/2fa/enable', { code } satisfies Body<W.Enable2FARequest>),
  disable2FA: (c: Conn, password: string) => call<Session>(c, 'POST', '/api/v1/auth/2fa/disable', { password } satisfies Body<W.Disable2FARequest>),
  // Email as a second factor: requestEmailVerification mails a code to a (possibly new) address and shows it
  // right away, unverified; confirmEmail proves the account can read it. Changing the address this way resets
  // verification and turns email-OTP back off (enforced server-side), so re-verifying is always required after
  // a change. enableEmailOTP needs no code of its own since verification already proved deliverability;
  // disableEmailOTP needs the current password and leaves the address itself verified.
  requestEmailVerification: (c: Conn, email: string) => call<Session>(c, 'POST', '/api/v1/auth/email/request', { email } satisfies Body<W.RequestEmailVerificationRequest>),
  confirmEmail: (c: Conn, code: string) => call<Session>(c, 'POST', '/api/v1/auth/email/confirm', { code } satisfies Body<W.ConfirmEmailRequest>),
  enableEmailOTP: (c: Conn) => call<Session>(c, 'POST', '/api/v1/auth/email-otp/enable'),
  disableEmailOTP: (c: Conn, password: string) => call<Session>(c, 'POST', '/api/v1/auth/email-otp/disable', { password } satisfies Body<W.DisableEmailOTPRequest>),
  // Passkeys (WebAuthn): begin* returns the options object exactly as the server's provider built it - hand
  // it straight to lib/webauthn's createPasskey/getPasskey, and send what they return back as `response`
  // unchanged. Registering and signing in with a passkey are each two calls for that reason: the browser
  // ceremony has to happen between them.
  beginPasskeyRegistration: (c: Conn) => call<unknown>(c, 'POST', '/api/v1/auth/webauthn/register/begin'),
  finishPasskeyRegistration: (c: Conn, name: string, response: unknown) => call<Session>(c, 'POST', '/api/v1/auth/webauthn/register/finish', { name, response } satisfies Body<W.FinishPasskeyRegistrationRequest>),
  renamePasskey: (c: Conn, id: string, name: string) => call<Session>(c, 'POST', `/api/v1/auth/webauthn/${encodeURIComponent(id)}/rename`, { name } satisfies Body<W.RenamePasskeyRequest>),
  removePasskey: (c: Conn, id: string, password: string) => call<Session>(c, 'POST', `/api/v1/auth/webauthn/${encodeURIComponent(id)}/remove`, { password } satisfies Body<W.RemovePasskeyRequest>),
  // The login half needs no session yet, same as login/login2FA: `pending` is the token login() returned.
  beginPasskeyLogin: (c: Conn, pending: string) => call<unknown>(c, 'POST', '/api/v1/auth/login/2fa/webauthn/begin', { pending } satisfies Body<W.BeginPasskeyLoginRequest>),
  finishPasskeyLogin: (c: Conn, pending: string, response: unknown) => call<Session>(c, 'POST', '/api/v1/auth/login/2fa/webauthn/finish', { pending, response } satisfies Body<W.FinishPasskeyLoginRequest>),
  // Personal API tokens: a self-service list, entirely separate from the session/passkey machinery above -
  // revoking or creating one never touches (or returns) the signed-in session itself.
  listApiTokens: (c: Conn) => call<ApiToken[]>(c, 'GET', '/api/v1/auth/tokens'),
  createApiToken: (c: Conn, name: string) => call<CreatedApiToken>(c, 'POST', '/api/v1/auth/tokens', { name } satisfies Body<W.CreateAPITokenRequest>),
  revokeApiToken: (c: Conn, id: string) => call<void>(c, 'POST', `/api/v1/auth/tokens/${encodeURIComponent(id)}/revoke`),

  // organisations, members, invitations
  orgs: (c: Conn) => call<OrgRef[]>(c, 'GET', '/api/v1/orgs'),
  createOrg: (c: Conn, name: string) => call<OrgRef>(c, 'POST', '/api/v1/orgs', { name } satisfies Body<W.CreateOrgRequest>),
  previewInvite: (c: Conn, token: string) => call<InvitePreview>(c, 'POST', '/api/v1/invites/preview', { token } satisfies Body<W.PreviewInviteRequest>),
  acceptInvite: (c: Conn, token: string) => call<OrgRef>(c, 'POST', '/api/v1/invites/accept', { token } satisfies Body<W.AcceptInviteRequest>),
  renameOrg: (c: Conn, name: string) => call<void>(c, 'POST', '/api/v1/rename', { name }),
  deleteOrg: (c: Conn, confirm: string) => call<void>(c, 'POST', '/api/v1/delete', { confirm }),
  leaveOrg: (c: Conn) => call<void>(c, 'POST', '/api/v1/leave'),
  members: (c: Conn) => call<Member[]>(c, 'GET', '/api/v1/members'),
  setMemberRole: (c: Conn, id: string, role: Role) => call<void>(c, 'POST', `/api/v1/members/${id}/role`, { role } satisfies Body<W.SetMemberRoleRequest>),
  removeMember: (c: Conn, id: string) => call<void>(c, 'POST', `/api/v1/members/${id}/remove`),
  invites: (c: Conn) => call<Invite[]>(c, 'GET', '/api/v1/invites'),
  createInvite: (c: Conn, role: Role, label: string) => call<W.CreatedInviteDoc>(c, 'POST', '/api/v1/invites', { role, label } satisfies Body<W.CreateInviteRequest>),
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
        headers: { 'X-Requested-With': 'ikhnos-ui', ...(etag ? { 'If-None-Match': etag } : {}) },
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
  /** A token and the command that installs an agent with it. `options` are what the person chose to switch on or narrow; the server writes them into the command. */
  createToken: (c: Conn, name: string, tier: number, options?: TokenInstallOptions) => call<CreatedToken>(c, 'POST', '/api/v1/tokens', { name, tier, ...options }),
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
    call<CreatedOperator>(c, 'POST', '/api/v1/operators', { name, sourceClusterIds, destination, heartbeat: options.heartbeat === true, labels: options.labels ?? [], exposure: options.exposure ?? 'cluster', ...(options.extraProcessors?.length ? { extraProcessors: options.extraProcessors } : {}) }),
  /** The addresses discovery says the operator can be reached at (adminRole): to pick from instead of typing one. */
  /** Says which cluster the server runs in (central operator only); an empty id takes the choice back. */
  setServerCluster: (c: Conn, operatorId: string, clusterId: string) => call<{ clusterId: string }>(c, 'PUT', `/api/v1/operators/${encodeURIComponent(operatorId)}/server-cluster`, { clusterId }),
  operatorAddressCandidates: (c: Conn, id: string) => call<AddressCandidates>(c, 'GET', `/api/v1/operators/${encodeURIComponent(id)}/address-candidates`),
  /** The clusters sending to the operator, the address each dials, how delivery goes and, where it fails, which end is likely at fault (adminRole). */
  operatorSenders: (c: Conn, id: string) => call<OperatorSenders>(c, 'GET', `/api/v1/operators/${encodeURIComponent(id)}/senders`),
  /** Asks the agent (so the cluster it runs in) to try each address (adminRole, audited): a TCP connection is timed, nothing is sent. */
  startAddressTests: (c: Conn, agentId: string, addresses: string[]) => call<{ tests: AddressTest[] }>(c, 'POST', `/api/v1/agents/${encodeURIComponent(agentId)}/address-tests`, { addresses }),
  /** The tests an agent still holds (they expire after ten minutes) and what each found so far. */
  addressTests: (c: Conn, agentId: string) => call<{ tests: AddressTest[] }>(c, 'GET', `/api/v1/agents/${encodeURIComponent(agentId)}/address-tests`),
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
  openFusionPage: (c: Conn, page: 'grafana' | 'prometheus') => call<W.FusionPageDoc>(c, 'POST', '/api/v1/fusion/pages', { page } satisfies W.OpenFusionPageRequest),
  disableFusion: (c: Conn) => call<FusionStatus>(c, 'POST', '/api/v1/fusion/disable'),
  listFusionTokens: (c: Conn) => call<FusionAccessToken[]>(c, 'GET', '/api/v1/fusion/tokens'),
  createFusionToken: (c: Conn, req: NewFusionAccessToken) => call<CreatedFusionAccessToken>(c, 'POST', '/api/v1/fusion/tokens', req),
  revokeFusionToken: (c: Conn, id: string) => call<void>(c, 'DELETE', `/api/v1/fusion/tokens/${encodeURIComponent(id)}`),
  /** What FUSION holds, by Ikhnos application and by service, over the last day. */
  getFusionApplications: (c: Conn) => call<FusionApplications>(c, 'GET', '/api/v1/fusion/applications?from=now-24h'),
  getFusionServices: (c: Conn) => call<FusionServices>(c, 'GET', '/api/v1/fusion/services?from=now-24h'),
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
  /** The command that changes an installed agent (its access ceiling, its telemetry, or both), written by the server. Computes only: nothing is stored or issued. */
  agentUpgradeCommand: (c: Conn, req: AgentUpgradeRequest) => call<AgentUpgradeAnswer>(c, 'POST', '/api/v1/commands/agent-upgrade', req),
  /** The commands for an intent's agent. With the telemetry the page is editing the answer also holds the whole block to paste (`command`), written by the server. Each call issues a client certificate. */
  getTelemetryIntentCommand: (c: Conn, id: string, telemetry?: AgentUpgradeRequest['telemetry']) =>
    call<TelemetryIntentCommand>(c, 'POST', `/api/v1/telemetry-intents/${encodeURIComponent(id)}/command`, telemetry ? { telemetry } : undefined),

  // workspace: the human layer of the topology, shared by everyone who signs in
  workspace: (c: Conn) => call<WorkspaceDoc>(c, 'GET', '/api/v1/workspace'),
  workspaceMeta: (c: Conn) => call<WorkspaceMeta>(c, 'GET', '/api/v1/workspace?meta=1'),
  saveWorkspace: (c: Conn, rev: number, doc: unknown) => call<W.WorkspaceSavedDoc>(c, 'PUT', '/api/v1/workspace', doc, { 'If-Match': String(rev) }),

  // settings, history and decisions (viewers read; administrators change settings and force a recording)
  settings: (c: Conn) => call<Partial<AppSettings>>(c, 'GET', '/api/v1/settings').then(normalizeSettings),
  // `deciderConfigured`, `deciderSecretSet` and `imageDefaults` are derived by the server; it refuses a document
  // that carries them back. `deciderSecret` (set a new one) and `clearDeciderSecret` (remove it) are write-only:
  // never part of `AppSettings` (a GET never carries the secret to round-trip), only ever sent on the way in.
  // `rev` (the document's revision, which a GET carries) is not part of the body either: it goes in If-Match, so the server
  // refuses the save with 409 - the current document in the error's `body` - when another save came in since. Left out, the save replaces whatever is there.
  saveSettings: (c: Conn, s: Partial<AppSettings> & Pick<Body<W.SaveSettingsRequest>, 'deciderSecret' | 'clearDeciderSecret'>, rev?: number) => {
    const { deciderConfigured: _derived, deciderSecretSet: _secretSet, imageDefaults: _defaults, rev: _rev, ...body } = s
    return call<Partial<AppSettings>>(c, 'PUT', '/api/v1/settings', body satisfies Body<W.SaveSettingsRequest>, rev === undefined ? {} : { 'If-Match': String(rev) }).then(normalizeSettings)
  },
  // The latest figures of every watched cluster pair, read from the link series in FUSION (nothing is stored by the server).
  linksLatest: (c: Conn) => call<Partial<LinksLatest>>(c, 'GET', '/api/v1/links/latest').then(normalizeLinksLatest),
  // The server's SMTP configuration (see MailConfig): global, not one organisation's, so these two are the
  // only calls in this file that don't need `c.org` at all. Same write-only-password convention as
  // saveSettings' decider secret: omit `password` (or send "") to leave it alone, `clearPassword: true` to
  // remove it, a non-empty `password` to set a new one.
  mailConfig: (c: Conn) => call<MailConfig>(c, 'GET', '/api/v1/mail'),
  saveMailConfig: (c: Conn, m: Body<W.PutMailConfigRequest>) =>
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
    return call<W.EventsDoc>(c, 'GET', `/api/v1/events${p.size ? `?${p}` : ''}`).then((r) => r.events)
  },
  storage: (c: Conn) => call<StorageInfo>(c, 'GET', '/api/v1/storage'),
  timeline: (c: Conn, kind: string, id: string) => call<Timeline>(c, 'GET', `/api/v1/timeline?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}`),
  // Dependents/Dependencies walk the graph's relationship edges backward/forward from one entity, as
  // of a moment: "what breaks if this goes down" and "what this needs in order to keep working." hops
  // defaults to 3 server-side when omitted.
  dependents: (c: Conn, kind: string, id: string, at: string, hops?: number) =>
    call<W.GraphWalkDoc>(
      c, 'GET', `/api/v1/graph/dependents?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}&at=${encodeURIComponent(at)}${hops ? `&hops=${hops}` : ''}`,
    ),
  dependencies: (c: Conn, kind: string, id: string, at: string, hops?: number) =>
    call<W.GraphWalkDoc>(
      c, 'GET', `/api/v1/graph/dependencies?kind=${encodeURIComponent(kind)}&id=${encodeURIComponent(id)}&at=${encodeURIComponent(at)}${hops ? `&hops=${hops}` : ''}`,
    ),
  structuralDiff: (c: Conn, from: string, to: string) => call<W.GraphDiffDoc>(c, 'GET', `/api/v1/graph/diff?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`),
  audit: (c: Conn, q: { actor?: string; action?: string; target?: string; since?: string; until?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<W.AuditSearchDoc>(c, 'GET', `/api/v1/audit${p.size ? `?${p}` : ''}`)
  },
  // proposals: what advisors suggest about an agent's access, and the decision on each (editors; widening takes an administrator).
  // Nothing here submits one: an advisor does that with an editor's API token, and submitting changes nothing.
  listProposals: (c: Conn, q: { status?: ProposalStatus; agent?: string; limit?: number } = {}) => {
    const p = new URLSearchParams()
    for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== '') p.set(k, String(v))
    return call<W.ProposalsDoc>(c, 'GET', `/api/v1/proposals${p.size ? `?${p}` : ''}`).then((r) => r.proposals)
  },
  listAdvisors: (c: Conn) => call<W.AdvisorsDoc>(c, 'GET', '/api/v1/proposals/advisors').then((r) => r.advisors),
  getProposal: (c: Conn, id: string) => call<W.ProposalDoc>(c, 'GET', `/api/v1/proposals/${encodeURIComponent(id)}`),
  acceptProposal: (c: Conn, id: string, note = '') => call<W.ProposalDoc>(c, 'POST', `/api/v1/proposals/${encodeURIComponent(id)}/accept`, { note }),
  rejectProposal: (c: Conn, id: string, note = '') => call<W.ProposalDoc>(c, 'POST', `/api/v1/proposals/${encodeURIComponent(id)}/reject`, { note }),
  workspaceRevisions: (c: Conn) => call<W.WorkspaceRevisionsDoc>(c, 'GET', '/api/v1/workspace/revisions').then((r) => r.revisions),
  recordNow: (c: Conn) => call<void>(c, 'POST', '/api/v1/history/record'),
  decide: (c: Conn, input: unknown) => call<{ decider: string; result: unknown }>(c, 'POST', '/api/v1/decide', input),
  // Fire-and-forget from the caller's point of view - see recordDecisionLog in lib/placement/deciders.ts,
  // which is what actually calls this and swallows whatever it rejects with.
  recordDecisions: (c: Conn, entries: DecisionLogEntry[]) => call<W.RecordedDoc>(c, 'POST', '/api/v1/decisions', { entries }),
  listDecisions: (c: Conn, limit?: number) =>
    call<W.DecisionsDoc>(c, 'GET', `/api/v1/decisions${limit ? `?limit=${limit}` : ''}`).then((r) => r.decisions),
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
    const res = await fetch(`${c.url.replace(/\/$/, '')}/api/v1/auth/me`, { credentials: 'include', headers: { 'X-Requested-With': 'ikhnos-ui' } })
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
