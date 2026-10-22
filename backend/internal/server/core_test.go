package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/kernel"
	"continuum/internal/pki"
	"continuum/internal/store"
	"continuum/internal/testkit"
)

type env struct {
	w    *testkit.World
	base *Core // the platform-wide core: accounts, sign-in, the gRPC listener
	core *Core // organisation "org-1", which most tests work in
	st   *store.SQLite
	now  *time.Time
	ctx  context.Context
	// dbPath is the SQLite file behind st, for tests that must look at what is really stored.
	dbPath string
	// codes are the approval codes of the agents enroll made, by agent id: approve types them.
	codes map[string]string
}

// newEnv is a server with one organisation, "org-1", owned by a user nobody signs in as.
func newEnv(t *testing.T) *env {
	t.Helper()
	e := newEnvBare(t)
	e.w.CreateOrg1(t)
	return e
}

// newEnvBare has no accounts and no organisation yet (BootstrapAdmin would create "org-1").
func newEnvBare(t *testing.T) *env {
	t.Helper()
	w := testkit.NewBareWorld(t)
	base := wrap(w.Base)
	return &env{w: w, base: base, core: base.ForOrg("org-1"), st: w.Store, now: w.Now, ctx: w.Ctx, dbPath: w.DBPath, codes: map[string]string{}}
}

func (e *env) enroll(t *testing.T, tier uint32, fingerprint string) (*continuumv1.EnrollResponse, string) {
	t.Helper()
	resp, secret, code := testkit.Enroll(t, e.ctx, e.core.Agents, tier, fingerprint)
	e.codes[resp.AgentId] = code
	return resp, secret
}

// approve is Approve with the code enroll made for that agent.
func (e *env) approve(id string, tier int) error {
	return e.core.Agents.Approve(e.ctx, "alex", id, e.codes[id], tier)
}

func TestEnrollApprovePollHappyPath(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, testkit.FP)
	poll := func() *continuumv1.PollResponse {
		p, err := e.core.Agents.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if poll().State != continuumv1.PollResponse_PENDING {
		t.Fatal("should be pending before approval")
	}
	if err := e.approve(resp.AgentId, 2); err != nil {
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
	a, _ := e.st.GetAgent(e.ctx, "org-1", resp.AgentId)
	if a.Name != "edge-patras" || a.AccessTier != 2 || a.ClusterID != kernel.ClusterIDFor("org-1", testkit.FP) || a.ApprovedBy != "alex" {
		t.Fatalf("agent = %+v", a)
	}
}

func TestTokenIsSingleUseEvenUnderRace(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "c", 1)
	var wins int32
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, _ := testkit.CSR(t)
			if _, err := e.core.Agents.Enroll(e.ctx, "10.0.0.2", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)}); err == nil {
				atomic.AddInt32(&wins, 1)
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d agents enrolled with one token, want exactly 1", wins)
	}
	agentList, _ := e.st.ListAgents(e.ctx, "org-1")
	if len(agentList) != 1 {
		t.Fatalf("%d agent rows", len(agentList))
	}
}

func TestTokenExpiryUnknownAndMalformed(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "c", 1)
	*e.now = e.now.Add(kernel.TokenTTL + time.Second)
	d, _ := testkit.CSR(t)
	_, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)})
	if testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("expired token: %v", err)
	}
	unknown, _ := kernel.NewTokenSecret()
	_, err = e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: unknown, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)})
	if testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("unknown token: %v", err)
	}
	for _, bad := range []string{"", "cnt_", "short", "cnt_" + string(make([]byte, 43))} {
		_, err = e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: bad, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)})
		if testkit.KindOf(err) != kernel.KindUnauthenticated {
			t.Fatalf("malformed %q: %v", bad, err)
		}
	}
}

func TestBadCSRDoesNotBurnTheToken(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "c", 1)
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsaCSR, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, rk)
	if _, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: rsaCSR, ClusterFingerprint: testkit.FP}); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("rsa csr: %v", err)
	}
	if _, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: []byte("x"), ClusterFingerprint: testkit.FP}); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("garbage csr: %v", err)
	}
	d, _ := testkit.CSR(t)
	if _, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: "bad fp!"}); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("bad fingerprint: %v", err)
	}
	if _, err := e.core.Agents.Enroll(e.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)}); err != nil {
		t.Fatalf("token should still work after malformed attempts: %v", err)
	}
}

func TestPollNeedsTheSecretAndDoesNotRevealAgents(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, testkit.FP)
	_, err1 := e.core.Agents.Poll(e.ctx, "9.9.9.9", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: "wrong"})
	_, err2 := e.core.Agents.Poll(e.ctx, "9.9.9.9", &continuumv1.PollRequest{AgentId: "ag-doesnotexist", PollSecret: "wrong"})
	if testkit.KindOf(err1) != kernel.KindUnauthenticated || testkit.KindOf(err2) != kernel.KindUnauthenticated || err1.Error() != err2.Error() {
		t.Fatalf("poll must answer identically: %v / %v", err1, err2)
	}
}

