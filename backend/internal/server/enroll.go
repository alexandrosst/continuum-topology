package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/approval"
	"continuum/internal/pki"
	"continuum/internal/store"

	"crypto/subtle"
)

func (c *Core) pendingTTL() time.Duration {
	if c.PendingTTL > 0 {
		return c.PendingTTL
	}
	return DefaultPendingTTL
}

func (c *Core) pollAfter() int32 {
	if c.PollAfter > 0 {
		return int32(max(c.PollAfter/time.Second, 1))
	}
	return PollIntervalSecond
}

// Enroll handles the first, unauthenticated call. ip is the peer address for rate limiting.
//
// It is idempotent. An agent stores its key and approval code before it calls, so a response that never
// arrived (or a crash right after it) leads to the same call again. The token is spent by then, but the
// request is recognised as the same enrollment, because it comes with the same token and the same key,
// and it continues the same pending agent instead of failing or creating a second one. The agent gets a
// fresh poll secret (the first was lost with the response). A different key with a spent token is refused.
func (c *Core) Enroll(ctx context.Context, ip string, req *continuumv1.EnrollRequest) (*continuumv1.EnrollResponse, error) {
	if !c.EnrollRL.Allow("enroll:" + LimitKey(ip)) {
		return nil, errf(KindRateLimited, "too many attempts, slow down")
	}
	if !LooksLikeToken(req.Token) {
		return nil, errf(KindUnauthenticated, "invalid or expired token")
	}
	if !fingerprintRe.MatchString(req.ClusterFingerprint) {
		return nil, errf(KindInvalid, "cluster fingerprint is malformed")
	}
	if _, err := pki.ParseCSR(req.CsrDer); err != nil {
		return nil, errf(KindInvalid, "%v", err)
	}
	if len(req.AgentVersion) > 64 || len(req.KubernetesVersion) > 64 {
		return nil, errf(KindInvalid, "version string too long")
	}
	if n := len(req.ApprovalCodeHash); n != 0 && n != approval.HashLen {
		return nil, errf(KindInvalid, "approval code hash is malformed")
	}
	tier := int(req.InstalledAccessTier)
	if tier > MaxTier {
		return nil, errf(KindInvalid, "installed access tier out of range")
	}
	pollSecret, err := newSecret()
	if err != nil {
		return nil, err
	}
	tokenHash := HashSecret(req.Token)
	a := store.Agent{
		ID:             newAgentID(),
		InstalledTier:  tier,
		Fingerprint:    req.ClusterFingerprint,
		CSR:            req.CsrDer,
		PollSecretHash: HashSecret(pollSecret),
		Version:        printable(req.AgentVersion, 40),
		K8sVersion:     printable(req.KubernetesVersion, 40),
		ConnectingIP:   ip,
		ApprovalHash:   req.ApprovalCodeHash,
	}
	tok, err := c.Store.EnrollAgent(ctx, tokenHash, a, c.Now())
	if errors.Is(err, store.ErrWrongCluster) {
		c.Log.Warn("enrollment refused: the token is bound to another cluster", "ip", ip)
		c.auditOrg(ctx, tok.OrgID, "agent:unenrolled", "enrollment-refused", "token", tok.ID,
			fmt.Sprintf("%q: the token is for cluster %s but the agent reported cluster %s (from %s)", tok.Label, shortFP(tok.ExpectedFingerprint), shortFP(req.ClusterFingerprint), ip))
		return nil, errf(KindForbidden, "this enrollment token was created for a different cluster")
	}
	if errors.Is(err, store.ErrTokenInvalid) {
		if resp, rerr := c.resumeEnrollment(ctx, ip, req, tokenHash, a, pollSecret); rerr != nil {
			return nil, rerr
		} else if resp != nil {
			return resp, nil
		}
		c.Log.Warn("enrollment refused: bad token", "ip", ip)
		return nil, errf(KindUnauthenticated, "invalid or expired token")
	}
	if err != nil {
		return nil, err
	}
	how := "with an approval code"
	if len(a.ApprovalHash) == 0 {
		how = "as a legacy enrollment, without an approval code"
	}
	c.auditOrg(ctx, tok.OrgID, "agent:"+a.ID, "agent-enrolled", "agent", a.ID,
		fmt.Sprintf("%q from %s waiting for approval %s (installed tier %d, cluster %s)", tok.Label, ip, how, tier, shortFP(req.ClusterFingerprint)))
	return c.enrollResponse(a.ID, pollSecret), nil
}

