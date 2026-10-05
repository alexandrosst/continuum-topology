// Package store persists the server's control state behind an interface so the
// SQLite development store can later be replaced by Postgres without touching callers.
package store

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNotFound = errors.New("not found")
	// ErrTokenInvalid is returned for an unknown, expired or already-used token.
	// The three cases are deliberately indistinguishable to callers.
	ErrTokenInvalid = errors.New("enrollment token is invalid, expired or already used")
	// ErrBadState means the agent is not in a state that allows the operation.
	ErrBadState = errors.New("agent is not in a state that allows this")
	// ErrExists means a record with that unique name already exists.
	ErrExists = errors.New("already exists")
	// ErrConflict means the workspace changed since the revision the caller last saw.
	ErrConflict = errors.New("workspace changed elsewhere")
	// ErrClusterEnrolled means another live agent already represents this cluster.
	ErrClusterEnrolled = errors.New("another agent is already approved for this cluster")
	// ErrWrongCluster means the token was created for one specific cluster and the agent reported another.
	// The token is not consumed, so the right cluster can still use it.
	ErrWrongCluster = errors.New("the enrollment token is bound to a different cluster")
)

type AgentStatus string

const (
	StatusPending  AgentStatus = "pending"
	StatusApproved AgentStatus = "approved"
	StatusRevoked  AgentStatus = "revoked"
	StatusRejected AgentStatus = "rejected"
	// StatusExpired is a pending enrollment nobody approved in time. The agent may enroll again with the
	// same token and key (its record is reopened); a record nobody reopens is deleted after a while.
	StatusExpired AgentStatus = "expired"
)

// Token is a one-time enrollment token. Only its SHA-256 is stored; the secret is
// shown once at creation.
type Token struct {
	ID         string
	OrgID      string
	Label      string // becomes the agent's name, normally the cluster name
	AccessTier int    // highest tier this token may enroll (0-4)
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
	UsedBy     string // agent id
	// ExpectedFingerprint, when set, is the kube-system UID of the only cluster that may enroll with the token.
	ExpectedFingerprint string
}

type Agent struct {
	ID             string
	OrgID          string
	Name           string
	Status         AgentStatus
	InstalledTier  int // what the RBAC on the cluster allows
	TierCap        int // the enrollment token's ceiling
	AccessTier     int // what a human approved
	Fingerprint    string
	ClusterID      string
	CSR            []byte
	PollSecretHash []byte // cleared after the first mTLS contact
	Version        string
	K8sVersion     string
	TokenID        string
	CreatedAt      time.Time
	ApprovedAt     *time.Time
	ApprovedBy     string
	RevokedAt      *time.Time
	Reason         string
	LastSeen       *time.Time
	ConnectingIP   string
	LeafDER        []byte
	LeafNotAfter   *time.Time
	// ApprovalHash is the hash of the approval code the agent printed in its own log (never the code).
	// Empty for an agent that predates approval codes: a "legacy" enrollment.
	ApprovalHash []byte
	// ApprovalAttempts counts the codes an administrator has typed for this pending agent, right or wrong.
	ApprovalAttempts int
	// ClockSkewMs is the agent's clock minus the server's, as the agent last measured it (0: unknown or in step).
	ClockSkewMs int64
}

// OperatorStatus is the lifecycle of a regional operator's registration. There is no "pending" state:
// unlike an agent, a regional operator never phones home to be approved - creating one issues its
// receiver credential immediately.
type OperatorStatus string

const (
	OperatorActive  OperatorStatus = "active"
	OperatorRevoked OperatorStatus = "revoked"
)

// DestinationKind is where a regional operator (or a local operator's own TelemetryIntent) re-exports
// what it collects. "operator" points at another regional operator in this organisation directly - see
// Core.validateDestination - rather than at an arbitrary external endpoint. Continuum's fleet is two
// tiers: that target operator's own export is not itself re-validated or chained any further.
type DestinationKind string

const (
	DestinationExternal DestinationKind = "external"
	DestinationOperator DestinationKind = "operator"
)

// Destination is an OTLP export target, shaped like the agent chart's own telemetry.export.otlp block
// (endpoint/insecure/caFile/auth header+secret) so the same rendering logic applies to both.
type Destination struct {
	Kind           DestinationKind
	Endpoint       string
	Insecure       bool
	CAFile         string
	AuthHeaderName string
	AuthSecretName string
	AuthSecretKey  string
	// TargetOperatorID is only meaningful for DestinationOperator: the id of the regional operator in
	// this organisation to export into, checked by Core.validateDestination the same way an external
	// destination's Endpoint is.
	TargetOperatorID string
}

