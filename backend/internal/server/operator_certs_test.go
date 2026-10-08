package server

import (
	"testing"
	"time"

	"continuum/internal/pki"
	"continuum/internal/store"
)

const day = 24 * time.Hour

// countAudit is how many audit rows of one action there are, and the newest one's detail.
func (e *env) countAudit(t *testing.T, action string) (n int, detail string) {
	t.Helper()
	rows, err := e.st.ListAudit(e.ctx, "org-1", 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Action == action {
			if n == 0 {
				detail = r.Detail
			}
			n++
		}
	}
	return n, detail
}

// A new operator records when its receiver and client certificates end, and the operator document shows them with the CA's.
func TestCreatedOperatorRecordsItsCertificateDates(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, bundle, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.GetOperator(e.ctx, op.ID)
	wantRecv, _ := pki.NotAfter(bundle.ReceiverCertPEM)
	if len(bundle.Senders) != 1 {
		t.Fatalf("senders = %d, want one certificate for the one source cluster", len(bundle.Senders))
	}
	wantClient, _ := pki.NotAfter(bundle.Senders[0].CertPEM)
	if stored.ReceiverNotAfter == nil || !stored.ReceiverNotAfter.Equal(wantRecv.Truncate(time.Millisecond)) || stored.ClientNotAfter == nil {
		t.Fatalf("recorded %v / %v, issued %v / %v", stored.ReceiverNotAfter, stored.ClientNotAfter, wantRecv, wantClient)
	}
	// The client end is the one recorded with the operator: minted moments after the receiver's, so within a minute of it.
	if d := wantClient.Sub(*stored.ClientNotAfter); d > time.Minute || d < -time.Minute {
		t.Fatalf("recorded client end %v, issued %v", stored.ClientNotAfter, wantClient)
	}
	d := toOperatorDoc(stored, *e.now)
	caEnd, _ := pki.NotAfter(stored.ClientCACertPEM)
	if d.Certs == nil || d.Certs.ReceiverNotAfter == "" || d.Certs.ClientNotAfter == "" || d.Certs.CANotAfter != rfc(caEnd) || d.CertState != "ok" {
		t.Fatalf("certs = %+v state %q", d.Certs, d.CertState)
	}
}

// The state says whether renewal is happening: ok until the day it should have happened has passed by the slack, then
// renewal-failing (with that day), then expired. A bearer operator has no certificate to speak of.
func TestCertStateSaysWhetherRenewalIsHappening(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mk := func(recv time.Time) store.Operator {
		far := now.Add(900 * day)
		return store.Operator{ID: "op-x", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS, ReceiverNotAfter: &recv, ClientNotAfter: &far, CreatedAt: now}
	}
	for _, c := range []struct {
		left time.Duration
		want string
	}{{200 * day, CertStateOK}, {25 * day, CertStateOK}, {16 * day, CertStateOK}, {14 * day, CertStateRenewalFailing}, {1 * day, CertStateRenewalFailing}, {-time.Hour, CertStateExpired}} {
		d := toOperatorDoc(mk(now.Add(c.left)), now)
		if d.CertState != c.want {
			t.Errorf("%v left: certState = %q, want %q", c.left, d.CertState, c.want)
		}
		wantSince := ""
		if c.want != CertStateOK {
			wantSince = rfc(now.Add(c.left).Add(-pki.OperatorRenewBefore))
		}
		if d.CertStateSince != wantSince {
			t.Errorf("%v left: certStateSince = %q, want %q", c.left, d.CertStateSince, wantSince)
		}
	}
	bearer := store.Operator{ID: "op-b", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer, CreatedAt: now}
	if d := toOperatorDoc(bearer, now); d.Certs != nil || d.CertState != "" {
		t.Fatalf("a bearer operator shows certificates: %+v %q", d.Certs, d.CertState)
	}
	revoked := mk(now.Add(-day))
	revoked.Status = store.OperatorRevoked
	if d := toOperatorDoc(revoked, now); d.Certs != nil || d.CertState != "" {
		t.Fatalf("a revoked operator shows certificates: %+v %q", d.Certs, d.CertState)
	}
}

// An operator from before the dates were recorded: the receiver and client certificates were issued when it was
// created, for a year, and nothing could renew them, so that is when they end; the CA's date is read off its stored certificate.
func TestLegacyOperatorCertDatesAreDerivedFromWhatIsStored(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "old", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.GetOperator(e.ctx, op.ID)
	stored.ReceiverNotAfter, stored.ClientNotAfter = nil, nil // as read from a schema 15 row
	c := operatorCertDates(stored)
	want := stored.CreatedAt.Add(pki.LegacyOperatorTLSTTL)
	if c.Receiver == nil || !c.Receiver.Equal(want) || c.Client == nil || !c.Client.Equal(want) || c.CA == nil {
		t.Fatalf("derived dates = %+v, want receiver and client at %v", c, want)
	}
	// The central operator's receiver certificate is renewed daily by FUSION itself: no date is guessed for it.
	stored.ID = CentralOperatorID
	if c := operatorCertDates(stored); c.Receiver != nil || c.Client != nil || c.CA == nil {
		t.Fatalf("central dates = %+v", c)
	}
}

