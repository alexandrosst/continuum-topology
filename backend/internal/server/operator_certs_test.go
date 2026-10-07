package server

import (
	"strings"
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

// The state is expiring from sixty days out and expired once past; a bearer operator has no certificate to speak of.
func TestCertStateFollowsTheSoonestDate(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	mk := func(recv time.Time) store.Operator {
		far := now.Add(900 * day)
		return store.Operator{ID: "op-x", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS, ReceiverNotAfter: &recv, ClientNotAfter: &far, CreatedAt: now}
	}
	for _, c := range []struct {
		left time.Duration
		want string
	}{{200 * day, "ok"}, {61 * day, "ok"}, {59 * day, "expiring"}, {1 * day, "expiring"}, {-time.Hour, "expired"}} {
		if got := toOperatorDoc(mk(now.Add(c.left)), now).CertState; got != c.want {
			t.Errorf("%v left: certState = %q, want %q", c.left, got, c.want)
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
// created and nothing could renew them, so that is when they end; the CA's date is read off its stored certificate.
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
	want := stored.CreatedAt.Add(pki.OperatorTLSTTL)
	if c.Receiver == nil || !c.Receiver.Equal(want) || c.Client == nil || !c.Client.Equal(want) || c.CA == nil {
		t.Fatalf("derived dates = %+v, want receiver and client at %v", c, want)
	}
	// The central operator's receiver certificate is renewed daily by FUSION itself: no date is guessed for it.
	stored.ID = CentralOperatorID
	if c := operatorCertDates(stored); c.Receiver != nil || c.Client != nil || c.CA == nil {
		t.Fatalf("central dates = %+v", c)
	}
}

// One audit entry per threshold, however often the check runs; a re-issue starts the count over.
func TestCheckOperatorCertsWarnsOncePerThresholdWithAFakedClock(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.GetOperator(e.ctx, op.ID)
	end := *stored.ReceiverNotAfter
	if end2 := *stored.ClientNotAfter; end2.Before(end) {
		end = end2
	}
	check := func(at time.Time) int {
		*e.now = at
		n, err := e.core.CheckOperatorCerts(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := check(end.Add(-100 * day)); n != 0 {
		t.Fatalf("a warning 100 days out: %d", n)
	}
	steps := []struct {
		at   time.Time
		want int
		text string
	}{
		{end.Add(-59 * day), 1, "expires on"},
		{end.Add(-58 * day), 0, ""}, // same threshold: no second warning
		{end.Add(-31 * day), 0, ""},
		{end.Add(-29 * day), 1, "expires on"},
		{end.Add(-8 * day), 0, ""},
		{end.Add(-6 * day), 1, "expires on"},
		{end.Add(-5 * day), 0, ""},
		{end.Add(time.Hour), 1, "expired on"},
		{end.Add(2 * time.Hour), 0, ""},
	}
	total := 0
	for i, s := range steps {
		if n := check(s.at); n != s.want {
			t.Fatalf("step %d (%v before the end): raised %d, want %d", i, end.Sub(s.at), n, s.want)
		}
		total += s.want
	}
	if n, detail := e.countAudit(t, "operator-cert-expiring"); n != total || !strings.Contains(detail, "expired on") || !strings.Contains(detail, "certificate") {
		t.Fatalf("audit entries = %d (%q), want %d", n, detail, total)
	}
	// Renewing (the install route) records the new dates and resets the level, so the next generation is watched afresh.
	*e.now = end.Add(-90 * day)
	if _, _, err := e.core.ReissueOperatorInstall(e.ctx, "alex", op.ID); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetOperator(e.ctx, op.ID)
	// (The certificates are really issued now, so their end is the same second as before; what matters is the reset.)
	if after.CertAlertLevel != 0 || after.ReceiverNotAfter == nil || after.ReceiverNotAfter.Before(end) {
		t.Fatalf("after re-issue: level %d, receiver until %v (was %v)", after.CertAlertLevel, after.ReceiverNotAfter, end)
	}
	if n, _ := e.countAudit(t, "operator-install-reissued"); n != 1 {
		t.Fatalf("operator-install-reissued entries = %d", n)
	}
	*e.now = end.Add(-59 * day)
	if n, _ := e.core.CheckOperatorCerts(e.ctx); n != 1 {
		t.Fatalf("the new generation was not watched afresh: %d", n)
	}
}

// An operator that is not active is not warned about, and a certificate with no known date is not guessed at.
func TestCheckOperatorCertsLeavesRevokedAndBearerOperatorsAlone(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	failTLSMint(t)
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "bearer", []string{cl}, extDest("c:4317"), nil); err != nil {
		t.Fatal(err)
	}
	if err := e.core.RevokeOperator(e.ctx, "alex", op.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	*e.now = e.now.Add(4000 * day) // past every certificate
	if n, err := e.core.CheckOperatorCerts(e.ctx); err != nil || n != 0 {
		t.Fatalf("raised %d (%v) for a revoked operator and a bearer one", n, err)
	}
}

// The watcher is the one place that reaches every organisation, new ones included.
func TestPlatformChecksEveryOrganisationsOperators(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := e.st.GetOperator(e.ctx, op.ID)
	*e.now = stored.ReceiverNotAfter.Add(-10 * day)
	p := NewPlatform(e.base, nil)
	if n, err := p.CheckAllOperatorCerts(e.ctx); err != nil || n != 1 {
		t.Fatalf("raised %d (%v)", n, err)
	}
	if n, _ := p.CheckAllOperatorCerts(e.ctx); n != 0 {
		t.Fatalf("the second run warned again: %d", n)
	}
}

// A client certificate issued to one sender on its own (here: the cluster again, later) is the one the operator is
// watched by from then on; it used to leave the stored date at the creation one, so the operator went on warning about a
// certificate that had been replaced.
func TestClientCertDateFollowsTheNewestCertificateOfEachSender(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	short := 30 * day
	old := pki.OperatorTLSTTL
	pki.OperatorTLSTTL = short
	op, _, _, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	pki.OperatorTLSTTL = old
	if err != nil {
		t.Fatal(err)
	}
	before, _ := e.st.GetOperator(e.ctx, op.ID)
	if before.ClientNotAfter == nil || before.ClientNotAfter.After(time.Now().Add(31*day)) {
		t.Fatalf("setup: client certificate ends %v, want about 30 days out", before.ClientNotAfter)
	}
	*e.now = e.now.Add(time.Minute) // issued later than the first: the ledger orders by issue time
	if _, _, _, err := e.core.IssueOperatorClientCertFor(e.ctx, "alex", op.ID, cl, "again"); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetOperator(e.ctx, op.ID)
	if after.ClientNotAfter == nil || after.ClientNotAfter.Before(time.Now().Add(300*day)) {
		t.Fatalf("client certificate end after issuing a new one = %v, want about a year out", after.ClientNotAfter)
	}
	if !after.ReceiverNotAfter.Equal(*before.ReceiverNotAfter) {
		t.Fatalf("the receiver certificate date moved: %v -> %v", before.ReceiverNotAfter, after.ReceiverNotAfter)
	}
}

// With several senders the operator is watched by the one whose newest certificate ends first, and the warning says
// which: "client certificate held by <cluster>". A sender that is no longer configured does not count.
func TestExpiryWarningNamesTheSenderWhoseCertificateEndsFirst(t *testing.T) {
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
	// c2's newest is now the short one: it is what the operator is watched by.
	st, _ := e.st.GetOperator(e.ctx, op.ID)
	if st.ClientNotAfter == nil || st.ClientNotAfter.After(time.Now().Add(21*day)) {
		t.Fatalf("client date = %v, want about 20 days out", st.ClientNotAfter)
	}
	*e.now = time.Now() // the certificates were really issued now, so their ends are measured from now
	if n, err := e.core.CheckOperatorCerts(e.ctx); err != nil || n != 1 {
		t.Fatalf("raised %d (%v), want the one 30-day warning", n, err)
	}
	if _, detail := e.countAudit(t, "operator-cert-expiring"); !strings.Contains(detail, "client certificate held by "+c2) {
		t.Fatalf("warning = %q, want it to name %s", detail, c2)
	}
	// c2 leaves the operator: the other sender's year is what counts, and the warnings start over.
	if err := e.core.UpdateOperatorScope(e.ctx, "alex", op.ID, []string{c1}, extDest("c:4317"), nil); err != nil {
		t.Fatal(err)
	}
	if n, err := e.core.CheckOperatorCerts(e.ctx); err != nil || n != 0 {
		t.Fatalf("after the short sender left: raised %d (%v)", n, err)
	}
	st, _ = e.st.GetOperator(e.ctx, op.ID)
	if st.ClientNotAfter.Before(time.Now().Add(300*day)) || st.CertAlertLevel != 0 {
		t.Fatalf("after the short sender left: client date %v, level %d", st.ClientNotAfter, st.CertAlertLevel)
	}
}
