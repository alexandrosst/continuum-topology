package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

const fp = "8f3c2a9e-1111-4222-8333-944455556666"

type env struct {
	base *Core // the platform-wide core: accounts, sign-in, the gRPC listener
	core *Core // organisation "org-1", which most tests work in
	st   *store.SQLite
	now  *time.Time
	ctx  context.Context
}

// newEnv is a server with one organisation, "org-1", owned by a user nobody signs in as.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvBare(t)
	u := store.User{ID: "u-owner", Username: "org1-owner", PasswordHash: "!", CreatedAt: time.Now()}
	if err := e.st.CreateUser(e.ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-1", Name: "Org One", CreatedAt: time.Now(), CreatedBy: u.ID}, u.ID); err != nil {
		t.Fatal(err)
	}
	return e
}

// newEnvBare has no accounts and no organisation yet (BootstrapAdmin would create "org-1").
func newEnvBare(t *testing.T) *env {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := NewCore(st, ca, "", nil)
	base.DefaultOrg = "org-1"
	now := time.Now()
	e := &env{base: base, st: st, now: &now, ctx: context.Background()}
	base.Now = func() time.Time { return *e.now }
	base.EnrollRL = NewLimiter(6000, 1000) // out of the way unless a test wants it
	e.core = base.ForOrg("org-1")
	return e
}

func csr(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	return der, k
}

func (e *env) enroll(t *testing.T, tier uint32, fingerprint string) (*continuumv1.EnrollResponse, string) {
	t.Helper()
	secret, _, err := e.core.CreateToken(e.ctx, "admin", "edge-patras", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := csr(t)
	resp, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fingerprint, InstalledAccessTier: tier, AgentVersion: "0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	return resp, secret
}

func kindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

func TestEnrollApprovePollHappyPath(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, fp)
	poll := func() *continuumv1.PollResponse {
		p, err := e.core.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if poll().State != continuumv1.PollResponse_PENDING {
		t.Fatal("should be pending before approval")
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp[:8], 2); err != nil {
		t.Fatal(err)
	}
	p := poll()
	if p.State != continuumv1.PollResponse_APPROVED || len(p.LeafDer) == 0 {
		t.Fatalf("state = %v", p.State)
	}
	leaf, _ := x509.ParseCertificate(p.LeafDer)
	if leaf.Subject.CommonName != resp.AgentId {
		t.Fatalf("CN = %s", leaf.Subject.CommonName)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: e.core.CA.Pool(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAgent(e.ctx, resp.AgentId)
	if a.Name != "edge-patras" || a.AccessTier != 2 || a.ClusterID != ClusterIDFor("org-1", fp) || a.ApprovedBy != "alex" {
		t.Fatalf("agent = %+v", a)
	}
}

func TestTokenIsSingleUseEvenUnderRace(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "c", 1)
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _ := csr(t)
			if _, err := e.core.Enroll(e.ctx, "10.0.0.2", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp}); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d agents enrolled with one token, want exactly 1", wins)
	}
	agents, _ := e.st.ListAgents(e.ctx, "org-1")
	if len(agents) != 1 {
		t.Fatalf("%d agent rows", len(agents))
	}
}

func TestTokenExpiryUnknownAndMalformed(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "c", 1)
	*e.now = e.now.Add(TokenTTL + time.Second)
	d, _ := csr(t)
	_, err := e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp})
	if kindOf(err) != KindUnauthenticated {
		t.Fatalf("expired token: %v", err)
	}
	unknown, _ := NewTokenSecret()
	_, err = e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: unknown, CsrDer: d, ClusterFingerprint: fp})
	if kindOf(err) != KindUnauthenticated {
		t.Fatalf("unknown token: %v", err)
	}
	for _, bad := range []string{"", "cnt_", "short", "cnt_" + string(make([]byte, 43))} {
		_, err = e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: bad, CsrDer: d, ClusterFingerprint: fp})
		if kindOf(err) != KindUnauthenticated {
			t.Fatalf("malformed %q: %v", bad, err)
		}
	}
}

