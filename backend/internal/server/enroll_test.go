package server

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/approval"
	"continuum/internal/store"
)

// coded is one enrollment the way a current agent makes it: a key, a code, and only the code's hash sent.
type coded struct {
	resp  *continuumv1.EnrollResponse
	req   *continuumv1.EnrollRequest
	code  string
	token string
}

func (e *env) enrollCoded(t *testing.T, fingerprint string) coded {
	t.Helper()
	secret, _, err := e.core.CreateToken(e.ctx, "admin", "edge-patras", 2)
	if err != nil {
		t.Fatal(err)
	}
	return e.enrollCodedWith(t, secret, fingerprint, nil, "")
}

// enrollCodedWith sends an enrollment with the given token; key (a CSR made earlier) and code are reused for a retry.
func (e *env) enrollCodedWith(t *testing.T, token, fingerprint string, csrDER []byte, code string) coded {
	t.Helper()
	c, err := e.tryEnroll(t, token, fingerprint, csrDER, code)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (e *env) tryEnroll(t *testing.T, token, fingerprint string, csrDER []byte, code string) (coded, error) {
	t.Helper()
	if csrDER == nil {
		csrDER, _ = csr(t)
	}
	if code == "" {
		var err error
		if code, err = approval.New(); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := approval.Hash(code, csrDER)
	if err != nil {
		t.Fatal(err)
	}
	req := &continuumv1.EnrollRequest{Token: token, CsrDer: csrDER, ClusterFingerprint: fingerprint, InstalledAccessTier: 2, AgentVersion: "0.1.0", ApprovalCodeHash: hash}
	resp, err := e.core.Enroll(e.ctx, "10.0.0.1", req)
	return coded{resp: resp, req: req, code: code, token: token}, err
}

func (e *env) poll(t *testing.T, id, secret string) *continuumv1.PollResponse {
	t.Helper()
	p, err := e.core.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: id, PollSecret: secret})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (e *env) auditActions(t *testing.T) []string {
	t.Helper()
	rows, err := e.st.ListAudit(e.ctx, "org-1", 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(rows) - 1; i >= 0; i-- {
		out = append(out, rows[i].Action+": "+rows[i].Detail)
	}
	return out
}

func (e *env) chainOK(t *testing.T) {
	t.Helper()
	r, err := e.st.VerifyAudit(e.ctx)
	if err != nil || !r.OK || r.Unchained != 0 {
		t.Fatalf("audit chain: %+v %v", r, err)
	}
}

func attemptsLeft(err error) (int, bool) {
	var e *Error
	if errors.As(err, &e) {
		if n, ok := e.Data["attemptsLeft"].(int); ok {
			return n, true
		}
	}
	return 0, false
}

func TestApprovalCodeApprovesAndOnlyItsHashIsStored(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, fp)
	a, _ := e.st.GetAgent(e.ctx, c.resp.AgentId)
	if len(a.ApprovalHash) != approval.HashLen {
		t.Fatalf("hash = %d bytes", len(a.ApprovalHash))
	}
	if strings.Contains(string(a.ApprovalHash), strings.ReplaceAll(c.code, "-", "")) {
		t.Fatal("the code must not be stored")
	}

	// A wrong code says how many tries are left and is audited.
	err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, "ZZZZ-ZZZZ", 2)
	if kindOf(err) != KindInvalid {
		t.Fatalf("wrong code: %v", err)
	}
	if n, ok := attemptsLeft(err); !ok || n != 4 {
		t.Fatalf("attempts left = %d %v", n, ok)
	}
	if ag, _ := e.st.GetAgent(e.ctx, c.resp.AgentId); ag.Status != store.StatusPending {
		t.Fatal("still pending")
	}

	// The right code, typed loosely (lower case, no dash, O for zero), approves.
	typed := strings.ToLower(strings.ReplaceAll(c.code, "-", " "))
	typed = strings.ReplaceAll(typed, "0", "o")
	if err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, typed, 2); err != nil {
		t.Fatalf("right code %q: %v", typed, err)
	}
	if p := e.poll(t, c.resp.AgentId, c.resp.PollSecret); p.State != continuumv1.PollResponse_APPROVED {
		t.Fatalf("state = %v", p.State)
	}
	log := strings.Join(e.auditActions(t), "\n")
	for _, want := range []string{"agent-enrolled", "with an approval code", "approval-refused: wrong approval code (attempt 1 of 5)", "agent-approved", "approval code confirmed"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, strings.ReplaceAll(c.code, "-", "")) || strings.Contains(log, c.code) {
		t.Error("the code must never reach the audit trail")
	}
	e.chainOK(t)
}

