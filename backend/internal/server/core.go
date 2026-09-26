// Package server holds the control-plane logic shared by the gRPC and HTTP front ends.
package server

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

type Kind int

const (
	KindInvalid Kind = iota + 1
	KindUnauthenticated
	KindNotFound
	KindConflict
	KindRateLimited
	KindInternal
	KindForbidden
	// KindTwoFactorRequired is what Login returns instead of a session when the password was right but the
	// account also needs a TOTP code: not a failure, a second step, so callers can tell it apart from a wrong
	// password and prompt for the code instead of showing an error.
	KindTwoFactorRequired
)

// Error carries a category the transport layers map to gRPC codes / HTTP statuses.
type Error struct {
	Kind Kind
	Msg  string
	// Data is extra fields for the JSON answer (for example how many approval attempts are left).
	Data map[string]any
}

func (e *Error) Error() string { return e.Msg }

func errf(k Kind, format string, a ...any) *Error {
	return &Error{Kind: k, Msg: fmt.Sprintf(format, a...)}
}

const (
	MaxTier = 4
	// ImplementedTier is the highest access tier this release honours. Tiers above it
	// (deploy, apply) are reserved; the server refuses to grant them so nothing can
	// be approved that the code does not yet know how to constrain.
	ImplementedTier    = 2
	TokenTTL           = time.Hour
	MinConfirmChars    = 8
	PollIntervalSecond = 3
	// MaxApprovalAttempts is how many codes an administrator may type for one pending agent. The last wrong
	// one rejects the enrollment: a 40-bit code cannot be guessed in five tries, and the agent starts over
	// with a new token rather than leaving the request open to more guesses.
	MaxApprovalAttempts = 5
	// DefaultPendingTTL is how long an enrollment may wait for approval before it expires.
	DefaultPendingTTL = 24 * time.Hour
	// expiredKeep is how long the record of an expired enrollment stays (so the agent can learn it expired and
	// enroll again) before it is deleted.
	expiredKeep = 7 * 24 * time.Hour
)

// A cluster's identity is the UID of its kube-system namespace: always a lowercase UUID. Accepting
// anything looser would let a confused (or hostile) agent claim an arbitrary string as its cluster.
var fingerprintRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type Core struct {
	Store store.Store
	CA    *pki.CA
	OrgID string
	// DefaultOrg names the organisation created together with the first account.
	DefaultOrg string
	Log        *slog.Logger
	EnrollRL   *Limiter // per source address, applied to unauthenticated calls
	// RenewRL limits certificate renewals per agent id (a healthy agent renews twice a day).
	RenewRL *Limiter
	// TapRL limits every enrollment-service request per address before its body is read.
	TapRL  *Limiter
	Now    func() time.Time
	auth   *authState
	userMu *sync.Mutex // serialises membership changes so an organisation always keeps an owner
	// RegMode says who may create an account: RegOpen, RegInvite or RegClosed.
	RegMode string
	// Decider is what the server may connect to on behalf of the external decider (nil: public addresses only).
	Decider *DeciderPolicy
	// PendingTTL is how long an enrollment may wait to be approved (0: DefaultPendingTTL).
	PendingTTL time.Duration
	// RefuseLegacyApproval refuses to approve an agent that enrolled without an approval code (one that
	// predates them). By default such an agent can still be approved by confirming its cluster
	// fingerprint, and the UI and the audit trail mark it as a legacy enrollment.
	RefuseLegacyApproval bool
	// PollAfter is what pending agents are told to wait between polls (0: PollIntervalSecond, 3 seconds).
	PollAfter time.Duration
	// OnRevoke lets the sync hub drop a live stream at once.
	OnRevoke func(agentID string)
	// OnSettings lets the sync hub push new intervals to live streams.
	OnSettings func(Settings)
	// OnWorkspace tells the hub the declared layer was saved, so the effective model is recomputed.
	OnWorkspace func(rev int64)
	// OnOrgDeleted lets the platform stop the organisation's hub once its data is gone.
	OnOrgDeleted func(org string)
	// TrustAgentProxy is set when an L4 load balancer or reverse proxy sits in front of the agent
	// listener and is configured to send a PROXY protocol header ahead of each connection (see
	// proxyproto.go). With it, NewGRPC requires that header on every connection and uses the address it
	// declares - not the TCP socket's own peer, which would be the proxy - for approval cards, rate
	// limits, the audit trail and the location a connecting agent is placed at. Only correct when the
	// proxy is the sole way to reach this port and always sends the header; otherwise a connection could
	// forge its address, or (since the header is then mandatory) a real agent behind a proxy that isn't
	// sending it would be refused outright.
	TrustAgentProxy bool
	// mailer holds the mail configuration behind a pointer, not a plain value, because it is server-wide -
	// shared by every organisation's Core, unlike Settings - and can change at runtime from Settings once an
	// owner of DefaultOrg saves one (see SaveMailConfig). ForOrg's shallow copy (n := *c) copies the pointer,
	// not what it points to, so every tenant sees the same live configuration; a plain MailConfig value here
	// would instead give each tenant its own independently stale copy. Its zero value has Enabled() false:
	// nothing that needs to send mail (RequestEmailVerification, RequestLoginEmailCode) is reachable until an
	// operator configures one, the same way Decider being nil keeps decider-only paths unreachable.
	mailer *mailHolder
	// WebAuthn is the passkey/security key ceremony implementation (see webauthn.go): nil keeps every
	// passkey-only path unreachable, the same way a nil Decider or an unconfigured Mailer does. Unlike
	// Mailer this needs no operator configuration to be worth setting - cmd/server wires in the real one
	// unconditionally - so nil in practice only ever means a build that omitted it (a test, say).
	WebAuthn WebAuthnProvider
	settings *settingsHolder
	// trafficCache is historyTraffic's short-TTL cache for its SQLite slow path (see admin_history.go).
	// Reset per organisation in ForOrg so one organisation's cached traffic can never reach another's.
	trafficCache *trafficCache
}