func TestBadCSRDoesNotBurnTheToken(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "c", 1)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rk)
	if _, err := e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: rsaCSR, ClusterFingerprint: fp}); kindOf(err) != KindInvalid {
		t.Fatalf("rsa csr: %v", err)
	}
	if _, err := e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: []byte("x"), ClusterFingerprint: fp}); kindOf(err) != KindInvalid {
		t.Fatalf("garbage csr: %v", err)
	}
	d, _ := csr(t)
	if _, err := e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: "bad fp!"}); kindOf(err) != KindInvalid {
		t.Fatalf("bad fingerprint: %v", err)
	}
	if _, err := e.core.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp}); err != nil {
		t.Fatalf("token should still work after malformed attempts: %v", err)
	}
}

func TestPollNeedsTheSecretAndDoesNotRevealAgents(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, fp)
	_, err1 := e.core.Poll(e.ctx, "9.9.9.9", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: "wrong"})
	_, err2 := e.core.Poll(e.ctx, "9.9.9.9", &continuumv1.PollRequest{AgentId: "ag-doesnotexist", PollSecret: "wrong"})
	if kindOf(err1) != KindUnauthenticated || kindOf(err2) != KindUnauthenticated || err1.Error() != err2.Error() {
		t.Fatalf("poll must answer identically: %v / %v", err1, err2)
	}
}

func TestApprovalRequiresFingerprintConfirmation(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, fp)
	for _, confirm := range []string{"", "8f3c", "deadbeef", fp + "x", "8F3C2A9E"} {
		if err := e.core.Approve(e.ctx, "alex", resp.AgentId, confirm, 1); kindOf(err) != KindInvalid {
			t.Fatalf("confirm %q accepted: %v", confirm, err)
		}
	}
	if a, _ := e.st.GetAgent(e.ctx, resp.AgentId); a.Status != store.StatusPending {
		t.Fatal("agent left pending state without a correct confirmation")
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp, 1); err != nil { // full value also works
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp, 1); kindOf(err) != KindConflict {
		t.Fatalf("approving twice: %v", err)
	}
}

func TestTierIsCappedByInstalledRBACTokenAndRelease(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, fp) // RBAC installed at tier 1
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp[:8], 2); kindOf(err) != KindInvalid {
		t.Fatalf("tier above installed RBAC was granted: %v", err)
	}
	// A cluster whose RBAC claims tier 4 still cannot be granted more than this release implements.
	e2 := newEnv(t)
	secret, _, _ := e2.core.CreateToken(e2.ctx, "a", "c", 2)
	d, _ := csr(t)
	r2, _ := e2.core.Enroll(e2.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 4})
	if err := e2.core.Approve(e2.ctx, "alex", r2.AgentId, fp[:8], 3); kindOf(err) != KindInvalid {
		t.Fatalf("unimplemented tier was granted: %v", err)
	}
	if _, _, err := e2.core.CreateToken(e2.ctx, "a", "c", 3); kindOf(err) != KindInvalid {
		t.Fatalf("token above implemented tier: %v", err)
	}
}

func TestOneLiveAgentPerClusterAndReplaceAfterRevoke(t *testing.T) {
	e := newEnv(t)
	a, _ := e.enroll(t, 2, fp)
	b, _ := e.enroll(t, 2, fp)
	if err := e.core.Approve(e.ctx, "alex", a.AgentId, fp, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", b.AgentId, fp, 1); kindOf(err) != KindConflict {
		t.Fatalf("second live agent for the same cluster: %v", err)
	}
	if err := e.core.Revoke(e.ctx, "alex", a.AgentId, "replaced"); err != nil {
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", b.AgentId, fp, 1); err != nil {
		t.Fatalf("replacement after revoke: %v", err)
	}
}