func TestFiveWrongCodesRejectTheEnrollment(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, fp)
	for i, want := range []int{4, 3, 2, 1} {
		err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, fmt.Sprintf("ZZZZ-ZZZ%d", i), 2)
		if n, ok := attemptsLeft(err); !ok || n != want {
			t.Fatalf("attempt %d: left = %d %v (%v)", i+1, n, ok, err)
		}
	}
	err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, "ZZZZ-ZZZZ", 2)
	var se *Error
	if !errors.As(err, &se) || se.Data["locked"] != true {
		t.Fatalf("fifth wrong code should lock: %v", err)
	}
	if ag, _ := e.st.GetAgent(e.ctx, c.resp.AgentId); ag.Status != store.StatusRejected || !strings.Contains(ag.Reason, "wrong approval codes") {
		t.Fatalf("agent = %s %q", ag.Status, ag.Reason)
	}
	if p := e.poll(t, c.resp.AgentId, c.resp.PollSecret); p.State != continuumv1.PollResponse_REJECTED {
		t.Fatalf("the agent must learn it was rejected, got %v", p.State)
	}
	// Even the right code is too late now.
	if err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, c.code, 2); kindOf(err) != KindConflict {
		t.Fatalf("right code after the lock: %v", err)
	}
	log := strings.Join(e.auditActions(t), "\n")
	if !strings.Contains(log, "approval-locked") || strings.Count(log, "approval-refused") != 4 {
		t.Errorf("audit:\n%s", log)
	}
	e.chainOK(t)
}

func TestParallelGuessesCannotExceedTheAttemptLimit(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, fp)
	var wg sync.WaitGroup
	var wrong, locked int
	var mu sync.Mutex
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, fmt.Sprintf("ZZZZ-%04d", 1000+i), 2)
			mu.Lock()
			defer mu.Unlock()
			var se *Error
			if errors.As(err, &se) && se.Data != nil {
				if se.Data["locked"] == true {
					locked++
				} else {
					wrong++
				}
			}
		}()
	}
	wg.Wait()
	a, _ := e.st.GetAgent(e.ctx, c.resp.AgentId)
	if a.ApprovalAttempts > MaxApprovalAttempts+30 || a.Status != store.StatusRejected {
		t.Fatalf("agent = %+v", a)
	}
	if wrong > MaxApprovalAttempts-1 {
		t.Fatalf("%d guesses were answered as merely wrong; at most %d may be", wrong, MaxApprovalAttempts-1)
	}
	if locked < 1 {
		t.Fatal("the lock must be reported")
	}
}

func TestAMalformedCodeDoesNotSpendAnAttempt(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, fp)
	for _, junk := range []string{"", "abc", "K7QM-4TX", "K7QM-4TXDD", "K7QM-4TXU", "ünïcödé!"} {
		err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, junk, 2)
		if n, ok := attemptsLeft(err); kindOf(err) != KindInvalid || !ok || n != 5 {
			t.Errorf("%q: %v (left %d)", junk, err, n)
		}
	}
	if a, _ := e.st.GetAgent(e.ctx, c.resp.AgentId); a.ApprovalAttempts != 0 {
		t.Fatalf("attempts = %d", a.ApprovalAttempts)
	}
}

func TestAHashMadeForAnotherKeyDoesNotApprove(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "x", 2)
	victim, _ := csr(t)
	other, _ := csr(t)
	code, _ := approval.New()
	// The hash is bound to `other`'s key but sent with `victim`'s request.
	hash, _ := approval.Hash(code, other)
	resp, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: victim, ClusterFingerprint: fp, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, code, 2); kindOf(err) != KindInvalid {
		t.Fatalf("a code bound to another key approved: %v", err)
	}
}

