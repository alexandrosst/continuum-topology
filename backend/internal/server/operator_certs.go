package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"continuum/internal/pki"
	"continuum/internal/store"
)

// Operator certificates expire (the receiver's and the clients' after pki.OperatorTLSTTL, the operator's own CA
// after pki.OperatorCATTL) and nothing renews them by itself, so the server says when that is coming: the dates are in
// the operator document, and a daily check writes one audit entry as the first of them comes within each of these
// many days.
var certWarnDays = []int{60, 30, 7}

// certExpiredLevel is the CertAlertLevel for a certificate already past its end; levels 1..len(certWarnDays) are
// the thresholds, in order of increasing urgency.
var certExpiredLevel = len(certWarnDays) + 1

// OperatorCerts are the dates an operator's certificates stop being valid; a nil one is not known.
type OperatorCerts struct {
	Receiver, Client, CA *time.Time
}

// operatorCertDates is what the server knows about op's certificate lifetimes. The CA's comes from its stored
// certificate. The receiver and client certificates are not kept (they are shown once), so their dates are the ones
// recorded when they were issued; for an mTLS operator from before they were recorded, the date they must have
// had: they were issued in the same step that created the operator, for pki.OperatorTLSTTL, and nothing could
// re-issue them before the install route existed. A bearer operator has no certificates the server issued that it
// can vouch for, and the central operator's receiver certificate is renewed daily by its own control loop, so for
// neither is one guessed.
func operatorCertDates(op store.Operator) OperatorCerts {
	var c OperatorCerts
	if len(op.ClientCACertPEM) > 0 {
		c.CA = certNotAfter(op.ClientCACertPEM)
	}
	if op.ReceiverAuth != store.ReceiverAuthMTLS || op.ID == CentralOperatorID {
		return c
	}
	derived := op.CreatedAt.Add(pki.OperatorTLSTTL)
	if c.CA != nil && derived.After(*c.CA) {
		derived = *c.CA // a certificate never outlives the CA that signed it
	}
	c.Receiver, c.Client = op.ReceiverNotAfter, op.ClientNotAfter
	if c.Receiver == nil {
		c.Receiver = &derived
	}
	if c.Client == nil {
		c.Client = &derived
	}
	return c
}

// soonest is the earliest known date and which certificate it is, ok false when none is known.
func (c OperatorCerts) soonest() (at time.Time, which string, ok bool) {
	for _, e := range []struct {
		t    *time.Time
		name string
	}{{c.Receiver, "receiver certificate"}, {c.Client, "client certificate"}, {c.CA, "CA certificate"}} {
		if e.t != nil && (!ok || e.t.Before(at)) {
			at, which, ok = *e.t, e.name, true
		}
	}
	return at, which, ok
}

// certLevel is how urgent a certificate ending at `at` is, seen from now: 0 not yet, up to certExpiredLevel.
func certLevel(at, now time.Time) int {
	left := at.Sub(now)
	if left <= 0 {
		return certExpiredLevel
	}
	for i := len(certWarnDays) - 1; i >= 0; i-- {
		if left <= time.Duration(certWarnDays[i])*24*time.Hour {
			return i + 1
		}
	}
	return 0
}

// known says whether any of the dates is.
func (c OperatorCerts) known() bool { _, _, ok := c.soonest(); return ok }

// certState is "ok", "expiring" (anything within the first threshold) or "expired"; "" when no date is known.
func (c OperatorCerts) certState(now time.Time) string {
	at, _, ok := c.soonest()
	switch {
	case !ok:
		return ""
	case certLevel(at, now) == certExpiredLevel:
		return "expired"
	case certLevel(at, now) > 0:
		return "expiring"
	}
	return "ok"
}