func TestRevokeStopsEverythingAndDropsTheStream(t *testing.T) {
	e := newEnv(t)
	var dropped string
	e.core.OnRevoke = func(id string) { dropped = id }
	resp, _ := e.enroll(t, 2, fp)
	_ = e.core.Approve(e.ctx, "alex", resp.AgentId, fp, 2)
	d, _ := csr(t)
	if _, _, err := e.core.Renew(e.ctx, resp.AgentId, d); err != nil {
		t.Fatalf("renew while approved: %v", err)
	}
	if err := e.core.Revoke(e.ctx, "alex", resp.AgentId, "compromised"); err != nil {
		t.Fatal(err)
	}
	if dropped != resp.AgentId {
		t.Fatal("live stream was not dropped")
	}
	if _, err := e.core.AuthorizeAgent(e.ctx, resp.AgentId); kindOf(err) != KindUnauthenticated {
		t.Fatalf("revoked agent still authorised: %v", err)
	}
	if _, _, err := e.core.Renew(e.ctx, resp.AgentId, d); kindOf(err) != KindUnauthenticated {
		t.Fatalf("revoked agent renewed: %v", err)
	}
	a, _ := e.st.GetAgent(e.ctx, resp.AgentId)
	if len(a.LeafDER) != 0 || a.Status != store.StatusRevoked || a.Reason != "compromised" {
		t.Fatalf("agent = %+v", a)
	}
	if p, err := e.core.Poll(e.ctx, "1.1.1.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret}); err == nil {
		t.Fatalf("revoked agent can still poll: %v", p.State)
	}
}

func TestRejectAndRenewDetails(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, fp)
	if err := e.core.Reject(e.ctx, "alex", resp.AgentId, "not ours"); err != nil {
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp, 1); kindOf(err) != KindConflict {
		t.Fatalf("approve after reject: %v", err)
	}

	r2, _ := e.enroll(t, 1, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	_ = e.core.Approve(e.ctx, "alex", r2.AgentId, "aaaaaaaa", 1)
	d, _ := csr(t)
	leaf, notAfter, err := e.core.Renew(e.ctx, r2.AgentId, d)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(notAfter) > pki.AgentCertTTL {
		t.Fatal("renewed certificate outlives the cap")
	}
	if c, _ := x509.ParseCertificate(leaf); c.Subject.CommonName != r2.AgentId {
		t.Fatal("renewed certificate has the wrong identity")
	}
	if a, _ := e.st.GetAgent(e.ctx, r2.AgentId); len(a.PollSecretHash) != 0 {
		t.Fatal("poll secret should be cleared after the first mTLS contact")
	}
	if _, _, err := e.core.Renew(e.ctx, r2.AgentId, []byte("junk")); kindOf(err) != KindInvalid {
		t.Fatalf("renew with junk csr: %v", err)
	}
}

func TestEnrollmentIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	e.core.EnrollRL = NewLimiter(1, 3)
	d, _ := csr(t)
	var limited int
	for i := 0; i < 10; i++ {
		tok, _ := NewTokenSecret() // guessing tokens
		if _, err := e.core.Enroll(e.ctx, "6.6.6.6", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: fp}); kindOf(err) == KindRateLimited {
			limited++
		}
	}
	if limited < 6 {
		t.Fatalf("only %d of 10 guesses were limited", limited)
	}
	tok, _ := NewTokenSecret()
	if _, err := e.core.Enroll(e.ctx, "7.7.7.7", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: fp}); kindOf(err) == KindRateLimited {
		t.Fatal("another address must not share the limit")
	}
}

func TestAuditTrailAndSecretsNeverStored(t *testing.T) {
	e := newEnv(t)
	resp, secret := e.enroll(t, 1, fp)
	_ = e.core.Approve(e.ctx, "alex", resp.AgentId, "wrongwrong", 1)
	_ = e.core.Approve(e.ctx, "alex", resp.AgentId, fp, 1)
	events, _ := e.st.ListAudit(e.ctx, "org-1", 50)
	got := map[string]bool{}
	for _, ev := range events {
		got[ev.Action] = true
		for _, s := range []string{secret, resp.PollSecret} {
			if s != "" && (contains(ev.Detail, s) || contains(ev.Action, s)) {
				t.Fatal("a secret leaked into the audit log")
			}
		}
	}
	for _, want := range []string{"token-created", "agent-enrolled", "approval-refused", "agent-approved"} {
		if !got[want] {
			t.Errorf("missing audit event %q (have %v)", want, got)
		}
	}
	toks, _ := e.st.ListTokens(e.ctx, "org-1")
	if len(toks) != 1 || toks[0].UsedBy != resp.AgentId {
		t.Fatalf("tokens = %+v", toks)
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