// ForOrg returns a view of the same server scoped to one organisation. It shares the database, the
// CA, the rate limiters and the sign-in state; what is per organisation is its settings and the
// callbacks its hub registers. The core with OrgID "" is the platform-wide one: it handles accounts
// and the calls that arrive before an agent is known to belong to an organisation.
func (c *Core) ForOrg(org string) *Core {
	n := *c
	n.OrgID = org
	n.settings = &settingsHolder{}
	n.trafficCache = &trafficCache{}
	n.OnRevoke, n.OnSettings, n.OnWorkspace = nil, nil, nil
	return &n
}

func NewCore(st store.Store, ca *pki.CA, org string, log *slog.Logger) *Core {
	if log == nil {
		log = slog.Default()
	}
	return &Core{Store: st, CA: ca, OrgID: org, Log: log, EnrollRL: NewLimiter(20, 10), RenewRL: NewLimiter(1, 5), TapRL: NewLimiter(60, 30), Now: time.Now, auth: newAuthState(), userMu: &sync.Mutex{}, RegMode: RegOpen, settings: &settingsHolder{}, trafficCache: &trafficCache{}, mailer: &mailHolder{}}
}

// audit records something that happened. It is best effort: a failure is logged and the caller carries on.
// Use audited for anything a person with authority does, which must not happen without a record.
func (c *Core) audit(ctx context.Context, actor, action, kind, id, detail string) {
	c.auditOrg(ctx, c.OrgID, actor, action, kind, id, detail)
}

// Bounds on what one audit row may hold, so a long reason or a hostile name cannot bloat the trail.
const (
	maxAuditActor  = 128
	maxAuditTarget = 128
	maxAuditDetail = 500
)

func (c *Core) auditRow(org, actor, action, kind, id, detail string) store.AuditEvent {
	return store.AuditEvent{At: c.Now(), OrgID: org, Actor: printable(actor, maxAuditActor), Action: printable(action, 64), TargetKind: printable(kind, 64),
		TargetID: printable(id, maxAuditTarget), Detail: printable(detail, maxAuditDetail)}
}

// auditOrg writes to one organisation's trail ("" is the server's own, which no tenant can read).
func (c *Core) auditOrg(ctx context.Context, org, actor, action, kind, id, detail string) {
	if err := c.Store.AddAudit(ctx, c.auditRow(org, actor, action, kind, id, detail)); err != nil {
		c.Log.Error("audit write failed", "action", action, "err", err)
	}
}

// audited records that a privileged action is about to happen and then performs it; if the record cannot
// be written the action is not performed and the caller gets an error (fail closed: nothing that needs
// an administrator happens unseen). If the action itself then fails, a second row says so, so the trail
// never claims something that did not take place.
func (c *Core) audited(ctx context.Context, actor, action, kind, id, detail string, do func() error) error {
	if err := c.Store.AddAudit(ctx, c.auditRow(c.OrgID, actor, action, kind, id, detail)); err != nil {
		c.Log.Error("audit write failed; the action was not carried out", "action", action, "err", err)
		return errf(KindInternal, "the audit trail could not be written, so nothing was changed. Check the server's database and try again")
	}
	if err := do(); err != nil {
		c.audit(ctx, actor, action+"-failed", kind, id, err.Error())
		return err
	}
	return nil
}

