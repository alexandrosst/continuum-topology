package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"continuum/internal/store"
	"continuum/internal/workspace"
)

// Roles inside an organisation, weakest first. A person can hold a different one in each.
const (
	RoleViewer = "viewer" // reads the topology
	RoleEditor = "editor" // and edits the human layer (workspace)
	RoleAdmin  = "admin"  // and manages agents, tokens, settings and editors/viewers
	RoleOwner  = "owner"  // and everything else, including owners and deleting the organisation

	// Who may create accounts.
	RegOpen   = "open"   // anyone; a new account gets an organisation of its own unless it comes with an invite
	RegInvite = "invite" // only with an invitation
	RegClosed = "closed" // nobody through the API; the operator adds accounts on the command line

	// InviteTTL is how long an invitation can be used.
	InviteTTL = 7 * 24 * time.Hour
	// MaxOrgsPerUser bounds what one account can create for itself.
	MaxOrgsPerUser = 10

	sessionPrefix = "cns_"
	// SessionIdle is how long a browser may sit unused before it must sign in again;
	// SessionMax is the hard limit however active it is.
	SessionIdle = 8 * time.Hour
	SessionMax  = 7 * 24 * time.Hour
	// touchEvery avoids a database write on every poll of the UI.
	touchEvery = time.Minute

	maxConcurrentHashes = 4 // each argon2 run needs 64 MiB
	// maxHashQueue is how many further password checks may wait for one of those slots. Beyond it a request
	// is refused at once ("busy"), so a flood cannot pile up unbounded goroutines and request bodies behind the
	// hash workers; hashWait is the longest one may wait.
	maxHashQueue = 32
	hashWait     = 10 * time.Second
)

var usernameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{2,63}$`)

// Principal is the signed-in person behind a request. Org and Role are set only on requests to an
// organisation, and describe this person's standing in that one.
type Principal struct {
	User        store.User
	Org         string
	Role        string
	SessionHash []byte
}

var roleRank = map[string]int{RoleViewer: 1, RoleEditor: 2, RoleAdmin: 3, RoleOwner: 4}

func validRole(r string) bool { return roleRank[r] > 0 }

// authState is the account-related state of Core.
type authState struct {
	loginIP   *Limiter // every login attempt from an address
	loginUser *Limiter // attempts on one account from one address
	regIP     *Limiter // registrations and invitation lookups from one address
	regAll    *Limiter // registrations from anywhere, so a botnet cannot mint accounts without bound
	hashSem   chan struct{}
	hashQueue atomic.Int32 // password checks waiting for a slot in hashSem
	maxQueue  int32
	dummyHash string // verified against when the user does not exist, so timing does not reveal it
	lastPurge time.Time
	failures  *failureTracker
}

func newAuthState() *authState {
	dummy, _ := HashPassword("not-a-real-password")
	return &authState{
		loginIP:   NewLimiter(30, 10),
		loginUser: NewLimiter(6, 5),
		regIP:     NewLimiter(6, 4),
		regAll:    NewLimiter(120, 40),
		hashSem:   make(chan struct{}, maxConcurrentHashes),
		maxQueue:  maxHashQueue,
		dummyHash: dummy,
		failures:  newFailureTracker(),
	}
}

func newSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return sessionPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// errBusy is what a caller gets when the password workers are saturated.
var errBusy = func() *Error {
	return errf(KindRateLimited, "the server is busy checking other sign-ins; try again in a few seconds")
}

// acquireHash takes one of the few password-hashing slots (each run needs 64 MiB, so their number is the
// server's memory bound for this). Waiting is bounded in both number and time.
func (c *Core) acquireHash(ctx context.Context) (release func(), err error) {
	a := c.auth
	select {
	case a.hashSem <- struct{}{}:
		return func() { <-a.hashSem }, nil
	default:
	}
	if a.hashQueue.Add(1) > a.maxQueue {
		a.hashQueue.Add(-1)
		return nil, errBusy()
	}
	defer a.hashQueue.Add(-1)
	t := time.NewTimer(hashWait)
	defer t.Stop()
	select {
	case a.hashSem <- struct{}{}:
		return func() { <-a.hashSem }, nil
	case <-t.C:
		return nil, errBusy()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Core) hashPassword(ctx context.Context, pw string) (string, error) {
	release, err := c.acquireHash(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	return HashPassword(pw)
}

func (c *Core) verifyPassword(ctx context.Context, pw, hash string) (bool, error) {
	release, err := c.acquireHash(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	return VerifyPassword(pw, hash)
}

// Login checks the credentials and opens a session. Account names are unique across the server. The
// error for a wrong username, a wrong password and a disabled account is the same, and the same
// amount of work is done for each.
func (c *Core) Login(ctx context.Context, ip, username, password string) (secret string, u store.User, err error) {
	name := strings.ToLower(strings.TrimSpace(username))
	key := LimitKey(ip)
	if !c.auth.loginIP.Allow(key) || !c.auth.loginUser.Allow(key+"|"+name) {
		return "", store.User{}, errf(KindRateLimited, "too many sign-in attempts, wait a minute")
	}
	if len(password) > MaxPasswordLen || len(name) > 64 {
		return "", store.User{}, errf(KindUnauthenticated, "wrong username or password")
	}
	// Per-account backoff, whichever address the attempts come from. It is kept for every name that is
	// tried, whether or not such an account exists, so what it says (and when) reveals nothing about which
	// accounts are real.
	if wait := c.auth.failures.blocked(name, c.Now()); wait > 0 {
		return "", store.User{}, errf(KindRateLimited, "too many failed sign-in attempts for this account; try again in %s", roundWait(wait))
	}
	u, gerr := c.Store.GetUserByName(ctx, name)
	hash := c.auth.dummyHash
	if gerr == nil {
		hash = u.PasswordHash
	}
	ok, verr := c.verifyPassword(ctx, password, hash)
	if verr != nil && !errors.Is(verr, errBadHash) {
		return "", store.User{}, verr
	}
	if gerr != nil || !ok || u.DisabledAt != nil {
		c.auth.failures.fail(name, c.Now())
		Metrics.authFailures.Add(1)
		c.auditOrg(ctx, "", "anonymous", "login-failed", "user", printable(name, 64), "from "+ip)
		return "", store.User{}, errf(KindUnauthenticated, "wrong username or password")
	}
	c.auth.failures.succeed(name)
	secret, err = c.openSession(ctx, u, ip)
	if err != nil {
		return "", store.User{}, err
	}
	c.auditUser(ctx, u, "login", "", ip)
	return secret, u, nil
}

func (c *Core) openSession(ctx context.Context, u store.User, ip string) (string, error) {
	secret, err := newSessionSecret()
	if err != nil {
		return "", err
	}
	now := c.Now()
	if err := c.Store.CreateSession(ctx, HashSecret(secret), u.ID, ip, now, now.Add(SessionMax)); err != nil {
		return "", err
	}
	_ = c.Store.MarkLogin(ctx, u.ID, now)
	if now.Sub(c.auth.lastPurge) > time.Hour {
		c.auth.lastPurge = now
		_ = c.Store.PurgeSessions(ctx, now.Add(-SessionIdle))
	}
	return secret, nil
}

// auditUser records something a person did in their own name. Every organisation they belong to gets
// the event (so each can see when its members sign in) WITHOUT the network address; the address goes
// only to the server's own trail (organisation ""), which no organisation's members can read. Members of
// an organisation are not entitled to learn where a colleague, who may also belong to other
// organisations, signs in from.
func (c *Core) auditUser(ctx context.Context, u store.User, action, detail, ip string) {
	orgs, _ := c.Store.ListMyOrgs(ctx, u.ID)
	for _, o := range orgs {
		c.auditOrg(ctx, o.ID, u.Username, action, "user", u.ID, detail)
	}
	server := detail
	if ip != "" {
		if server != "" {
			server += " "
		}
		server += "from " + ip
	}
	c.auditOrg(ctx, "", u.Username, action, "user", u.ID, server)
}

// Authenticate resolves a session secret to a person, or fails. Expiry, idleness and a disabled
// account all end the session on the spot. It says nothing about any organisation.
func (c *Core) Authenticate(ctx context.Context, secret string) (Principal, error) {
	deny := errf(KindUnauthenticated, "sign in required")
	if !strings.HasPrefix(secret, sessionPrefix) || len(secret) != len(sessionPrefix)+43 {
		return Principal{}, deny
	}
	h := HashSecret(secret)
	se, u, err := c.Store.LookupSession(ctx, h)
	if err != nil {
		return Principal{}, deny
	}
	now := c.Now()
	if now.After(se.ExpiresAt) || now.Sub(se.LastUsed) > SessionIdle || u.DisabledAt != nil {
		_ = c.Store.DeleteSession(ctx, h)
		return Principal{}, deny
	}
	if now.Sub(se.LastUsed) > touchEvery {
		_ = c.Store.TouchSession(ctx, h, now)
	}
	return Principal{User: u, SessionHash: h}, nil
}

// Member places a signed-in person inside one organisation. A person who does not belong to it gets
// the same "no such organisation" as for one that does not exist, so ids cannot be probed.
func (c *Core) Member(ctx context.Context, p Principal, org string) (Principal, error) {
	m, err := c.Store.GetMembership(ctx, org, p.User.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return Principal{}, errf(KindNotFound, "no such organisation")
		}
		return Principal{}, err
	}
	p.Org, p.Role = org, m.Role
	return p, nil
}

func (c *Core) Logout(ctx context.Context, p Principal) {
	_ = c.Store.DeleteSession(ctx, p.SessionHash)
	c.auditUser(ctx, p.User, "logout", "", "")
}

// ChangePassword lets a person replace their own password. Every other browser they were signed
// in on is signed out.
func (c *Core) ChangePassword(ctx context.Context, p Principal, current, next string) error {
	if !c.auth.loginUser.Allow("pw|" + p.User.ID) {
		return errf(KindRateLimited, "too many attempts, wait a minute")
	}
	u, err := c.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		return errf(KindUnauthenticated, "sign in required")
	}
	ok, verr := c.verifyPassword(ctx, current, u.PasswordHash)
	if verr != nil || !ok {
		c.auditUser(ctx, u, "password-change-refused", "current password was wrong", "")
		return errf(KindInvalid, "your current password is not correct")
	}
	if err := CheckPasswordPolicy(u.Username, next); err != nil {
		return errf(KindInvalid, "%v", err)
	}
	if current == next {
		return errf(KindInvalid, "choose a different password")
	}
	hash, err := c.hashPassword(ctx, next)
	if err != nil {
		return err
	}
	if err := c.Store.SetPassword(ctx, u.ID, hash, false); err != nil {
		return err
	}
	_ = c.Store.DeleteUserSessions(ctx, u.ID, p.SessionHash)
	c.auditUser(ctx, u, "password-changed", "", "")
	return nil
}

// ---- registration, organisations and invitations ----

func newInviteSecret() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return invitePrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

const invitePrefix = "cni_"

func looksLikeInvite(s string) bool {
	return strings.HasPrefix(s, invitePrefix) && len(s) == len(invitePrefix)+32
}

var errBadInvite = func() *Error { return errf(KindInvalid, "this invitation is invalid, expired or already used") }

// PreviewInvite tells someone holding an invitation what it is for, before they sign up.
func (c *Core) PreviewInvite(ctx context.Context, ip, secret string) (store.Invite, string, error) {
	if !c.auth.regIP.Allow("inv|" + LimitKey(ip)) {
		return store.Invite{}, "", errf(KindRateLimited, "too many attempts, wait a minute")
	}
	if !looksLikeInvite(secret) {
		return store.Invite{}, "", errBadInvite()
	}
	inv, err := c.Store.PeekInvite(ctx, HashSecret(secret), c.Now())
	if err != nil {
		return store.Invite{}, "", errBadInvite()
	}
	o, err := c.Store.GetOrg(ctx, inv.OrgID)
	if err != nil {
		return store.Invite{}, "", errBadInvite()
	}
	return inv, o.Name, nil
}

func cleanOrgName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if l := len([]rune(name)); l < 2 || l > 60 {
		return "", errf(KindInvalid, "name the organisation (2-60 characters)")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", errf(KindInvalid, "the name contains control characters")
		}
	}
	return name, nil
}

// Register creates an account and signs it in. Without an invitation the new person also gets an
// organisation of their own, which they own; with one they only join the organisation it names.
// Who may register at all is the server's RegMode.
func (c *Core) Register(ctx context.Context, ip, username, password, invite, orgName string) (string, store.User, error) {
	if !c.auth.regIP.Allow("reg|"+LimitKey(ip)) || !c.auth.regAll.Allow("reg") {
		return "", store.User{}, errf(KindRateLimited, "too many attempts, wait a minute")
	}
	username = strings.TrimSpace(username)
	if !usernameRe.MatchString(username) {
		return "", store.User{}, errf(KindInvalid, "username must be 3-64 characters: letters, digits and . _ @ -")
	}
	if err := CheckPasswordPolicy(username, password); err != nil {
		return "", store.User{}, errf(KindInvalid, "%v", err)
	}
	var inv *store.Invite
	switch {
	case c.RegMode == RegClosed:
		return "", store.User{}, errf(KindForbidden, "this server is not accepting new accounts")
	case invite != "":
		i, _, err := c.PreviewInvite(ctx, ip, invite)
		if err != nil {
			return "", store.User{}, err
		}
		inv = &i
	case c.RegMode == RegInvite:
		return "", store.User{}, errf(KindForbidden, "this server is invitation-only: ask an administrator of your organisation for an invitation")
	}
	if inv == nil {
		var err error
		if orgName == "" {
			orgName = username + "'s organisation"
		}
		if orgName, err = cleanOrgName(orgName); err != nil {
			return "", store.User{}, err
		}
	}
	hash, err := c.hashPassword(ctx, password)
	if err != nil {
		return "", store.User{}, err
	}
	now := c.Now()
	u := store.User{ID: "u-" + randHex(6), Username: username, PasswordHash: hash, CreatedAt: now}
	if err := c.Store.CreateUser(ctx, u); errors.Is(err, store.ErrExists) {
		return "", store.User{}, errf(KindConflict, "that username is taken")
	} else if err != nil {
		return "", store.User{}, err
	}
	rollback := func() { _ = c.Store.DeleteUser(ctx, u.ID) }
	if inv != nil {
		used, err := c.Store.UseInvite(ctx, HashSecret(invite), u.ID, now)
		if err != nil {
			rollback()
			return "", store.User{}, errBadInvite()
		}
		c.auditOrg(ctx, used.OrgID, username, "user-registered", "user", u.ID, fmt.Sprintf("joined as %s with an invitation from %s", used.Role, c.nameOf(ctx, used.CreatedBy)))
	} else {
		o := store.Org{ID: newOrgID(), Name: orgName, CreatedAt: now, CreatedBy: u.ID}
		if err := c.Store.CreateOrg(ctx, o, u.ID); err != nil {
			rollback()
			return "", store.User{}, err
		}
		c.auditOrg(ctx, o.ID, username, "org-created", "org", o.ID, fmt.Sprintf("%q, registered by %s", o.Name, username))
	}
	secret, err := c.openSession(ctx, u, ip)
	if err != nil {
		return "", store.User{}, err
	}
	return secret, u, nil
}

func newOrgID() string { return "org-" + randHex(6) }

func (c *Core) nameOf(ctx context.Context, userID string) string {
	if u, err := c.Store.GetUser(ctx, userID); err == nil {
		return u.Username
	}
	return "someone who has left"
}

// CreateOrg gives a signed-in person another organisation of their own (only where registration is open).
func (c *Core) CreateOrg(ctx context.Context, p Principal, name string) (store.Org, error) {
	if c.RegMode != RegOpen {
		return store.Org{}, errf(KindForbidden, "this server does not let people create organisations")
	}
	name, err := cleanOrgName(name)
	if err != nil {
		return store.Org{}, err
	}
	mine, err := c.Store.ListMyOrgs(ctx, p.User.ID)
	if err != nil {
		return store.Org{}, err
	}
	owned := 0
	for _, o := range mine {
		if o.Role == RoleOwner {
			owned++
		}
	}
	if owned >= MaxOrgsPerUser {
		return store.Org{}, errf(KindConflict, "you already own %d organisations", owned)
	}
	o := store.Org{ID: newOrgID(), Name: name, CreatedAt: c.Now(), CreatedBy: p.User.ID}
	if err := c.Store.CreateOrg(ctx, o, p.User.ID); err != nil {
		return store.Org{}, err
	}
	c.auditOrg(ctx, o.ID, p.User.Username, "org-created", "org", o.ID, fmt.Sprintf("%q", o.Name))
	return o, nil
}

// AcceptInvite adds a signed-in person to the organisation an invitation names.
func (c *Core) AcceptInvite(ctx context.Context, ip string, p Principal, secret string) (store.Org, string, error) {
	if !c.auth.regIP.Allow("inv|" + LimitKey(ip)) {
		return store.Org{}, "", errf(KindRateLimited, "too many attempts, wait a minute")
	}
	if !looksLikeInvite(secret) {
		return store.Org{}, "", errBadInvite()
	}
	inv, err := c.Store.UseInvite(ctx, HashSecret(secret), p.User.ID, c.Now())
	switch {
	case errors.Is(err, store.ErrExists):
		return store.Org{}, "", errf(KindConflict, "you already belong to that organisation")
	case err != nil:
		return store.Org{}, "", errBadInvite()
	}
	o, err := c.Store.GetOrg(ctx, inv.OrgID)
	if err != nil {
		return store.Org{}, "", err
	}
	c.auditOrg(ctx, o.ID, p.User.Username, "member-joined", "user", p.User.ID, fmt.Sprintf("as %s, invited by %s", inv.Role, c.nameOf(ctx, inv.CreatedBy)))
	return o, inv.Role, nil
}

// canGrant reports whether someone with role `by` may give (or take away) `target`.
func canGrant(by, target string) bool {
	switch by {
	case RoleOwner:
		return validRole(target)
	case RoleAdmin:
		return target == RoleEditor || target == RoleViewer
	}
	return false
}

// CreateInvite makes a one-time invitation to this organisation. The secret is returned once.
func (c *Core) CreateInvite(ctx context.Context, p Principal, role, label string) (string, store.Invite, error) {
	label = strings.TrimSpace(label)
	if len(label) > 80 {
		return "", store.Invite{}, errf(KindInvalid, "the note is at most 80 characters")
	}
	if !validRole(role) {
		return "", store.Invite{}, errf(KindInvalid, "role must be owner, admin, editor or viewer")
	}
	if !canGrant(p.Role, role) {
		return "", store.Invite{}, errf(KindForbidden, "your role cannot invite someone as %s", role)
	}
	secret, err := newInviteSecret()
	if err != nil {
		return "", store.Invite{}, err
	}
	now := c.Now()
	inv := store.Invite{ID: "inv-" + randHex(5), OrgID: c.OrgID, Role: role, Label: label, CreatedBy: p.User.ID, CreatedAt: now, ExpiresAt: now.Add(InviteTTL)}
	if err := c.audited(ctx, p.User.Username, "invite-created", "invite", inv.ID, fmt.Sprintf("as %s%s", role, forLabel(label)), func() error {
		return c.Store.CreateInvite(ctx, inv, HashSecret(secret))
	}); err != nil {
		return "", store.Invite{}, err
	}
	return secret, inv, nil
}

func forLabel(l string) string {
	if l == "" {
		return ""
	}
	return fmt.Sprintf(" for %q", l)
}

func (c *Core) RevokeInvite(ctx context.Context, p Principal, id string) error {
	invs, err := c.Store.ListInvites(ctx, c.OrgID)
	if err != nil {
		return err
	}
	for _, i := range invs {
		if i.ID == id && !canGrant(p.Role, i.Role) {
			return errf(KindForbidden, "your role cannot withdraw an invitation to %s", i.Role)
		}
	}
	return c.audited(ctx, p.User.Username, "invite-revoked", "invite", id, "", func() error {
		if err := c.Store.RevokeInvite(ctx, c.OrgID, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return errf(KindNotFound, "no such open invitation")
			}
			return err
		}
		return nil
	})
}

// SetMemberRole changes what someone may do here. Owners can set any role and change anyone; an
// administrator can only move editors and viewers between editor and viewer. Nobody changes their
// own role, and the last owner cannot be demoted.
func (c *Core) SetMemberRole(ctx context.Context, p Principal, userID, role string) error {
	if !validRole(role) {
		return errf(KindInvalid, "role must be owner, admin, editor or viewer")
	}
	c.userMu.Lock()
	defer c.userMu.Unlock()
	m, u, err := c.member(ctx, userID)
	if err != nil {
		return err
	}
	if u.ID == p.User.ID {
		return errf(KindConflict, "you cannot change your own role")
	}
	if !canGrant(p.Role, m.Role) || !canGrant(p.Role, role) {
		return errf(KindForbidden, "your role cannot make this change")
	}
	if m.Role == RoleOwner && role != RoleOwner {
		if err := c.needAnotherOwner(ctx, u); err != nil {
			return err
		}
	}
	return c.audited(ctx, p.User.Username, "member-role-changed", "user", u.ID, fmt.Sprintf("%s: %s → %s", u.Username, m.Role, role), func() error {
		return c.Store.SetMemberRole(ctx, c.OrgID, u.ID, role)
	})
}

// RemoveMember takes someone out of the organisation, or lets a person leave it. They keep their
// account and their other organisations.
func (c *Core) RemoveMember(ctx context.Context, p Principal, userID string) error {
	c.userMu.Lock()
	defer c.userMu.Unlock()
	m, u, err := c.member(ctx, userID)
	if err != nil {
		return err
	}
	self := u.ID == p.User.ID
	if !self && !canGrant(p.Role, m.Role) {
		return errf(KindForbidden, "your role cannot remove this person")
	}
	if m.Role == RoleOwner {
		if err := c.needAnotherOwner(ctx, u); err != nil {
			return err
		}
	}
	action, detail := "member-removed", fmt.Sprintf("%s (%s)", u.Username, m.Role)
	if self {
		action, detail = "member-left", ""
	}
	return c.audited(ctx, p.User.Username, action, "user", u.ID, detail, func() error {
		return c.Store.RemoveMember(ctx, c.OrgID, u.ID)
	})
}

func (c *Core) member(ctx context.Context, userID string) (store.Membership, store.User, error) {
	m, err := c.Store.GetMembership(ctx, c.OrgID, userID)
	if errors.Is(err, store.ErrNotFound) {
		return m, store.User{}, errf(KindNotFound, "no such member")
	}
	if err != nil {
		return m, store.User{}, err
	}
	u, err := c.Store.GetUser(ctx, userID)
	return m, u, err
}

func (c *Core) needAnotherOwner(ctx context.Context, u store.User) error {
	n, err := c.Store.CountOwners(ctx, c.OrgID)
	if err != nil {
		return err
	}
	if n <= 1 && u.DisabledAt == nil {
		return errf(KindConflict, "the organisation must keep at least one owner: make someone else an owner first")
	}
	return nil
}

// RenameOrg changes the display name.
func (c *Core) RenameOrg(ctx context.Context, p Principal, name string) error {
	name, err := cleanOrgName(name)
	if err != nil {
		return err
	}
	return c.audited(ctx, p.User.Username, "org-renamed", "org", c.OrgID, fmt.Sprintf("%q", name), func() error {
		return c.Store.RenameOrg(ctx, c.OrgID, name)
	})
}

// DeleteOrg erases the organisation with its agents, topology, history and members. The caller must
// type its name. The audit trail is kept, so it stays possible to see who deleted it.
func (c *Core) DeleteOrg(ctx context.Context, p Principal, confirm string) error {
	o, err := c.Store.GetOrg(ctx, c.OrgID)
	if err != nil {
		return errf(KindNotFound, "no such organisation")
	}
	if strings.TrimSpace(confirm) != o.Name {
		return errf(KindInvalid, "type the organisation's name to confirm")
	}
	if err := c.audited(ctx, p.User.Username, "org-deleted", "org", o.ID, fmt.Sprintf("%q with everything in it", o.Name), func() error {
		return c.Store.DeleteOrg(ctx, c.OrgID)
	}); err != nil {
		return err
	}
	if c.OnOrgDeleted != nil {
		c.OnOrgDeleted(o.ID)
	}
	return nil
}

// BootstrapAdmin creates the first account, "admin", and an organisation it owns, on a fresh
// database. With an empty password a random one is generated and returned (shown once, and must be
// changed at first sign-in); an operator-supplied password (for example from a container secret) is
// accepted as chosen. It does nothing when any account already exists.
func (c *Core) BootstrapAdmin(ctx context.Context, password string) (created bool, generated string, err error) {
	n, err := c.Store.CountUsers(ctx)
	if err != nil || n > 0 {
		return false, "", err
	}
	pw, must := password, false
	if pw == "" {
		if pw, err = randomPassword(); err != nil {
			return false, "", err
		}
		must, generated = true, pw
	} else if err := CheckPasswordPolicy("admin", pw); err != nil {
		return false, "", fmt.Errorf("CONTINUUM_ADMIN_PASSWORD: %w", err)
	}
	hash, err := c.hashPassword(ctx, pw)
	if err != nil {
		return false, "", err
	}
	now := c.Now()
	u := store.User{ID: "u-" + randHex(6), Username: "admin", PasswordHash: hash, MustChange: must, CreatedAt: now}
	if err := c.Store.CreateUser(ctx, u); err != nil {
		return false, "", err
	}
	o := store.Org{ID: firstNonEmpty(c.DefaultOrg, "default"), Name: "Default", CreatedAt: now, CreatedBy: u.ID}
	if err := c.Store.CreateOrg(ctx, o, u.ID); err != nil {
		return false, "", err
	}
	c.auditOrg(ctx, o.ID, "system", "user-created", "user", u.ID, "first administrator")
	return true, generated, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// RecoverPassword is the offline recovery path (server binary, run on the host): it resets the
// named account, or creates it if it does not exist. A new account is given no organisation; add
// one with CreateOrgFor.
func (c *Core) RecoverPassword(ctx context.Context, username string) (string, error) {
	pw, err := randomPassword()
	if err != nil {
		return "", err
	}
	hash, err := c.hashPassword(ctx, pw)
	if err != nil {
		return "", err
	}
	u, err := c.Store.GetUserByName(ctx, strings.ToLower(username))
	switch {
	case errors.Is(err, store.ErrNotFound):
		if !usernameRe.MatchString(username) {
			return "", errors.New("invalid username")
		}
		u = store.User{ID: "u-" + randHex(6), Username: username, PasswordHash: hash, MustChange: true, CreatedAt: c.Now()}
		if err := c.Store.CreateUser(ctx, u); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if err := c.Store.SetPassword(ctx, u.ID, hash, true); err != nil {
			return "", err
		}
		_ = c.Store.SetDisabled(ctx, u.ID, nil)
		_ = c.Store.DeleteUserSessions(ctx, u.ID, nil)
	}
	c.auditUser(ctx, u, "password-recovered", "server command line", "")
	return pw, nil
}

// CreateOrgFor is the operator's way to make an organisation for an existing account (or join
// one to an existing organisation), whatever the registration mode. name is the display name.
func (c *Core) CreateOrgFor(ctx context.Context, username, name string) (store.Org, error) {
	u, err := c.Store.GetUserByName(ctx, strings.ToLower(username))
	if err != nil {
		return store.Org{}, fmt.Errorf("no account named %q", username)
	}
	name, err = cleanOrgName(name)
	if err != nil {
		return store.Org{}, err
	}
	o := store.Org{ID: newOrgID(), Name: name, CreatedAt: c.Now(), CreatedBy: u.ID}
	if err := c.Store.CreateOrg(ctx, o, u.ID); err != nil {
		return store.Org{}, err
	}
	c.auditOrg(ctx, o.ID, "system", "org-created", "org", o.ID, fmt.Sprintf("%q for %s (server command line)", name, u.Username))
	return o, nil
}

// ---- workspace ----

// MaxWorkspaceBytes bounds the stored document.
const MaxWorkspaceBytes = 8 << 20

// SaveWorkspace stores the document if expectRev is still current. On a conflict it returns the
// stored workspace with a KindConflict error.
func (c *Core) SaveWorkspace(ctx context.Context, actor string, expectRev int64, data []byte) (store.Workspace, error) {
	if len(data) > MaxWorkspaceBytes {
		return store.Workspace{}, errf(KindInvalid, "workspace is larger than %d MB", MaxWorkspaceBytes>>20)
	}
	if err := checkWorkspaceJSON(data); err != nil {
		return store.Workspace{}, errf(KindInvalid, "%v", err)
	}
	// Only what people declared is stored. Observed facts belong to the agents and are held apart from the workspace;
	// a document from an older client that still carries them is reduced to what is declared, with a note.
	data, rep, err := workspace.Declare(data)
	var newer workspace.ErrNewer
	if errors.As(err, &newer) {
		return store.Workspace{}, &Error{Kind: KindInvalid, Msg: err.Error(), Data: map[string]any{"supportedVersion": workspace.CurrentVersion, "documentVersion": newer.Have}}
	}
	if err != nil {
		return store.Workspace{}, errf(KindInvalid, "%v", err)
	}
	w, err := c.Store.PutWorkspace(ctx, c.OrgID, expectRev, data, actor, c.Now())
	if errors.Is(err, store.ErrConflict) {
		return w, errf(KindConflict, "the workspace was changed by someone else")
	}
	if err != nil {
		return store.Workspace{}, err
	}
	if rep.Changed() {
		w.Note = rep.Note()
	}
	if c.OnWorkspace != nil {
		c.OnWorkspace(w.Rev)
	}
	// One audit entry per save would drown everything else; the workspace has its own revision history in the row.
	return w, nil
}

// checkWorkspaceJSON accepts a JSON object that declares a numeric schemaVersion. The server does
// not interpret anything else: the UI owns the shape and its migrations.
func checkWorkspaceJSON(data []byte) error {
	var probe struct {
		SchemaVersion *int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return errors.New("workspace must be a JSON object")
	}
	if probe.SchemaVersion == nil {
		return errors.New("workspace must declare a schemaVersion")
	}
	return nil
}