// Installing again issues certificates again: the new dates are recorded, and nothing is written to the audit trail but
// the install itself (there is no expiry warning to raise any more).
func TestReissueRecordsTheNewDatesAndRaisesNoWarning(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	old := pki.OperatorTLSTTL
	pki.OperatorTLSTTL = 10 * day
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	pki.OperatorTLSTTL = old
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.GetOperator(e.ctx, op.ID)
	*e.now = e.now.Add(time.Minute)
	if _, _, err := e.core.ReissueOperatorInstall(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetOperator(e.ctx, op.ID)
	if after.ReceiverNotAfter == nil || !after.ReceiverNotAfter.After(before.ReceiverNotAfter.Add(15*day)) {
		t.Fatalf("receiver date after installing again = %v (was %v)", after.ReceiverNotAfter, before.ReceiverNotAfter)
	}
	if after.ClientNotAfter == nil || !after.ClientNotAfter.After(before.ClientNotAfter.Add(15*day)) {
		t.Fatalf("client date after installing again = %v (was %v)", after.ClientNotAfter, before.ClientNotAfter)
	}
	if n, _ := e.countAudit(t, "operator-cert-expiring"); n != 0 {
		t.Fatalf("expiry warnings in the trail: %d", n)
	}
}

// A client certificate issued to one sender on its own (here: the cluster again, later) is the one the operator is
// watched by from then on; it used to leave the stored date at the creation one, so the operator went on warning about a
// certificate that had been replaced.
func TestClientCertDateFollowsTheNewestCertificateOfEachSender(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	short := 10 * day
	old := pki.OperatorTLSTTL
	pki.OperatorTLSTTL = short
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	pki.OperatorTLSTTL = old
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.GetOperator(e.ctx, op.ID)
	if before.ClientNotAfter == nil || before.ClientNotAfter.After(time.Now().Add(11*day)) {
		t.Fatalf("setup: client certificate ends %v, want about 10 days out", before.ClientNotAfter)
	}
	*e.now = e.now.Add(time.Minute) // issued later than the first: the ledger orders by issue time
	if _, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, cl, "again"); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetOperator(e.ctx, op.ID)
	if after.ClientNotAfter == nil || after.ClientNotAfter.Before(time.Now().Add(25*day)) {
		t.Fatalf("client certificate end after issuing a new one = %v, want about 30 days out", after.ClientNotAfter)
	}
	if !after.ReceiverNotAfter.Equal(*before.ReceiverNotAfter) {
		t.Fatalf("the receiver certificate date moved: %v -> %v", before.ReceiverNotAfter, after.ReceiverNotAfter)
	}
}

// With several senders the dates follow the one whose newest certificate ends first. A sender that is no longer
// configured does not count.
func TestOperatorDatesFollowTheSenderWhoseCertificateEndsFirst(t *testing.T) {
	e := newEnv(t)
	c1 := e.approvedCluster(t, fp)
	c2 := e.approvedCluster(t, "9a3c2a9e-1111-4222-8333-944455556677")
	oldTTL := pki.OperatorTLSTTL
	t.Cleanup(func() { pki.OperatorTLSTTL = oldTTL })
	pki.OperatorTLSTTL = 365 * day
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{c1, c2}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	// c2 gets a certificate that ends in 20 days; c1 keeps its year.
	pki.OperatorTLSTTL = 20 * day
	*e.now = e.now.Add(time.Minute) // issued later than the first: the ledger orders by issue time
	if _, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, c2, "short"); err != nil {
		t.Fatal(err)
	}
	pki.OperatorTLSTTL = 365 * day
	st, _ := e.st.GetOperator(e.ctx, op.ID)
	if st.ClientNotAfter == nil || st.ClientNotAfter.After(time.Now().Add(21*day)) {
		t.Fatalf("client date = %v, want about 20 days out", st.ClientNotAfter)
	}
	// c2 leaves the operator: the other sender's year is what counts.
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{c1}, extDest("c:4317"), nil); err != nil {
		t.Fatal(err)
	}
	st, _ = e.st.GetOperator(e.ctx, op.ID)
	if st.ClientNotAfter.Before(time.Now().Add(300 * day)) {
		t.Fatalf("after the short sender left: client date %v", st.ClientNotAfter)
	}
}
