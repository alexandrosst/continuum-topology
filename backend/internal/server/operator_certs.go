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
	for _, op := range ops {
		if op.Status != store.OperatorActive {
			continue
		}
		at, which, ok := operatorCertDates(op).soonest()
		if !ok {
			continue
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
		if out.TLS.ClientCertPEM, out.TLS.ClientKeyPEM, err = issuer.IssueOperatorClientTLS(op.ID, c.OrgID); err != nil {
			return store.Operator{}, out, err
		}
		r, cl := certNotAfter(out.TLS.ReceiverCertPEM), certNotAfter(out.TLS.ClientCertPEM)
		if r == nil || cl == nil {
			return store.Operator{}, out, fmt.Errorf("the certificates just issued could not be read back")
		}
		recvEnd, clientEnd = *r, *cl
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