func TestLegacyEnrollmentIsAllowedButFlagged(t *testing.T) {
	e := newEnv(t)
	resp, _ := e.enroll(t, 2, fp) // no approval hash: an older agent
	hub := NewHub(e.core)
	d, err := hub.State(e.ctx)
	if err != nil || len(d.Agents) != 1 || !d.Agents[0].LegacyEnrollment || d.Agents[0].ApprovalAttemptsLeft != nil {
		t.Fatalf("state: %+v %v", d.Agents, err)
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp[:8], 2); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(e.auditActions(t), "\n")
	if !strings.Contains(log, "legacy enrollment, without an approval code") || !strings.Contains(log, "LEGACY enrollment, no approval code") {
		t.Errorf("audit must say it was legacy:\n%s", log)
	}
	e.chainOK(t)
}

func TestLegacyApprovalCanBeRefused(t *testing.T) {
	e := newEnv(t)
	e.core.RefuseLegacyApproval = true
	resp, _ := e.enroll(t, 2, fp)
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fp[:8], 2); kindOf(err) != KindForbidden {
		t.Fatalf("err = %v", err)
	}
	if a, _ := e.st.GetAgent(e.ctx, resp.AgentId); a.Status != store.StatusPending {
		t.Fatal("must stay pending")
	}
	// A coded agent is unaffected.
	c := e.enrollCoded(t, "aaaaaaaa-1111-4222-8333-944455556666")
	if err := e.core.Approve(e.ctx, "alex", c.resp.AgentId, c.code, 2); err != nil {
		t.Fatal(err)
	}
}

// ---- token bound to a cluster ----

func TestATokenBoundToAClusterRefusesAnotherAndIsNotSpent(t *testing.T) {
	e := newEnv(t)
	secret, tok, err := e.core.CreateTokenFor(e.ctx, "admin", "prod", 2, "  "+strings.ToUpper(fp)+" ")
	if err != nil || tok.ExpectedFingerprint != fp {
		t.Fatalf("%+v %v", tok, err)
	}
	other := "11111111-2222-4333-8444-555566667777"
	_, err = e.tryEnroll(t, secret, other, nil, "")
	if kindOf(err) != KindForbidden {
		t.Fatalf("a different cluster: %v", err)
	}
	log := strings.Join(e.auditActions(t), "\n")
	if !strings.Contains(log, "enrollment-refused") || !strings.Contains(log, shortFP(fp)) || !strings.Contains(log, shortFP(other)) {
		t.Errorf("audit:\n%s", log)
	}
	// The token was not consumed: the right cluster can still use it.
	if _, err := e.tryEnroll(t, secret, fp, nil, ""); err != nil {
		t.Fatalf("the right cluster: %v", err)
	}
	e.chainOK(t)
}

func TestExpectedClusterMustBeAKubeSystemUID(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"prod", "1234", "8f3c2a9e-1111-4222-8333-94445555666"} {
		if _, _, err := e.core.CreateTokenFor(e.ctx, "admin", "prod", 2, bad); kindOf(err) != KindInvalid {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

// ---- idempotent enrollment ----

func TestRetriedEnrollmentContinuesTheSameAgent(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	d, key := csr(t)
	first := e.enrollCodedWith(t, secret, fp, d, "")
	// The response was lost; the agent, with the same key and code, asks again (its CSR is signed afresh).
	d2 := resign(t, key)
	again, err := e.tryEnroll(t, secret, fp, d2, first.code)
	if err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if again.resp.AgentId != first.resp.AgentId {
		t.Fatalf("a retry made agent %s, not %s", again.resp.AgentId, first.resp.AgentId)
	}
	agents, _ := e.st.ListAgents(e.ctx, "org-1")
	if len(agents) != 1 {
		t.Fatalf("%d agents", len(agents))
	}
	// The first poll secret went with the lost response; the new one works.
	if _, err := e.core.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: first.resp.AgentId, PollSecret: first.resp.PollSecret}); kindOf(err) != KindUnauthenticated {
		t.Fatalf("the old poll secret still works: %v", err)
	}
	if p := e.poll(t, again.resp.AgentId, again.resp.PollSecret); p.State != continuumv1.PollResponse_PENDING {
		t.Fatalf("state = %v", p.State)
	}
	// Approving works once, with the code the agent printed, and the retry is audited as a retry.
	if err := e.core.Approve(e.ctx, "alex", first.resp.AgentId, first.code, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.auditActions(t), "\n"), "agent-enrollment-retried") {
		t.Error("the retry must be in the audit trail")
	}
	e.chainOK(t)
}