func (c *Core) enrollResponse(agentID, pollSecret string) *continuumv1.EnrollResponse {
	return &continuumv1.EnrollResponse{AgentId: agentID, PollSecret: pollSecret, PollIntervalSeconds: PollIntervalSecond,
		ServerTimeUnix: c.Now().Unix(), PendingTtlSeconds: int32(c.pendingTTL() / time.Second)}
}

// resumeEnrollment recognises a retried enrollment (a spent token, but the agent it was spent on has this
// very key and cluster) and continues it. It returns nil, nil when the request is not a retry.
func (c *Core) resumeEnrollment(ctx context.Context, ip string, req *continuumv1.EnrollRequest, tokenHash []byte, fresh store.Agent, pollSecret string) (*continuumv1.EnrollResponse, error) {
	prev, err := c.Store.AgentByToken(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	oldKey, err1 := approval.KeyID(prev.CSR)
	newKey, err2 := approval.KeyID(req.CsrDer)
	if err1 != nil || err2 != nil || !bytes.Equal(oldKey, newKey) || prev.Fingerprint != req.ClusterFingerprint {
		return nil, nil // somebody else with a spent token: the ordinary refusal
	}
	r := store.Resume{PollSecretHash: fresh.PollSecretHash, ApprovalHash: fresh.ApprovalHash, CSR: fresh.CSR, Version: fresh.Version, K8sVersion: fresh.K8sVersion, IP: ip}
	reopen := false
	switch prev.Status {
	case store.StatusPending, store.StatusApproved:
		// The same enrollment again. A different code under the same key and token is not a retry of it.
		if !bytes.Equal(prev.ApprovalHash, req.ApprovalCodeHash) {
			return nil, errf(KindConflict, "an enrollment with a different approval code is already waiting for this token")
		}
		if prev.Status == store.StatusPending && c.Now().Sub(prev.CreatedAt) > c.pendingTTL() {
			// It ran out while the agent was away; treat it as expired and let it start over.
			c.ExpirePending(ctx, prev.OrgID)
			prev.Status = store.StatusExpired
		}
	case store.StatusExpired:
	default:
		return nil, nil // rejected or revoked: permanent
	}
	if prev.Status == store.StatusExpired {
		reopen = true
	}
	if err := c.Store.ResumeEnrollment(ctx, prev.ID, r, reopen, c.Now()); err != nil {
		if errors.Is(err, store.ErrBadState) {
			return nil, nil // e.g. an approved agent that has already collected its certificate
		}
		return nil, err
	}
	if reopen {
		c.auditOrg(ctx, prev.OrgID, "agent:"+prev.ID, "agent-enrollment-reopened", "agent", prev.ID,
			fmt.Sprintf("%q from %s enrolled again after its request expired; it is waiting for approval with a new code", prev.Name, ip))
	} else {
		c.auditOrg(ctx, prev.OrgID, "agent:"+prev.ID, "agent-enrollment-retried", "agent", prev.ID,
			fmt.Sprintf("%q from %s repeated its enrollment (same token and key): continuing the same agent", prev.Name, ip))
	}
	return c.enrollResponse(prev.ID, pollSecret), nil
}

// ExpirePending expires the organisation's enrollments that nobody approved within the pending lifetime,
// records that, and deletes the records of expired ones nobody reopened. It returns how many expired.
func (c *Core) ExpirePending(ctx context.Context, org string) int {
	now := c.Now()
	expired, err := c.Store.ExpirePending(ctx, org, now.Add(-c.pendingTTL()))
	if err != nil {
		c.Log.Error("expiring pending enrollments failed", "err", err)
		return 0
	}
	for _, a := range expired {
		detail := fmt.Sprintf("%q waited %s for approval and expired. The agent enrolls again on its own and shows a new approval code", a.Name, c.pendingTTL())
		c.auditOrg(ctx, org, "system", "agent-enrollment-expired", "agent", a.ID, detail)
		if err := c.Store.AddEvents(ctx, org, []store.Event{{At: now, Kind: "enrollment-expired", TargetKind: "agent", TargetID: a.ID, Name: a.Name, ClusterName: a.Name,
			Detail: "The request from " + a.Name + " was not approved within " + c.pendingTTL().String() + " and was removed. The agent asks again by itself; look at its log for the new approval code.",
			Cause:  "nobody approved it in time", Severity: "notice"}}); err != nil {
			c.Log.Error("recording an expired enrollment failed", "err", err)
		}
	}
	if _, err := c.Store.PurgeExpired(ctx, org, now.Add(-expiredKeep)); err != nil {
		c.Log.Error("purging expired enrollments failed", "err", err)
	}
	return len(expired)
}

// Poll lets an enrolling agent learn its approval state and collect its certificate.
func (c *Core) Poll(ctx context.Context, ip string, req *continuumv1.PollRequest) (*continuumv1.PollResponse, error) {
	if !c.EnrollRL.Allow("poll:" + LimitKey(ip)) {
		return nil, errf(KindRateLimited, "too many attempts, slow down")
	}
	a, err := c.Store.GetAgent(ctx, req.AgentId)
	// Same answer for "no such agent" and "wrong secret" so ids cannot be probed.
	if err != nil || len(a.PollSecretHash) == 0 || subtle.ConstantTimeCompare(a.PollSecretHash, HashSecret(req.PollSecret)) != 1 {
		return nil, errf(KindUnauthenticated, "unknown agent or wrong secret")
	}
	now := c.Now().Unix()
	if a.Status == store.StatusPending && c.Now().Sub(a.CreatedAt) > c.pendingTTL() {
		c.ExpirePending(ctx, a.OrgID)
		if a, err = c.Store.GetAgent(ctx, req.AgentId); err != nil {
			return nil, errf(KindUnauthenticated, "unknown agent or wrong secret")
		}
	}
	switch a.Status {
	case store.StatusApproved:
		return &continuumv1.PollResponse{
			State:          continuumv1.PollResponse_APPROVED,
			LeafDer:        a.LeafDER,
			CaDer:          c.CA.DER,
			NotAfter:       tsProto(a.LeafNotAfter),
			ServerTimeUnix: now,
		}, nil
	case store.StatusRejected, store.StatusRevoked:
		return &continuumv1.PollResponse{State: continuumv1.PollResponse_REJECTED, Reason: a.Reason, ServerTimeUnix: now}, nil
	case store.StatusExpired:
		return &continuumv1.PollResponse{State: continuumv1.PollResponse_EXPIRED, Reason: a.Reason, ServerTimeUnix: now}, nil
	default:
		return &continuumv1.PollResponse{State: continuumv1.PollResponse_PENDING, PollAfterSeconds: c.pollAfter(), ServerTimeUnix: now}, nil
	}
}

// Approve is the human decision. The approver types the approval code the agent printed in its own log,
// which only someone who can read that cluster's pod log has. That defeats approving the wrong request.
// (For an agent that enrolled without a code, proof is the start of the cluster fingerprint instead.)
//
// At most MaxApprovalAttempts codes are tried per pending agent, counted before the comparison is made so
// that parallel guesses cannot exceed it; the last wrong one rejects the enrollment.
func (c *Core) Approve(ctx context.Context, actor, agentID, proof string, tier int) error {
	a, err := c.agentInOrg(ctx, agentID)
	if err != nil {
		return err
	}
	if a.Status == store.StatusPending && c.Now().Sub(a.CreatedAt) > c.pendingTTL() {
		c.ExpirePending(ctx, c.OrgID)
		a.Status = store.StatusExpired
	}
	switch a.Status {
	case store.StatusPending:
	case store.StatusExpired:
		return errf(KindConflict, "this request expired because nobody approved it in time. The agent asks again by itself and shows a new code in its log")
	default:
		return errf(KindConflict, "agent is %s, not pending", a.Status)
	}
	legacy := len(a.ApprovalHash) == 0
	how := "approval code confirmed"
	if legacy {
		if c.RefuseLegacyApproval {
			c.audit(ctx, actor, "approval-refused", "agent", a.ID, "legacy enrollment (no approval code) and this server refuses those")
			return errf(KindForbidden, "this agent enrolled without an approval code (it is an older version) and this server refuses to approve those. Update the agent and enroll it again")
		}
		how = "LEGACY enrollment, no approval code: confirmed by cluster fingerprint only"
	}
	// A string that cannot be a code cannot be a guess either: say so without spending an attempt.
	norm, wellFormed := approval.Normalize(proof)
	if legacy {
		wellFormed = len(proof) >= MinConfirmChars && len(proof) <= len(a.Fingerprint)
	}
	if !wellFormed {
		left := max(MaxApprovalAttempts-a.ApprovalAttempts, 0)
		if legacy {
			return errWrongCode("type at least the first 8 characters of the cluster fingerprint", left, false)
		}
		return errWrongCode("that is not an approval code. It is 8 letters and digits, like K7QM-4TXD, and is printed in the agent's log", left, false)
	}
	n, err := c.Store.CountApprovalAttempt(ctx, a.ID)
	if errors.Is(err, store.ErrBadState) {
		return errf(KindConflict, "agent changed state while approving")
	}
	if err != nil {
		return err
	}
	if n > MaxApprovalAttempts {
		return errWrongCode("too many wrong codes for this request", 0, true)
	}
	var ok bool
	if legacy {
		ok = subtle.ConstantTimeCompare([]byte(proof), []byte(a.Fingerprint[:len(proof)])) == 1
	} else {
		ok = approval.Matches(norm, a.CSR, a.ApprovalHash)
	}
	if !ok {
		left := MaxApprovalAttempts - n
		if left > 0 {
			c.audit(ctx, actor, "approval-refused", "agent", a.ID, fmt.Sprintf("wrong approval code (attempt %d of %d)", n, MaxApprovalAttempts))
			return errWrongCode("that is not the code this cluster's agent printed. Check the agent's log and try again", left, false)
		}
		reason := "too many wrong approval codes"
		_ = c.audited(ctx, actor, "approval-locked", "agent", a.ID, fmt.Sprintf("%d wrong approval codes in a row: the request is rejected", n), func() error {
			return c.Store.RejectAgent(ctx, a.ID, reason, c.Now())
		})
		return errWrongCode("that was the last attempt. The request was rejected: enroll the agent again with a new token", 0, true)
	}
	maxTier := min(a.InstalledTier, a.TierCap, ImplementedTier)
	if tier < 0 || tier > maxTier {
		return errf(KindInvalid, "access tier %d is above what is installed (%d), allowed by the token (%d) or implemented (%d)", tier, a.InstalledTier, a.TierCap, ImplementedTier)
	}
	csr, err := pki.ParseCSR(a.CSR)
	if err != nil {
		return errf(KindInvalid, "%v", err)
	}
	leaf, notAfter, err := c.CA.IssueAgent(csr, a.ID, c.OrgID, pki.AgentCertTTL)
	if err != nil {
		return err
	}
	return c.audited(ctx, actor, "agent-approved", "agent", a.ID, fmt.Sprintf("%q at tier %d (%s)", a.Name, tier, how), func() error {
		err := c.Store.ApproveAgent(ctx, a.ID, tier, actor, ClusterIDFor(c.OrgID, a.Fingerprint), leaf, notAfter, c.Now())
		switch {
		case errors.Is(err, store.ErrClusterEnrolled):
			return errf(KindConflict, "another agent is already approved for this cluster. Revoke it first to replace it")
		case errors.Is(err, store.ErrBadState):
			return errf(KindConflict, "agent changed state while approving")
		}
		return err
	})
}

// errWrongCode is the answer to a code that did not fit. left is how many tries remain; locked says the
// request has been rejected.
func errWrongCode(msg string, left int, locked bool) *Error {
	if !locked {
		msg = fmt.Sprintf("%s (%d %s left)", msg, left, plural(left, "attempt", "attempts"))
	}
	return &Error{Kind: KindInvalid, Msg: capitalise(msg), Data: map[string]any{"attemptsLeft": left, "locked": locked}}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func capitalise(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-32) + s[1:]
}