// CheckOperatorCerts raises the expiry warning for every active operator whose soonest certificate has reached a
// threshold it has not been warned about yet: one audit entry ("operator-cert-expiring") per threshold per
// certificate generation, never one per run. Re-issuing the certificates (see ReissueOperatorInstall) resets the
// level, so the next generation is watched afresh. Returns how many warnings it raised. A warning whose audit row
// cannot be written is not marked as raised, so the next run tries again.
func (c *Core) CheckOperatorCerts(ctx context.Context) (int, error) {
	ops, err := c.Store.ListOperators(ctx, c.OrgID)
	if err != nil {
		return 0, err
	}
	now := c.Now()
	raised := 0
	senders, err := c.operatorSenders(ctx)
	if err != nil {
		return 0, err
	}
	for _, op := range ops {
		if op.Status != store.OperatorActive {
			continue
		}
		// The dates the operator document shows are brought in line with the ledger first, so a certificate issued
		// for one sender on its own is watched, and one for a sender that is gone no longer is.
		op, holder, err := c.refreshCertEnds(ctx, op, senders[op.ID])
		if err != nil {
			c.Log.Error("operator certificate dates not refreshed", "operator", op.ID, "err", err)
		}
		at, which, ok := operatorCertDates(op).soonest()
		if !ok {
			continue
		}
		if which == "client certificate" && holder != "" {
			which += " held by " + holder
		}
		level := certLevel(at, now)
		if level <= op.CertAlertLevel {
			continue
		}
		detail := fmt.Sprintf("the %s expires on %s (in %d days)", which, at.UTC().Format("2006-01-02"), int(at.Sub(now).Hours()/24))
		if level == certExpiredLevel {
			detail = fmt.Sprintf("the %s expired on %s", which, at.UTC().Format("2006-01-02"))
		}
		if err := c.Store.AddAudit(ctx, c.auditRow(c.OrgID, "system", "operator-cert-expiring", "operator", op.ID, detail)); err != nil {
			c.Log.Error("operator certificate warning not recorded; will retry", "operator", op.ID, "err", err)
			continue
		}
		if err := c.Store.SetOperatorCertAlertLevel(ctx, op.ID, level); err != nil {
			c.Log.Error("operator certificate warning level not saved", "operator", op.ID, "err", err)
			continue
		}
		raised++
	}
	return raised, nil
}