func TestRetryAfterApprovalStillCollectsTheCertificate(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := csr(t)
	first := e.enrollCodedWith(t, secret, fp, d, "")
	if err := e.core.Approve(e.ctx, "alex", first.resp.AgentId, first.code, 2); err != nil {
		t.Fatal(err)
	}
	// The agent crashed before it collected the certificate and asks again.
	again, err := e.tryEnroll(t, secret, fp, d, first.code)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.poll(t, again.resp.AgentId, again.resp.PollSecret); p.State != continuumv1.PollResponse_APPROVED || len(p.LeafDer) == 0 {
		t.Fatalf("state = %v", p.State)
	}
	// Once the agent has connected with its certificate, the poll secret is gone and the token is truly spent.
	if err := e.st.ClearPollSecret(e.ctx, first.resp.AgentId); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tryEnroll(t, secret, fp, d, first.code); kindOf(err) != KindUnauthenticated {
		t.Fatalf("a retry after the agent went live: %v", err)
	}
}

func TestSpentTokenWithAnotherKeyOrCodeIsRefused(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := csr(t)
	first := e.enrollCodedWith(t, secret, fp, d, "")
	// Somebody else with the spent token and their own key.
	if _, err := e.tryEnroll(t, secret, fp, nil, ""); kindOf(err) != KindUnauthenticated {
		t.Fatalf("another key: %v", err)
	}
	// The same key with a different code is not a retry of that enrollment.
	if _, err := e.tryEnroll(t, secret, fp, d, ""); kindOf(err) != KindConflict {
		t.Fatalf("another code: %v", err)
	}
	// Nor is the same key claiming another cluster.
	if _, err := e.tryEnroll(t, secret, "11111111-2222-4333-8444-555566667777", d, first.code); kindOf(err) != KindUnauthenticated {
		t.Fatalf("another cluster: %v", err)
	}
	if agents, _ := e.st.ListAgents(e.ctx, "org-1"); len(agents) != 1 {
		t.Fatalf("%d agents", len(agents))
	}
}