// Modality is a telemetry signal category: what an operator (regional or local) actually moves. Used
// today only by Operator.AcceptedModalities, to narrow which kinds of telemetry a regional operator will
// take from a source cluster's agent.
type Modality string

const (
	ModalityMetrics Modality = "metrics"
	ModalityLogs    Modality = "logs"
	ModalityTraces  Modality = "traces"
)

// Operator is a regional operator: a standalone OTel Collector that aggregates telemetry already
// exported by a set of approved agents' clusters (SourceClusterIDs) and re-exports it to Destination.
// Unlike Agent it does not connect back to the server by default - ReceiverAuthTokenHash is the only
// credential it needs, checked when something exports into it, not when it starts up. The one optional
// exception is the heartbeat (HeartbeatHash): an operator that opted in sends a content-free "I am alive"
// request, with its own secret, so the server can tell online from offline.
type Operator struct {
	ID    string
	OrgID string
	Name  string
	// SiteID is optional: where this operator conceptually lives, for UI grouping only.
	SiteID           string
	Status           OperatorStatus
	SourceClusterIDs []string
	Destination      Destination
	// AcceptedModalities restricts which signal modalities this operator will accept from its source
	// clusters' agents; empty/nil means it accepts everything - the only behaviour possible before this
	// field existed, and so what every operator row written before it was added keeps reading back as
	// (see telemetry_migrate.go).
	AcceptedModalities []Modality
	// ReceiverAuthTokenHash is the hash of the bearer token the operator's receiver expects; the token
	// itself is minted and returned once, the same as an enrollment token.
	ReceiverAuthTokenHash []byte
	// HeartbeatHash is the hash of the secret the operator presents when it reports that it is alive (see
	// Core.EnableOperatorHeartbeat); nil when the operator never opted in. It is a different credential from
	// ReceiverAuthTokenHash on purpose: that one authenticates traffic INTO the operator, this one
	// authenticates the operator's own call OUT to this server, and neither must ever open the other door.
	HeartbeatHash []byte
	// HeartbeatEnabledAt is when HeartbeatHash was last minted (first enabled, or rotated); nil with it.
	HeartbeatEnabledAt *time.Time
	// LastSeenAt is when a heartbeat last arrived (coalesced - see Core.RecordOperatorHeartbeat); nil when none ever has.
	LastSeenAt *time.Time
	CreatedBy  string
	CreatedAt  time.Time
	RevokedAt  *time.Time
	Reason     string
}

// TelemetryIntentStatus is the lifecycle of a TelemetryIntent. Like Operator there is no "pending" state:
// creating one takes effect immediately, there being nothing external to wait on.
type TelemetryIntentStatus string

const (
	TelemetryIntentActive  TelemetryIntentStatus = "active"
	TelemetryIntentRevoked TelemetryIntentStatus = "revoked"
)

// SignalGrant is one telemetry signal a TelemetryIntent asks an agent's bundled local operator (the
// OTel-collector telemetry extractors in the continuum-agent chart) to collect. ID matches the frontend's
// TELEMETRY_SIGNALS vocabulary (resourceUsage, energy, kubernetesState, nodeRuntime, networkLatency,
// applicationMetrics, systemLogs, kubernetesEvents, applicationLogs, traces, accelerators) but is opaque
// to this package - nothing here validates it against that list. Source says where the grant came from:
// "builtin" (a signal the chart turns on by default), "existing" (already running before this intent), or
// "bundle-<tool>" (installed alongside a named quick-start tool).
type SignalGrant struct {
	ID     string
	Source string
}

// TelemetryIntent is what a local operator (the telemetry extractors bundled in one agent's own
// continuum-agent install) is granted to collect and where to export it: the same server-side
// professionalism Operator already gives the standalone regional-operator chart, but scoped to a single
// agent instead of a fleet of source clusters. Namespaces/Exclude narrow which of the agent's own
// namespaces are in scope (on top of whatever the agent's tier/consent already leaves out); Signals is
// which telemetry signals are granted. Core.CreateTelemetryIntent enforces at most one active intent per
// agent - update the existing one instead of layering a second.
type TelemetryIntent struct {
	ID      string
	OrgID   string
	AgentID string
	Name    string
	Status  TelemetryIntentStatus
	// Namespaces/Exclude mirror the shape of Consent.Excluded - names, not reported facts - except
	// Namespaces here is a positive scope (empty means "every namespace the agent's own tier/consent
	// already allows") where Exclude narrows it further.
	Namespaces []string
	Exclude    []string
	Signals    []SignalGrant
	// Destination reuses store.Destination as-is; see Operator's own field for the shape it follows.
	Destination Destination
	CreatedBy   string
	CreatedAt   time.Time
	RevokedAt   *time.Time
	Reason      string
}

