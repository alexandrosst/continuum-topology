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

	AddAudit(ctx context.Context, e AuditEvent) error
	ListAudit(ctx context.Context, org string, limit int) ([]AuditEvent, error)
	// AuditSince returns audit rows of every organisation with an id above afterID, oldest first
	// (the graph projection reads the trail this way and remembers where it stopped).
	AuditSince(ctx context.Context, afterID int64, limit int) ([]AuditEvent, error)

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
