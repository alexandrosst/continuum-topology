package server

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// The life of an agent's identity: the token that enrols it, rejecting and revoking it, the certificate it renews and the
// rejoin of one whose certificate lapsed. These were written beside the Core they are methods of; they are the agents module's.

// CreateToken issues a one-time enrollment token. The secret is returned once and never stored.
func (c *Core) CreateToken(ctx context.Context, actor, label string, tier int) (string, store.Token, error) {
	return c.CreateTokenFor(ctx, actor, label, tier, "")
}

// CreateTokenFor is CreateToken for a token that only the cluster with this identity (the UID of its
// kube-system namespace) may use; empty means any cluster.
func (c *Core) CreateTokenFor(ctx context.Context, actor, label string, tier int, expectedCluster string) (string, store.Token, error) {
	expectedCluster = strings.ToLower(strings.TrimSpace(expectedCluster))
	if expectedCluster != "" && !FingerprintRe.MatchString(expectedCluster) {
		return "", store.Token{}, Errf(KindInvalid, "the expected cluster is the UID of its kube-system namespace, like 3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c (kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')")
	}
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 80 {
		return "", store.Token{}, Errf(KindInvalid, "name the cluster (1-80 characters)")
	}
	if !GrantableTier(tier) {
		return "", store.Token{}, Errf(KindInvalid, "access tier must be 0-%d in this release", ImplementedTier)
	}
	secret, err := NewTokenSecret()
	if err != nil {
		return "", store.Token{}, err
	}
	now := c.Now()
	t := store.Token{ID: NewTokenID(), OrgID: c.OrgID, Label: label, AccessTier: tier, CreatedBy: actor, CreatedAt: now, ExpiresAt: now.Add(TokenTTL), ExpectedFingerprint: expectedCluster}
	detail := fmt.Sprintf("for %q, up to tier %d", label, tier)
	if expectedCluster != "" {
		detail += ", only for cluster " + ShortFP(expectedCluster)
	}
	if err := c.AuditedWrite(c.Store.CreateToken(ctx, t, HashSecret(secret),
		store.WithAudit(c.AuditRow(c.OrgID, actor, "token-created", "token", t.ID, detail))), "token-created"); err != nil {
		return "", store.Token{}, err
	}
	return secret, t, nil
}

// DeleteToken withdraws an enrollment token that was not used yet.
func (c *Core) DeleteToken(ctx context.Context, actor, id string) error {
	err := c.Store.DeleteToken(ctx, c.OrgID, id, store.WithAudit(c.AuditRow(c.OrgID, actor, "token-deleted", "token", id, "")))
	if errors.Is(err, store.ErrNotFound) {
		return Errf(KindNotFound, "no such unused token")
	}
	return c.AuditedWrite(err, "token-deleted")
}

func (c *Core) Reject(ctx context.Context, actor, agentID, reason string) error {
	if _, err := c.AgentInOrg(ctx, agentID); err != nil {
		return err
	}
	reason = Printable(reason, MaxReason)
	err := c.Store.RejectAgent(ctx, agentID, reason, c.Now(), store.WithAudit(c.AuditRow(c.OrgID, actor, "agent-rejected", "agent", agentID, reason)))
	if errors.Is(err, store.ErrBadState) {
		err = Errf(KindConflict, "only pending agents can be rejected")
	}
	err = c.AuditedWrite(err, "agent-rejected")
	if err == nil {
		if fresh, ferr := c.AgentInOrg(ctx, agentID); ferr == nil {
			c.recordAgentGraph(ctx, fresh, "agent-rejected", reason)
		}
	}
	return err
}

// Revoke takes effect on the agent's next call and drops its live stream immediately.
func (c *Core) Revoke(ctx context.Context, actor, agentID, reason string) error {
	if _, err := c.AgentInOrg(ctx, agentID); err != nil {
		return err
	}
	reason = Printable(reason, MaxReason)
	err := c.Store.RevokeAgent(ctx, agentID, reason, c.Now(), store.WithAudit(c.AuditRow(c.OrgID, actor, "agent-revoked", "agent", agentID, reason)))
	if errors.Is(err, store.ErrBadState) {
		err = Errf(KindConflict, "agent is not active")
	}
	err = c.AuditedWrite(err, "agent-revoked")
	if err == nil {
		if fresh, ferr := c.AgentInOrg(ctx, agentID); ferr == nil {
			c.recordAgentGraph(ctx, fresh, "agent-revoked", reason)
		}
		// A revoked agent sends nothing, so its telemetry intents are dead letters. Left active, they still count
		// as users of whatever operator they point at and make revoking that operator need force. They end with
		// the agent, each audited on its own like any other intent revocation.
		c.endAgentIntents(ctx, actor, agentID, "the agent was revoked")
		if c.OnRevoke != nil {
			c.OnRevoke(agentID)
		}
	}
	return err
}

// endAgentIntents revokes the agent's active telemetry intents. A failure is logged and does not undo the
// agent's revocation, which already took effect; OperatorUsages ignores the intents of revoked agents either way.
func (c *Core) endAgentIntents(ctx context.Context, actor, agentID, reason string) {
	intents, err := c.Store.ListTelemetryIntentsByAgent(ctx, agentID)
	if err != nil {
		c.Log.Error("agent revoked but its telemetry intents could not be listed", "agent", agentID, "err", err)
		return
	}
	for _, ti := range intents {
		if ti.Status != store.TelemetryIntentActive {
			continue
		}
		if err := c.RevokeTelemetryIntent(ctx, actor, ti.ID, reason); err != nil {
			c.Log.Error("agent revoked but one of its telemetry intents could not be revoked", "agent", agentID, "intent", ti.ID, "err", err)
		}
	}
}

// AuthorizeAgent is called on every authenticated request. Being signed by our CA is not
// enough: the agent must still be approved right now, and belong to this organisation (the
// platform-wide core, OrgID "", accepts any and reports which).
func (c *Core) AuthorizeAgent(ctx context.Context, agentID string) (store.Agent, error) {
	a, err := c.Store.GetAgent(ctx, agentID)
	if err != nil || a.Status != store.StatusApproved || (c.OrgID != "" && a.OrgID != c.OrgID) {
		return store.Agent{}, Errf(KindUnauthenticated, "agent is not approved")
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
		return nil, time.Time{}, Errf(KindRateLimited, "renewing too often")
	}
	csr, err := pki.ParseCSR(csrDER)
	if err != nil {
		return nil, time.Time{}, Errf(KindInvalid, "%v", err)
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
		return nil, Errf(KindRateLimited, "too many attempts, slow down")
	}
	deny := Errf(KindUnauthenticated, "this agent cannot rejoin; enroll again with a new token")
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
		return nil, Errf(KindInvalid, "%v", err)
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
		return nil, Errf(KindRateLimited, "renewing too often")
	}
	der, notAfter, err := c.CA.IssueAgent(csr, id, ag.OrgID, pki.AgentCertTTL)
	if err != nil {
		return nil, err
	}
	if err := c.Store.SetLeaf(ctx, id, der, notAfter); err != nil {
		return nil, err
	}
	c.AuditOrg(ctx, ag.OrgID, AgentActor(id).String(), "agent-rejoined", "agent", id, fmt.Sprintf("from %s after its certificate expired at %s", ip, rfc(leaf.NotAfter)))
	return &continuumv1.RenewResponse{LeafDer: der, CaDer: c.CA.DER, NotAfter: TSProto(&notAfter)}, nil
}