// GatewayToken is a short-lived bearer secret scoped to one quick-start backend instance (see
// server.QuickStartBackend) - minted so the Part C nginx gateway's ConfigMap can check requests entirely
// on its own, with zero callback to this server, the same one-way trust model as everything else
// quick-start. Only its hash is ever stored; the secret itself is minted and returned once, never again -
// the same rule every other secret in this app follows (see server/tokens.go).
type GatewayToken struct {
	ID         string
	OrgID      string
	BackendID  string
	SecretHash []byte
	CreatedBy  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// Resume is what a retried enrollment changes on the agent it continues.
type Resume struct {
	PollSecretHash []byte
	ApprovalHash   []byte
	CSR            []byte
	Version        string
	K8sVersion     string
	IP             string
}

// User is a person who can sign in to the admin UI. Accounts belong to no organisation: a person is a
// member of any number of them (see Membership). Passwords are stored only as argon2id hashes.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	MustChange   bool // set for passwords an operator chose: the person must replace it before doing anything else
	CreatedAt    time.Time
	DisabledAt   *time.Time
	LastLogin    *time.Time
	// TOTPSecret is base32, in the clear (same trust boundary as everything else in this database: an
	// argon2id hash cannot stand in for it, since a login must reproduce and compare a live code from it).
	// Set the moment "set up 2FA" is asked for; TOTPEnabledAt stays nil until a code confirms it works, so a
	// half-finished setup never blocks sign-in.
	TOTPSecret    string
	TOTPEnabledAt *time.Time
	// TOTPRecovery holds SHA-256 hashes of unused one-time recovery codes (HashSecret, hex-encoded); each is
	// removed the moment it is spent. Empty once 2FA is off.
	TOTPRecovery []string
	// Email is set from account settings, not at sign-up (there is no email field there at all - see
	// RegisterScreen). EmailVerifiedAt is nil until a code sent to it is confirmed, and is reset to nil
	// the moment Email changes again, so a stale or mistyped address can never be trusted. EmailOTPEnabledAt
	// is a separate flag from verification: turning email-as-a-second-factor off does not forget the address,
	// the same way it stays usable elsewhere on the account even while this particular use of it is off.
	Email             string
	EmailVerifiedAt   *time.Time
	EmailOTPEnabledAt *time.Time
	// WebAuthnCredentials lists every passkey or security key registered to this account, oldest first.
	// Unlike TOTP and email there is no separate enabled flag: a credential only ever lands here once
	// FinishRegistration has verified it end to end, so its mere presence means it can be used to sign in.
	WebAuthnCredentials []WebAuthnCredential
}