// CreateToken issues a one-time enrollment token. The secret is returned once and never stored.
func (c *Core) CreateToken(ctx context.Context, actor, label string, tier int) (string, store.Token, error) {
	return c.CreateTokenFor(ctx, actor, label, tier, "")
}

// CreateTokenFor is CreateToken for a token that only the cluster with this identity (the UID of its
// kube-system namespace) may use; empty means any cluster.
func (c *Core) CreateTokenFor(ctx context.Context, actor, label string, tier int, expectedCluster string) (string, store.Token, error) {
	expectedCluster = strings.ToLower(strings.TrimSpace(expectedCluster))
	if expectedCluster != "" && !fingerprintRe.MatchString(expectedCluster) {
		return "", store.Token{}, errf(KindInvalid, "the expected cluster is the UID of its kube-system namespace, like 3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c (kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')")
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 80 {
		return "", store.Token{}, errf(KindInvalid, "name the cluster (1-80 characters)")
	}
	if tier < 0 || tier > ImplementedTier {
		return "", store.Token{}, errf(KindInvalid, "access tier must be 0-%d in this release", ImplementedTier)
	}
	secret, err := NewTokenSecret()
	if err != nil {
		return "", store.Token{}, err
	}
	now := c.Now()
	t := store.Token{ID: newTokenID(), OrgID: c.OrgID, Label: label, AccessTier: tier, CreatedBy: actor, CreatedAt: now, ExpiresAt: now.Add(TokenTTL), ExpectedFingerprint: expectedCluster}
	detail := fmt.Sprintf("for %q, up to tier %d", label, tier)
	if expectedCluster != "" {
		detail += ", only for cluster " + shortFP(expectedCluster)
	}
	if err := c.audited(ctx, actor, "token-created", "token", t.ID, detail, func() error {
		return c.Store.CreateToken(ctx, t, HashSecret(secret))
	}); err != nil {
		return "", store.Token{}, err
	}
	return secret, t, nil
}

// DeleteToken withdraws an enrollment token that was not used yet.
func (c *Core) DeleteToken(ctx context.Context, actor, id string) error {
	return c.audited(ctx, actor, "token-deleted", "token", id, "", func() error {
		if err := c.Store.DeleteToken(ctx, c.OrgID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such unused token")
			}
			return err
		}
		return nil
	})
}

// agentInOrg finds an agent of this organisation. An agent of another one is reported as not existing.
func (c *Core) agentInOrg(ctx context.Context, id string) (store.Agent, error) {
	a, err := c.Store.GetAgent(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && a.OrgID != c.OrgID) {
		return store.Agent{}, errf(KindNotFound, "no such agent")
	}
	return a, err
}

// maxReason bounds the reason an administrator gives when refusing or revoking an agent. It is stored on
// the agent, shown to the agent's operator, and written to the audit trail.
const maxReason = 200

func (c *Core) Reject(ctx context.Context, actor, agentID, reason string) error {
	if _, err := c.agentInOrg(ctx, agentID); err != nil {
		return err
	}
	reason = printable(reason, maxReason)
	return c.audited(ctx, actor, "agent-rejected", "agent", agentID, reason, func() error {
		if err := c.Store.RejectAgent(ctx, agentID, reason, c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "only pending agents can be rejected")
			}
			return err
		}
		return nil
	})
}

// Revoke takes effect on the agent's next call and drops its live stream immediately.
func (c *Core) Revoke(ctx context.Context, actor, agentID, reason string) error {
	if _, err := c.agentInOrg(ctx, agentID); err != nil {
		return err
	}
	reason = printable(reason, maxReason)
	err := c.audited(ctx, actor, "agent-revoked", "agent", agentID, reason, func() error {
		if err := c.Store.RevokeAgent(ctx, agentID, reason, c.Now()); err != nil {
			if errors.Is(err, store.ErrBadState) {
				return errf(KindConflict, "agent is not active")
			}
			return err
		}
		return nil
	})
	if err == nil && c.OnRevoke != nil {
		c.OnRevoke(agentID)
	}
	return err
}