func TestARejectedEnrollmentCannotBeRetriedBack(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := csr(t)
	first := e.enrollCodedWith(t, secret, fp, d, "")
	if err := e.core.Reject(e.ctx, "alex", first.resp.AgentId, "no"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tryEnroll(t, secret, fp, d, first.code); kindOf(err) != KindUnauthenticated {
		t.Fatalf("a rejected enrollment came back: %v", err)
	}
}

// ---- pending lifetime, polling hints ----

func TestPendingEnrollmentsExpireAndCanBeReopenedWithANewCode(t *testing.T) {
	e := newEnv(t)
	e.base.PendingTTL, e.core.PendingTTL = time.Hour, time.Hour
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := csr(t)
	first := e.enrollCodedWith(t, secret, fp, d, "")
	p := e.poll(t, first.resp.AgentId, first.resp.PollSecret)
	if p.State != continuumv1.PollResponse_PENDING || p.PollAfterSeconds != PollIntervalSecond || p.ServerTimeUnix != e.now.Unix() {
		t.Fatalf("pending poll: %+v", p)
	}
	if first.resp.PendingTtlSeconds != 3600 || first.resp.ServerTimeUnix != e.now.Unix() {
		t.Fatalf("enroll response: %+v", first.resp)
	}

	*e.now = e.now.Add(61 * time.Minute)
	if p := e.poll(t, first.resp.AgentId, first.resp.PollSecret); p.State != continuumv1.PollResponse_EXPIRED {
		t.Fatalf("after the lifetime: %v", p.State)
	}
	if err := e.core.Approve(e.ctx, "alex", first.resp.AgentId, first.code, 2); kindOf(err) != KindConflict {
		t.Fatalf("approving an expired request: %v", err)
	}
	evs, _ := e.st.ListEvents(e.ctx, "org-1", store.EventQuery{})
	found := false
	for _, ev := range evs {
		found = found || ev.Kind == "enrollment-expired"
	}
	if !found || !strings.Contains(strings.Join(e.auditActions(t), "\n"), "agent-enrollment-expired") {
		t.Fatalf("expiry must leave an event and an audit row (event found: %v)", found)
	}

	// The agent enrolls again with the same token and key and a new code: the same record, pending again.
	second, err := e.tryEnroll(t, secret, fp, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.resp.AgentId != first.resp.AgentId || second.code == first.code {
		t.Fatalf("reopen: agent %s code %s", second.resp.AgentId, second.code)
	}
	if err := e.core.Approve(e.ctx, "alex", first.resp.AgentId, first.code, 2); kindOf(err) != KindInvalid {
		t.Fatalf("the old code must not work: %v", err)
	}
	if err := e.core.Approve(e.ctx, "alex", second.resp.AgentId, second.code, 2); err != nil {
		t.Fatalf("the new code: %v", err)
	}
	e.chainOK(t)
}

func TestSweepExpiresAndPurges(t *testing.T) {
	e := newEnv(t)
	e.core.PendingTTL = time.Hour
	c := e.enrollCoded(t, fp)
	*e.now = e.now.Add(2 * time.Hour)
	if n := e.core.ExpirePending(e.ctx, "org-1"); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if a, _ := e.st.GetAgent(e.ctx, c.resp.AgentId); a.Status != store.StatusExpired {
		t.Fatalf("status = %s", a.Status)
	}
	if n := e.core.ExpirePending(e.ctx, "org-1"); n != 0 {
		t.Fatalf("expired again: %d", n)
	}
	*e.now = e.now.Add(8 * 24 * time.Hour)
	e.core.ExpirePending(e.ctx, "org-1")
	if _, err := e.st.GetAgent(e.ctx, c.resp.AgentId); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an expired record nobody reopened should be deleted: %v", err)
	}
}

func TestStateShowsApprovalDetailsForPendingAgents(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, fp)
	hub := NewHub(e.core)
	_ = e.core.Approve(e.ctx, "alex", c.resp.AgentId, "ZZZZ-ZZZZ", 2)
	d, _ := hub.State(e.ctx)
	a := d.Agents[0]
	if a.LegacyEnrollment || a.ApprovalAttemptsLeft == nil || *a.ApprovalAttemptsLeft != 4 || a.PendingExpiresAt == "" {
		t.Fatalf("%+v", a)
	}
}

// ---- admin API ----

func TestAdminApproveWithCodeAnswersWithAttemptsLeft(t *testing.T) {
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", RoleOwner)
	c := a.enrollCoded(t, fp)
	r := a.do("POST", "/api/v1/agents/"+c.resp.AgentId+"/approve", map[string]any{"code": "ZZZZ-ZZZZ", "tier": 2}, withCookie(owner))
	if r.Code != 400 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	if j["attemptsLeft"] != float64(4) || j["locked"] != false || !strings.Contains(j["error"].(string), "4 attempts left") {
		t.Fatalf("%v", j)
	}
	if r := a.do("POST", "/api/v1/agents/"+c.resp.AgentId+"/approve", map[string]any{"code": strings.ToLower(c.code), "tier": 2}, withCookie(owner)); r.Code != 204 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestAdminTokenCanBeBoundToACluster(t *testing.T) {
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", RoleOwner)
	r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "prod", "tier": 1, "expectedCluster": fp}, withCookie(owner))
	if r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if meta := r.json(t)["meta"].(map[string]any); meta["expectedCluster"] != fp {
		t.Fatalf("%v", meta)
	}
	if r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "prod", "tier": 1, "expectedCluster": "nope"}, withCookie(owner)); r.Code != 400 {
		t.Fatalf("a bad cluster id: %d", r.Code)
	}
	list := a.do("GET", "/api/v1/tokens", nil, withCookie(owner))
	if !strings.Contains(list.Body.String(), `"expectedCluster":"`+fp+`"`) {
		t.Fatalf("%s", list.Body.String())
	}
}

// resign makes another certificate request with the same key (a signature is random, so a retry's request
// is never byte-identical to the first).
func resign(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