func TestTierIsCappedByInstalledRBACTokenAndRelease(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, testkit.FP) // RBAC installed at tier 1
	if err := e.approve(resp.AgentId, 2); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("tier above installed RBAC was granted: %v", err)
	}
	// A cluster whose RBAC claims tier 4 still cannot be granted more than this release implements.
	e2 := newEnv(t)
	secret, _, _ := e2.core.Agents.CreateToken(e2.ctx, "a", "c", 2)
	d, _ := testkit.CSR(t)
	hash, code := testkit.ApprovalFor(t, d)
	r2, _ := e2.core.Agents.Enroll(e2.ctx, "1.1.1.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: 4, ApprovalCodeHash: hash})
	if err := e2.core.Agents.Approve(e2.ctx, "alex", r2.AgentId, code, 3); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("unimplemented tier was granted: %v", err)
	}
	if _, _, err := e2.core.Agents.CreateToken(e2.ctx, "a", "c", 3); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("token above implemented tier: %v", err)
	}
}

func TestOneLiveAgentPerClusterAndReplaceAfterRevoke(t *testing.T) {
	e := newEnv(t)
	a, _ := e.enroll(t, 2, testkit.FP)
	b, _ := e.enroll(t, 2, testkit.FP)
	if err := e.approve(a.AgentId, 1); err != nil {
		t.Fatal(err)
	}
	if err := e.approve(b.AgentId, 1); testkit.KindOf(err) != kernel.KindConflict {
		t.Fatalf("second live agent for the same cluster: %v", err)
	}
	if err := e.core.Agents.Revoke(e.ctx, "alex", a.AgentId, "replaced"); err != nil {
		t.Fatal(err)
	}
	if err := e.approve(b.AgentId, 1); err != nil {
		t.Fatalf("replacement after revoke: %v", err)
	}
}

func TestRevokeStopsEverythingAndDropsTheStream(t *testing.T) {
	e := newEnv(t)
	var dropped string
	e.core.OnRevoke = func(id string) { dropped = id }
	resp, _ := e.enroll(t, 2, testkit.FP)
	_ = e.approve(resp.AgentId, 2)
	d, _ := testkit.CSR(t)
	if _, _, err := e.core.Agents.Renew(e.ctx, resp.AgentId, d); err != nil {
		t.Fatalf("renew while approved: %v", err)
	}
	if err := e.core.Agents.Revoke(e.ctx, "alex", resp.AgentId, "compromised"); err != nil {
		t.Fatal(err)
	}
	if dropped != resp.AgentId {
		t.Fatal("live stream was not dropped")
	}
	if _, err := e.core.Agents.AuthorizeAgent(e.ctx, resp.AgentId); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("revoked agent still authorised: %v", err)
	}
	if _, _, err := e.core.Agents.Renew(e.ctx, resp.AgentId, d); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("revoked agent renewed: %v", err)
	}
	a, _ := e.st.GetAgent(e.ctx, "org-1", resp.AgentId)
	if len(a.LeafDER) != 0 || a.Status != store.StatusRevoked || a.Reason != "compromised" {
		t.Fatalf("agent = %+v", a)
	}
	if p, err := e.core.Agents.Poll(e.ctx, "1.1.1.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret}); err == nil {
		t.Fatalf("revoked agent can still poll: %v", p.State)
	}
}

func TestRejectAndRenewDetails(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 1, testkit.FP)
	if err := e.core.Agents.Reject(e.ctx, "alex", resp.AgentId, "not ours"); err != nil {
		t.Fatal(err)
	}
	if err := e.approve(resp.AgentId, 1); testkit.KindOf(err) != kernel.KindConflict {
		t.Fatalf("approve after reject: %v", err)
	}

	r2, _ := e.enroll(t, 1, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")
	_ = e.approve(r2.AgentId, 1)
	d, _ := testkit.CSR(t)
	leaf, notAfter, err := e.core.Agents.Renew(e.ctx, r2.AgentId, d)
	if err != nil {
		t.Fatal(err)
	}
	if time.Until(notAfter) > pki.AgentCertTTL {
		t.Fatal("renewed certificate outlives the cap")
	}
	if c, _ := x509.ParseCertificate(leaf); c.Subject.CommonName != r2.AgentId {
		t.Fatal("renewed certificate has the wrong identity")
	}
	if a, _ := e.st.GetAgent(e.ctx, "org-1", r2.AgentId); len(a.PollSecretHash) != 0 {
		t.Fatal("poll secret should be cleared after the first mTLS contact")
	}
	if _, _, err := e.core.Agents.Renew(e.ctx, r2.AgentId, []byte("junk")); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("renew with junk csr: %v", err)
	}
}

func TestEnrollmentIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	e.core.EnrollRL = kernel.NewLimiter(1, 3)
	d, _ := testkit.CSR(t)
	var limited int
	for i := 0; i < 10; i++ {
		tok, _ := kernel.NewTokenSecret() // guessing tokens
		if _, err := e.core.Agents.Enroll(e.ctx, "6.6.6.6", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)}); testkit.KindOf(err) == kernel.KindRateLimited {
			limited++
		}
	}
	if limited < 6 {
		t.Fatalf("only %d of 10 guesses were limited", limited)
	}
	tok, _ := kernel.NewTokenSecret()
	if _, err := e.core.Agents.Enroll(e.ctx, "7.7.7.7", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: testkit.FP, ApprovalCodeHash: testkit.HashFor(t, d)}); testkit.KindOf(err) == kernel.KindRateLimited {
		t.Fatal("another address must not share the limit")
	}
}

func TestAuditTrailAndSecretsNeverStored(t *testing.T) {
	e := newEnv(t)
	resp, secret := e.enroll(t, 1, testkit.FP)
	_ = e.core.Agents.Approve(e.ctx, "alex", resp.AgentId, "ZZZZ-ZZZZ", 1)
	_ = e.approve(resp.AgentId, 1)
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