// AuthorizeAgent is called on every authenticated request. Being signed by our CA is not
// enough: the agent must still be approved right now, and belong to this organisation (the
// platform-wide core, OrgID "", accepts any and reports which).
func (c *Core) AuthorizeAgent(ctx context.Context, agentID string) (store.Agent, error) {
	a, err := c.Store.GetAgent(ctx, agentID)
	if err != nil || a.Status != store.StatusApproved || (c.OrgID != "" && a.OrgID != c.OrgID) {
		return store.Agent{}, errf(KindUnauthenticated, "agent is not approved")
	}
	return a, nil
}

// Renew issues a new 24 h certificate for an agent that is still approved.
func (c *Core) Renew(ctx context.Context, agentID string, csrDER []byte) ([]byte, time.Time, error) {
	a, err := c.AuthorizeAgent(ctx, agentID)
	if err != nil {
		return nil, time.Time{}, err
	}
	if c.RenewRL != nil && !c.RenewRL.Allow("renew:"+agentID) {
		return nil, time.Time{}, errf(KindRateLimited, "renewing too often")
	}
	csr, err := pki.ParseCSR(csrDER)
	if err != nil {
		return nil, time.Time{}, errf(KindInvalid, "%v", err)
	}
	leaf, notAfter, err := c.CA.IssueAgent(csr, agentID, a.OrgID, pki.AgentCertTTL)
	if err != nil {
		return nil, time.Time{}, err
	}
	if err := c.Store.SetLeaf(ctx, agentID, leaf, notAfter); err != nil {
		return nil, time.Time{}, err
	}
	_ = c.Store.ClearPollSecret(ctx, agentID)
	return leaf, notAfter, nil
}

// Rejoin issues a new certificate to an agent whose certificate expired while it was offline.
// It needs no token, so it is tightly bounded: the expired certificate must be ours, must have
// expired no more than pki.RejoinWindow ago, the request must be signed by the same key, the
// agent must still be approved right now, and the call is rate limited per address and per agent.
func (c *Core) Rejoin(ctx context.Context, ip string, req *continuumv1.RejoinRequest) (*continuumv1.RenewResponse, error) {
	if !c.EnrollRL.Allow("rejoin:" + LimitKey(ip)) {
		return nil, errf(KindRateLimited, "too many attempts, slow down")
	}
	deny := errf(KindUnauthenticated, "this agent cannot rejoin; enroll again with a new token")
	leaf, err := c.CA.VerifyExpiredAgent(req.ExpiredLeafDer)
	if err != nil {
		return nil, deny
	}
	now := c.Now()
	if len(leaf.Subject.Organization) != 1 || now.Before(leaf.NotBefore) || now.Sub(leaf.NotAfter) > pki.RejoinWindow {
		return nil, deny
	}
	csr, err := pki.ParseCSR(req.CsrDer)
	if err != nil {
		return nil, errf(KindInvalid, "%v", err)
	}
	pub, ok := leaf.PublicKey.(*ecdsa.PublicKey)
	cpub, ok2 := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok || !ok2 || !pub.Equal(cpub) {
		return nil, deny
	}
	id := leaf.Subject.CommonName
	ag, err := c.AuthorizeAgent(ctx, id)
	// The certificate must name the organisation the agent belongs to now.
	if err != nil || leaf.Subject.Organization[0] != ag.OrgID {
		return nil, deny
	}
	if c.RenewRL != nil && !c.RenewRL.Allow("renew:"+id) {
		return nil, errf(KindRateLimited, "renewing too often")
	}
	der, notAfter, err := c.CA.IssueAgent(csr, id, ag.OrgID, pki.AgentCertTTL)
	if err != nil {
		return nil, err
	}
	if err := c.Store.SetLeaf(ctx, id, der, notAfter); err != nil {
		return nil, err
	}
	c.auditOrg(ctx, ag.OrgID, "agent:"+id, "agent-rejoined", "agent", id, fmt.Sprintf("from %s after its certificate expired at %s", ip, rfc(leaf.NotAfter)))
	return &continuumv1.RenewResponse{LeafDer: der, CaDer: c.CA.DER, NotAfter: tsProto(&notAfter)}, nil
}

// printable clips s and replaces control characters, for values that end up in logs and the UI.
func printable(s string, n int) string {
	s = clip(s, n)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// clip cuts s to at most n bytes without splitting a multi-byte character.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func shortFP(fp string) string { return clip(fp, 8) }