// operatorSenders is, for every operator of the organisation, who is configured to send to it right now: its own source
// clusters, the clusters of active telemetry intents that name it as a destination, and the operators that export
// into it. These are the holders of client certificates that matter: a certificate issued to a sender that no longer
// sends is not worth a warning. The sender names are the ones the ledger records (a cluster id, or an operator id).
// A cluster counts only while one of its agents is approved, so revoking the agent ends the cluster's certificate
// renewals (see RenewTelemetryCert) and the certificate ends within pki.OperatorTLSTTL.
func (c *Core) operatorSenders(ctx context.Context) (map[string]map[string]bool, error) {
	ops, err := c.Store.ListOperators(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	intents, err := c.Store.ListTelemetryIntents(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	agents, err := c.Store.ListAgents(ctx, c.OrgID)
	if err != nil {
		return nil, err
	}
	clusterOf := make(map[string]string, len(agents)) // approved agents only
	approved := map[string]bool{}                     // clusters with an approved agent
	for _, a := range agents {
		if a.Status == store.StatusApproved && a.ClusterID != "" {
			clusterOf[a.ID] = a.ClusterID
			approved[a.ClusterID] = true
		}
	}
	out := map[string]map[string]bool{}
	add := func(operator, sender string) {
		if operator == "" || sender == "" {
			return
		}
		if out[operator] == nil {
			out[operator] = map[string]bool{}
		}
		out[operator][sender] = true
	}
	for _, op := range ops {
		if op.Status != store.OperatorActive {
			continue
		}
		for _, cl := range op.SourceClusterIDs {
			if approved[cl] {
				add(op.ID, cl)
			}
		}
		if op.Destination.Kind == store.DestinationOperator {
			add(op.Destination.TargetOperatorID, op.ID)
		}
	}
	for _, ti := range intents {
		if ti.Status != store.TelemetryIntentActive {
			continue
		}
		cl := clusterOf[ti.AgentID]
		if ti.Destination.Kind == store.DestinationOperator {
			add(ti.Destination.TargetOperatorID, cl)
		}
		for _, d := range ti.Routes {
			if d.Kind == store.DestinationOperator {
				add(d.TargetOperatorID, cl)
			}
		}
	}
	return out, nil
}

// refreshCertEnds brings the stored receiver and client certificate dates of an mTLS operator in line with the ledger
// of what was actually issued, and returns the operator as it now stands, plus which sender's client certificate is the
// one ending first (when that is known).
//
// Why: the dates were written only when an operator was created or installed again, so a client certificate issued to one
// sender on its own (a cluster added later, a telemetry intent pointed at the operator) never moved them, and an old
// date kept warning about a certificate that had since been replaced, while a new one that was about to end was not
// watched at all. Now the client date is the soonest of the NEWEST certificate of each current sender. It is only
// replaced when every current sender has a ledger entry; with a sender that predates the ledger, or none at all, the date
// already stored is the best known and stays. Replacing a date starts the expiry warnings over for the new generation.
func (c *Core) refreshCertEnds(ctx context.Context, op store.Operator, senders map[string]bool) (store.Operator, string, error) {
	if op.ReceiverAuth != store.ReceiverAuthMTLS || op.ID == CentralOperatorID {
		return op, "", nil
	}
	dates := operatorCertDates(op)
	if dates.Receiver == nil || dates.Client == nil {
		return op, "", nil
	}
	ledger, err := c.Store.ListOperatorCerts(ctx, op.ID) // newest first
	if err != nil {
		return op, "", err
	}
	// The newest certificate of each kind and sender: the latest issued, and of two issued at the same instant the one
	// that was made later, then the one that lasts longer, so the answer never depends on the order rows come back in.
	later := func(a, b store.OperatorCert) bool {
		switch {
		case !a.IssuedAt.Equal(b.IssuedAt):
			return a.IssuedAt.After(b.IssuedAt)
		case !a.NotBefore.Equal(b.NotBefore):
			return a.NotBefore.After(b.NotBefore)
		}
		return a.NotAfter.After(b.NotAfter)
	}
	var recvCert *store.OperatorCert
	newestBy := map[string]store.OperatorCert{}
	for i, e := range ledger {
		switch e.Kind {
		case store.OperatorCertReceiver:
			if recvCert == nil || later(e, *recvCert) {
				recvCert = &ledger[i]
			}
		case store.OperatorCertClient:
			if cur, ok := newestBy[e.Sender]; !ok || later(e, cur) {
				newestBy[e.Sender] = e
			}
		}
	}
	recv := *dates.Receiver
	if recvCert != nil {
		recv = recvCert.NotAfter
	}
	client, holder := *dates.Client, ""
	newest := map[string]time.Time{}
	for s, e := range newestBy {
		newest[s] = e.NotAfter
	}
	if len(senders) > 0 {
		covered, first, who := true, time.Time{}, ""
		for s := range senders {
			end, ok := newest[s]
			if !ok {
				covered = false
				break
			}
			if first.IsZero() || end.Before(first) || (end.Equal(first) && s < who) {
				first, who = end, s
			}
		}
		if covered {
			client, holder = first, who
		}
	}
	if recv.Equal(*dates.Receiver) && client.Equal(*dates.Client) {
		return op, holder, nil
	}
	if err := c.Store.SetOperatorCerts(ctx, op.ID, recv, client); err != nil {
		return op, holder, stateErr(err)
	}
	op.ReceiverNotAfter, op.ClientNotAfter, op.CertAlertLevel = &recv, &client, 0
	return op, holder, nil
}

// operatorCertCheckEvery is how often WatchOperatorCerts runs: the thresholds are days apart.
const operatorCertCheckEvery = 24 * time.Hour

// CheckAllOperatorCerts is CheckOperatorCerts for every organisation, the ones created since the server started
// included. It returns how many warnings it raised.
func (p *Platform) CheckAllOperatorCerts(ctx context.Context) (int, error) {
	orgs, err := p.Base.Store.ListOrgs(ctx)
	if err != nil {
		return 0, err
	}
	// One organisation that cannot be read must not leave the others' certificates unwatched: go on, and report all.
	raised := 0
	var errs []error
	for _, o := range orgs {
		t, err := p.Tenant(ctx, o.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", o.ID, err))
			continue
		}
		n, err := t.C.CheckOperatorCerts(ctx)
		raised += n
		if err != nil {
			errs = append(errs, fmt.Errorf("org %s: %w", o.ID, err))
		}
	}
	return raised, errors.Join(errs...)
}

// WatchOperatorCerts runs CheckAllOperatorCerts when it starts, so a server that was down for weeks catches up at
// once, and then every day, until ctx ends.
func (p *Platform) WatchOperatorCerts(ctx context.Context) {
	t := time.NewTicker(operatorCertCheckEvery)
	defer t.Stop()
	for {
		if _, err := p.CheckAllOperatorCerts(ctx); err != nil && ctx.Err() == nil {
			p.Base.Log.Error("operator certificate check failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// OperatorReissue is what ReissueOperatorInstall minted: nothing in it is stored beyond the hashes that gate the
// operator, so it is shown once.
type OperatorReissue struct {
	// TLS is a fresh receiver and client certificate from the operator's own CA (mTLS operators), with that CA's
	// certificate.
	TLS OperatorTLSBundle
	// ReceiverToken is a new bearer token (bearer operators), replacing the old one.
	ReceiverToken string
	// HeartbeatSecret is a new heartbeat secret, for an operator that has one: the old one is hashed and cannot be
	// shown again, so it is replaced.
	HeartbeatSecret string
}

// ReissueOperatorInstall is "install this operator again": a fresh receiver and client certificate from the stored
// CA, a new bearer token for a bearer operator, and a new heartbeat secret for one that reports a heartbeat. The
// earlier certificates are not revoked (nothing here can) and keep working until they expire; the replaced
// tokens stop working at once. The new certificate dates are recorded and the expiry warning starts over.
func (c *Core) ReissueOperatorInstall(ctx context.Context, actor, id string) (store.Operator, OperatorReissue, error) {
	c.depMu.RLock() // see Core.depMu: a revoke or delete cannot come between the check that it is active and the writes below
	defer c.depMu.RUnlock()
	var out OperatorReissue
	if err := guardCentral(id); err != nil {
		return store.Operator{}, out, err
	}
	op, err := c.operatorInOrg(ctx, id)
	if err != nil {
		return store.Operator{}, out, err
	}
	if op.Status != store.OperatorActive {
		return store.Operator{}, out, errf(KindConflict, "only an active operator can be installed again")
	}
	var recvEnd, clientEnd time.Time
	var hbHash, tokenHash []byte
	var did []string
	if op.ReceiverAuth == store.ReceiverAuthMTLS {
		issuer, caPEM, err := c.operatorIssuer(ctx, op)
		if err != nil {
			return store.Operator{}, out, err
		}
		out.TLS.CACertPEM = caPEM
		if out.TLS.ReceiverCertPEM, out.TLS.ReceiverKeyPEM, err = issuer.IssueOperatorReceiverTLS(op.ID, c.OrgID, operatorReceiverHosts(op.ID)); err != nil {
			return store.Operator{}, out, err
		}
		r := certNotAfter(out.TLS.ReceiverCertPEM)
		if r == nil {
			return store.Operator{}, out, fmt.Errorf("the certificate just issued could not be read back")
		}
		recvEnd = *r
		// Recorded in the ledger now, before anything is changed, and a failure is the request's failure: the new secrets
		// exist only in this call, so an error AFTER the rotation was committed would lose the only copy of keys the
		// operator's old ones have already been replaced by. Here nothing has been replaced yet, so refusing costs nothing.
		if err := c.recordOperatorCert(ctx, actor, op, store.OperatorCertReceiver, "", out.TLS.ReceiverCertPEM); err != nil {
			return store.Operator{}, OperatorReissue{}, fmt.Errorf("the new receiver certificate could not be recorded, so nothing was changed: %w", err)
		}
		// One certificate per source cluster, each naming its holder. Unlike at creation a failure here is the
		// request's failure: this call exists to hand them out.
		for _, cl := range op.SourceClusterIDs {
			certPEM, keyPEM, _, err := c.issueOperatorClientCertHeld(ctx, actor, op.ID, cl, "cluster="+cl+" (install again)")
			if err != nil {
				return store.Operator{}, out, err
			}
			out.TLS.Senders = append(out.TLS.Senders, SenderCert{Sender: cl, CertPEM: certPEM, KeyPEM: keyPEM})
		}
		clientEnd = recvEnd // client certificates are minted with the same lifetime
		did = append(did, "certificates until "+recvEnd.UTC().Format("2006-01-02"))
	} else {
		if out.ReceiverToken, err = NewOperatorReceiverSecret(); err != nil {
			return store.Operator{}, out, err
		}
		tokenHash = HashSecret(out.ReceiverToken)
		did = append(did, "receiver token rotated")
	}
	if len(op.HeartbeatHash) > 0 {
		if out.HeartbeatSecret, err = NewOperatorHeartbeatSecret(); err != nil {
			return store.Operator{}, out, err
		}
		hbHash = HashSecret(out.HeartbeatSecret)
		did = append(did, "heartbeat secret rotated")
	}
	// Only what was done, never any secret or hash: the trail records that it happened, by whom, to what.
	detail := strings.Join(did, "; ")
	err = c.audited(ctx, actor, "operator-install-reissued", "operator", id, detail, func() error {
		if tokenHash != nil {
			if err := c.Store.SetOperatorReceiverToken(ctx, id, tokenHash); err != nil {
				return stateErr(err)
			}
		} else if err := c.Store.SetOperatorCerts(ctx, id, recvEnd, clientEnd); err != nil {
			return stateErr(err)
		}
		if hbHash != nil {
			if err := c.Store.SetOperatorHeartbeat(ctx, id, hbHash, c.Now()); err != nil {
				return stateErr(err)
			}
		}
		return nil
	})
	if err != nil {
		return store.Operator{}, OperatorReissue{}, err
	}
	op, err = c.operatorInOrg(ctx, id)
	return op, out, err
}

// stateErr words the store's "not active" answer for a caller.
func stateErr(err error) error {
	if errors.Is(err, store.ErrBadState) {
		return errf(KindConflict, "operator is not active")
	}
	return err
}