// WebAuthnCredential is one passkey or security key registered to an account (a phone's platform
// authenticator and a hardware key, say, both at once - nothing here limits an account to one).
type WebAuthnCredential struct {
	// CredentialID is the authenticator's own opaque handle for this credential; unique per account, and how
	// a login response is matched back to the row whose PublicKey should verify it.
	CredentialID []byte
	// PublicKey is the COSE-encoded public key returned at registration, opaque to everything here except
	// the WebAuthn library that verifies a login's signature against it.
	PublicKey []byte
	// SignCount is bumped on every successful login; a count that fails to advance is how a cloned
	// authenticator gets caught, so it is the caller's job to reject a login where it does not.
	SignCount uint32
	// Transports is the authenticator's own best-effort hints (usb, nfc, ble, internal, hybrid), passed back
	// to the browser on a later login so it can skip straight to the right one instead of guessing.
	Transports []string
	// Name is the person's own label for telling their credentials apart in settings ("MacBook Touch ID"),
	// never shown to, or trusted from, anything but the account's own owner.
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Org is a tenant: one private topology with its own agents, history, settings and members.
type Org struct {
	ID        string
	Name      string
	CreatedAt time.Time
	CreatedBy string // user id
}

// Membership is what a person may do inside one organisation: owner, admin, editor or viewer.
type Membership struct {
	OrgID     string
	UserID    string
	Role      string
	CreatedAt time.Time
	AddedBy   string // user id of whoever invited them ("" for the creator)
}

// Member is a person together with their role in one organisation.
type Member struct {
	User
	Role     string
	JoinedAt time.Time
}

// MyOrg is one organisation a person belongs to, with their role in it.
type MyOrg struct {
	Org
	Role string
}

// Invite lets one person join one organisation with a given role. Only its SHA-256 is stored; the
// secret is shown once to whoever created it and passed on by them (there is no email).
type Invite struct {
	ID        string
	OrgID     string
	Role      string
	Label     string // whom it is for, free text
	CreatedBy string // user id
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
	UsedBy    string // user id
}

// Session is a signed-in browser. Only the SHA-256 of its secret is stored.
type Session struct {
	UserID    string
	CreatedAt time.Time
	LastUsed  time.Time
	ExpiresAt time.Time
	IP        string
}

// APIToken is a personal access token: a long-lived secret a person can use to call the HTTP API
// without a signed-in browser (scripts, CI, curl). Only the SHA-256 of its secret is stored, the same
// as a session - the difference is that nothing about it expires or goes idle on its own, and it
// carries a name instead of an IP, since revoking it (rather than it lapsing) is the only way it ends.
type APIToken struct {
	ID        string
	UserID    string
	Name      string
	CreatedAt time.Time
	LastUsed  *time.Time
}

// Workspace is the human layer of the topology (manual records, overrides, decisions), one
// JSON document per organisation. The server treats the content as opaque.
type Workspace struct {
	OrgID     string
	Rev       int64 // 0 = never saved
	Data      []byte
	UpdatedAt time.Time
	UpdatedBy string
	// Note is what the server changed in the document when it upgraded it (for example, observed records it
	// removed from an older workspace). It is shown once and cleared by the next save.
	Note string
}

type AuditEvent struct {
	ID         int64
	At         time.Time
	OrgID      string
	Actor      string
	Action     string
	TargetKind string
	TargetID   string
	Detail     string
}

// DecisionLog is one row of the decision log: one service a single decider proposed moving, at a single
// moment, with the numbers the engine computed for it at the time (see src/lib/placement/deciders.ts's
// DecisionLogEntry, which is this row's frontend counterpart). It exists so a decider's quality can later
// be judged - was its predicted benefit real? - by comparing a past recommendation against what the
// topology actually looked like afterward.
//
// This is pure recording, nothing more: unlike AuditEvent it needs no tamper-evident hash chain (that is
// for what a person or administrator did; a decision log row is a decider's advisory output, never
// anything that was carried out - see Deciders.tsx's own "a decider recommends, a person decides"), and
// unlike the Neo4j graph's versioned topology entities it is an append-only event log, so it belongs here
// next to the other SQLite-only logs (audit, events) rather than in the graph.
//
// Schema/ClusterCount/ServiceCount/PolicyJSON summarise what the decider was given rather than storing it
// in full: the estate (every service, flow and cluster) is already kept elsewhere (the live topology, and
// its own history), so storing it again on every row of every run would multiply without adding anything
// a later lookup could not already answer, and - the same "no secrets, no environment values" rule the
// decision input itself already follows (see Deciders.tsx) - none of it is or carries a secret.
type DecisionLog struct {
	ID    int64
	OrgID string
	At    time.Time
	// RecordedBy is who was signed in when the recommendation was computed, not necessarily anyone who
	// acted on it (nothing here is ever executed).
	RecordedBy string

	DeciderID   string
	DeciderName string
	// DeciderKind is "builtin" or "external", mirroring Decider.kind in src/lib/placement/deciders.ts.
	DeciderKind string

	// Schema is DECISION_SCHEMA (src/lib/placement/deciders.ts) at the time this was computed.
	Schema       int
	ClusterCount int
	ServiceCount int
	// PolicyJSON is the cost model's own weights (Policy, serialised) used for this recommendation - small,
	// never a secret, and the one input that directly explains why BeforeCost/AfterCost/Benefit came out
	// the way they did.
	PolicyJSON []byte

	ServiceID   string
	ServiceName string
	FromCluster string
	ToCluster   string
	// Reason is the decider's own one-line explanation, when it gave one.
	Reason string
	// Benefit, BeforeCost, AfterCost and MigrationCost are exactly what the engine computed for this move
	// (Recommendation/MoveOutcome in engine.ts) - not recomputed or approximated here.
	Benefit       float64
	Confidence    string // Level, e.g. "high" | "medium" | "low"
	Verdict       string // Evaluation.verdict, e.g. "fits" | "cantTell"
	BeforeCost    float64
	AfterCost     float64
	MigrationCost float64
}

type Store interface {
	CreateToken(ctx context.Context, t Token, hash []byte) error
	ListTokens(ctx context.Context, org string) ([]Token, error)
	DeleteToken(ctx context.Context, org, id string) error

	// EnrollAgent atomically consumes the token and inserts the pending agent, or
	// returns ErrTokenInvalid and changes nothing.
	// A token bound to another cluster than a.Fingerprint fails with ErrWrongCluster (the token is returned so
	// it can be audited) and is not consumed.
	EnrollAgent(ctx context.Context, tokenHash []byte, a Agent, now time.Time) (Token, error)
	// AgentByToken returns the agent that consumed the token, or ErrNotFound. Retried enrollments find themselves this way.
	AgentByToken(ctx context.Context, tokenHash []byte) (Agent, error)
	// ResumeEnrollment continues an enrollment that is retried: a pending agent (same approval hash) or an
	// approved one that has not collected its certificate keeps its identity and gets a fresh poll secret;
	// an expired one becomes pending again (reopen), with a new approval hash, no failed attempts and a new
	// clock for its lifetime. ErrBadState if the agent is in none of those states.
	ResumeEnrollment(ctx context.Context, id string, r Resume, reopen bool, now time.Time) error
	// CountApprovalAttempt records one code typed for a pending agent and returns how many there are now.
	CountApprovalAttempt(ctx context.Context, id string) (int, error)
	// ExpirePending marks the organisation's pending agents created before the cutoff as expired and returns them.
	ExpirePending(ctx context.Context, org string, cutoff time.Time) ([]Agent, error)
	// PurgeExpired deletes expired agents whose record is older than the cutoff.
	PurgeExpired(ctx context.Context, org string, cutoff time.Time) (int, error)
	SetClockSkew(ctx context.Context, id string, ms int64) error
	// SetAccessTier changes what an approved agent may report (an administrator narrowing or widening it later);
	// ErrBadState unless the agent is approved. SetInstalledTier records the ceiling the agent says its install has.
	SetAccessTier(ctx context.Context, id string, tier int) error
	SetInstalledTier(ctx context.Context, id string, tier int) error

	GetAgent(ctx context.Context, id string) (Agent, error)
	ListAgents(ctx context.Context, org string) ([]Agent, error)
	// ApproveAgent moves pending -> approved. ErrClusterEnrolled if another approved agent has the same fingerprint.
	ApproveAgent(ctx context.Context, id string, tier int, approver string, clusterID string, leaf []byte, notAfter, now time.Time) error
	RejectAgent(ctx context.Context, id, reason string, now time.Time) error
	RevokeAgent(ctx context.Context, id, reason string, now time.Time) error
	// SetLeaf stores a renewed certificate; only for approved agents.
	SetLeaf(ctx context.Context, id string, leaf []byte, notAfter time.Time) error
	ClearPollSecret(ctx context.Context, id string) error
	Touch(ctx context.Context, id, ip, version, k8sVersion string, now time.Time) error

	// ---- regional operators ----

	// CreateOperator inserts a new regional operator together with the hash of its freshly minted
	// receiver token, in one call (there being no separate "mint a token" step, unlike agent enrollment -
	// nothing needs to phone home first).
	CreateOperator(ctx context.Context, op Operator, tokenHash []byte) error
	GetOperator(ctx context.Context, id string) (Operator, error)
	ListOperators(ctx context.Context, org string) ([]Operator, error)
	// UpdateOperatorScope replaces an operator's source clusters, destination and accepted modalities
	// together, atomically - there is no reason to leave them in an inconsistent combination between two
	// separate calls.
	UpdateOperatorScope(ctx context.Context, id string, sourceClusterIDs []string, dest Destination, acceptedModalities []Modality) error
	RevokeOperator(ctx context.Context, id, reason string, now time.Time) error
	DeleteOperator(ctx context.Context, id string) error
	// SetOperatorHeartbeat stores the hash of a freshly minted heartbeat secret for an ACTIVE operator,
	// replacing any earlier one (so the earlier secret stops working at once). ErrBadState if it is not
	// active, ErrNotFound if there is no such operator. last_seen_at is deliberately left as it was.
	SetOperatorHeartbeat(ctx context.Context, id string, hash []byte, now time.Time) error
	// GetOperatorByHeartbeatHash finds the operator a heartbeat secret's hash belongs to (any status:
	// the caller decides what a revoked one means). ErrNotFound if none.
	GetOperatorByHeartbeatHash(ctx context.Context, hash []byte) (Operator, error)
	// TouchOperatorSeen records that a heartbeat arrived at the given time.
	TouchOperatorSeen(ctx context.Context, id string, at time.Time) error

	// ---- telemetry intents ----

	// CreateTelemetryIntent inserts a new telemetry intent for one agent.
	CreateTelemetryIntent(ctx context.Context, ti TelemetryIntent) error
	GetTelemetryIntent(ctx context.Context, id string) (TelemetryIntent, error)
	// ListTelemetryIntentsByAgent lists every intent (active or revoked) belonging to one agent, oldest first.
	ListTelemetryIntentsByAgent(ctx context.Context, agentID string) ([]TelemetryIntent, error)
	ListTelemetryIntents(ctx context.Context, org string) ([]TelemetryIntent, error)
	// UpdateTelemetryIntentScope replaces an intent's namespaces, exclusions and signal grants together,
	// atomically, the same reasoning UpdateOperatorScope gives for an operator's own scope.
	UpdateTelemetryIntentScope(ctx context.Context, id string, namespaces, exclude []string, signals []SignalGrant) error
	// UpdateTelemetryIntentDestination replaces where an intent exports to, kept separate from its scope
	// since the two change independently (a destination rotates far less often than namespaces/signals do).
	UpdateTelemetryIntentDestination(ctx context.Context, id string, dest Destination) error
	RevokeTelemetryIntent(ctx context.Context, id, reason string, now time.Time) error
	DeleteTelemetryIntent(ctx context.Context, id string) error

	// ---- quick-start gateway tokens ----

	// CreateGatewayToken inserts a freshly minted gateway token's hash - see GatewayToken.
	CreateGatewayToken(ctx context.Context, t GatewayToken, hash []byte) error
	// LatestGatewayToken returns the most recently minted gateway token for a quick-start backend
	// (ErrNotFound if none was ever minted for it) - never the secret, only whether/when one exists and
	// expires, for the UI's own status display.
	LatestGatewayToken(ctx context.Context, org, backendID string) (GatewayToken, error)

	AddAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, org string, limit int) ([]AuditEvent, error)
	// AuditSince returns audit rows of every organisation with an id above afterID, oldest first
	// (the graph projection reads the trail this way and remembers where it stopped).
	AuditSince(ctx context.Context, afterID int64, limit int) ([]AuditEvent, error)

	// AddDecisions appends the rows one decider run produced, in a single transaction. Each row's OrgID
	// and At are the caller's to set (see DecisionLog); an empty slice is a no-op. There is no per-row
	// AddDecision: nothing writes just one of these outside a test, since a run compares at least one
	// decider's worth of moves at a time.
	AddDecisions(ctx context.Context, ds []DecisionLog) error
	// ListDecisions returns one organisation's decision log, newest first, limited the same way ListAudit is.
	ListDecisions(ctx context.Context, org string, limit int) ([]DecisionLog, error)

	// SaveSnapshot / LoadSnapshot keep the last full set of facts an agent reported,
	// so the server can serve state immediately after a restart.
	SaveSnapshot(ctx context.Context, agentID string, data []byte, now time.Time) error
	LoadSnapshot(ctx context.Context, agentID string) ([]byte, time.Time, error)

	// Accounts are global; the name is unique across the whole server, ignoring case.
	CreateUser(ctx context.Context, u User) error
	GetUser(ctx context.Context, id string) (User, error)
	GetUserByName(ctx context.Context, username string) (User, error)
	CountUsers(ctx context.Context) (int, error)
	// DeleteUser removes an account together with its sessions and memberships (used to undo a half-finished registration).
	DeleteUser(ctx context.Context, id string) error
	SetPassword(ctx context.Context, id, hash string, mustChange bool) error
	SetDisabled(ctx context.Context, id string, at *time.Time) error
	MarkLogin(ctx context.Context, id string, now time.Time) error
	// SetTOTP replaces the account's whole two-factor state in one write: a pending setup (secret set,
	// enabledAt nil, recovery nil), turning it on (enabledAt set, recovery the fresh codes), spending one
	// recovery code (secret and enabledAt unchanged, recovery one shorter), or turning it off (all three
	// zeroed). There being one setter rather than four keeps "what does 2FA state even look like right now"
	// answerable from a single row instead of several independent flags that could drift out of sync.
	SetTOTP(ctx context.Context, id, secret string, enabledAt *time.Time, recovery []string) error
	// SetEmail replaces the account's email address and its verified-at timestamp together: verifiedAt nil
	// always means the address just set has not been confirmed yet, and setting it also turns off
	// EmailOTPEnabledAt (a second factor cannot keep pointing at an address nobody has proven receives mail).
	// Confirming an address calls this again with the same email and now's time.
	SetEmail(ctx context.Context, id, email string, verifiedAt *time.Time) error
	// SetEmailOTPEnabled turns email-as-a-second-factor on (enabledAt non-nil) or off (nil), independently of
	// the address itself. The caller is responsible for only turning it on once EmailVerifiedAt is set.
	SetEmailOTPEnabled(ctx context.Context, id string, enabledAt *time.Time) error
	// AddWebAuthnCredential appends one newly-registered passkey or security key. ErrExists if this account
	// already has a credential with the same CredentialID (a browser retrying a registration it already
	// completed, say).
	AddWebAuthnCredential(ctx context.Context, id string, cred WebAuthnCredential) error
	// RenameWebAuthnCredential changes only a credential's own label. ErrNotFound if id has no credential
	// with that CredentialID.
	RenameWebAuthnCredential(ctx context.Context, id string, credentialID []byte, name string) error
	// TouchWebAuthnCredential updates one credential's sign counter and last-used time after a login that
	// used it. The caller must have already rejected the login if signCount failed to advance.
	TouchWebAuthnCredential(ctx context.Context, id string, credentialID []byte, signCount uint32, usedAt time.Time) error
	// RemoveWebAuthnCredential deletes one credential. Removing the last one simply leaves the account with
	// none - there being no separate enabled flag means there is nothing else to turn off.
	RemoveWebAuthnCredential(ctx context.Context, id string, credentialID []byte) error

	// ---- tenants ----

	// CreateOrg inserts the organisation and its first owner in one transaction. ErrExists if the id is taken.
	CreateOrg(ctx context.Context, o Org, ownerID string) error
	GetOrg(ctx context.Context, id string) (Org, error)
	ListOrgs(ctx context.Context) ([]Org, error)
	RenameOrg(ctx context.Context, id, name string) error
	// DeleteOrg removes the organisation and everything that belongs to it.
	DeleteOrg(ctx context.Context, id string) error
	ListMyOrgs(ctx context.Context, userID string) ([]MyOrg, error)
	GetMembership(ctx context.Context, org, userID string) (Membership, error)
	ListMembers(ctx context.Context, org string) ([]Member, error)
	AddMember(ctx context.Context, m Membership) error
	SetMemberRole(ctx context.Context, org, userID, role string) error
	RemoveMember(ctx context.Context, org, userID string) error
	// CountOwners counts the active (not disabled) owners of an organisation.
	CountOwners(ctx context.Context, org string) (int, error)

	CreateInvite(ctx context.Context, inv Invite, hash []byte) error
	ListInvites(ctx context.Context, org string) ([]Invite, error)
	RevokeInvite(ctx context.Context, org, id string) error
	// PeekInvite returns the invite behind a secret if it is still usable, without consuming it.
	PeekInvite(ctx context.Context, hash []byte, now time.Time) (Invite, error)
	// UseInvite atomically consumes the invite and adds the person to its organisation (or fails with
	// ErrTokenInvalid and changes nothing). A person who is already a member keeps their role and the invite is not consumed (ErrExists).
	UseInvite(ctx context.Context, hash []byte, userID string, now time.Time) (Invite, error)

	CreateSession(ctx context.Context, hash []byte, userID, ip string, now, expires time.Time) error
	// LookupSession returns ErrNotFound for an unknown hash. Expiry is the caller's decision.
	LookupSession(ctx context.Context, hash []byte) (Session, User, error)
	TouchSession(ctx context.Context, hash []byte, now time.Time) error
	DeleteSession(ctx context.Context, hash []byte) error
	// DeleteUserSessions signs a user out everywhere, optionally keeping one session (except may be nil).
	DeleteUserSessions(ctx context.Context, userID string, except []byte) error
	PurgeSessions(ctx context.Context, olderThan time.Time) error

	// CreateAPIToken records a freshly minted personal access token; id and hash are the caller's to
	// generate (see server.NewAPITokenSecret), matching how sessions and invites are created.
	CreateAPIToken(ctx context.Context, id string, hash []byte, userID, name string, now time.Time) error
	// ListAPITokens lists one person's own tokens, newest first. Never returns a secret or hash - only
	// what CreateAPIToken already committed the person to seeing again.
	ListAPITokens(ctx context.Context, userID string) ([]APIToken, error)
	// LookupAPIToken resolves a token's hash to the account it belongs to. ErrNotFound for an unknown
	// (or already-revoked) hash.
	LookupAPIToken(ctx context.Context, hash []byte) (APIToken, User, error)
	TouchAPIToken(ctx context.Context, hash []byte, now time.Time) error
	// RevokeAPIToken deletes one of a person's own tokens. ErrNotFound if id does not belong to userID,
	// so one account can never revoke another's by guessing an id.
	RevokeAPIToken(ctx context.Context, userID, id string) error

	// GetWorkspace returns Rev 0 and no data when nothing was ever saved.
	GetWorkspace(ctx context.Context, org string) (Workspace, error)
	// PutWorkspace saves data only if the stored revision is still expectRev, and returns the new
	// state. On a mismatch it returns ErrConflict together with the current stored workspace.
	PutWorkspace(ctx context.Context, org string, expectRev int64, data []byte, by string, now time.Time) (Workspace, error)

	// ---- history ----

	// AddHistory stores one compressed topology snapshot taken at `at` (an existing one at that instant is replaced).
	AddHistory(ctx context.Context, org string, at time.Time, data []byte) error
	// ListHistory returns the index of stored snapshots between since and until (zero = open), oldest first.
	ListHistory(ctx context.Context, org string, since, until time.Time) ([]HistoryPoint, error)
	// GetHistory returns the snapshot taken at or before `at`, or ErrNotFound.
	GetHistory(ctx context.Context, org string, at time.Time) (HistoryPoint, []byte, error)
	DeleteHistory(ctx context.Context, org string, ats []time.Time) error
	// AddEvents stores change events, assigning their ids.
	AddEvents(ctx context.Context, org string, evs []Event) error
	ListEvents(ctx context.Context, org string, q EventQuery) ([]Event, error)
	PruneEvents(ctx context.Context, org string, before time.Time, keepNewest int) error
	// DeleteEvents removes events by id (used once they have been handed to the graph).
	DeleteEvents(ctx context.Context, org string, ids []int64) error

	// GetSettings returns nil data when nothing was ever saved.
	GetSettings(ctx context.Context, org string) ([]byte, error)
	PutSettings(ctx context.Context, org string, data []byte, now time.Time) error

	// ---- mail configuration: server-wide, not scoped to any organisation (see server.Core.Mailer) ----

	// GetMailConfig returns nil data when nothing was ever saved.
	GetMailConfig(ctx context.Context) ([]byte, error)
	PutMailConfig(ctx context.Context, data []byte, now time.Time) error

	// ---- the twin: what was observed and then went away, machine identities, the model version ----

	ListTombstones(ctx context.Context, org string) ([]Tombstone, error)
	PutTombstones(ctx context.Context, org string, ts []Tombstone) error
	DeleteTombstones(ctx context.Context, org string, keys []TombstoneKey) error
	ListIdentities(ctx context.Context, org string) ([]Identity, error)
	PutIdentities(ctx context.Context, org string, ids []Identity) error
	// GetModelState returns ErrNotFound when the organisation's model has never been versioned.
	GetModelState(ctx context.Context, org string) (ModelState, error)
	PutModelState(ctx context.Context, org string, m ModelState) error

	Close() error
}

// HistoryPoint identifies one stored snapshot.
type HistoryPoint struct {
	At    time.Time
	Bytes int
}

// Event is one change worth telling a person about: something appeared, went away, was scaled,
// moved, changed status, or a consistency check found the server's picture out of date.
type Event struct {
	ID          int64
	At          time.Time
	Kind        string // e.g. service-scaled, node-status, cluster-added, drift
	TargetKind  string // cluster | node | service | dependency | agent
	TargetID    string
	Name        string
	ClusterID   string
	ClusterName string
	Detail      string
	Cause       string // what most likely caused it, when that is known ("autoscaler", "node drained")
	Severity    string // info | notice | warning
}

// EventQuery filters ListEvents. Zero values mean no filter.
type EventQuery struct {
	Since, Until time.Time
	Kind         string
	ClusterID    string
	TargetID     string
	Limit        int // default 200, at most 2000
}
