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

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/accounts"
	"ikhnos/internal/approval"
	"ikhnos/internal/kernel"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
)

// coded is one enrollment the way a current agent makes it: a key, a code, and only the code's hash sent.
type coded = testkit.Coded

func (e *env) enrollCoded(t *testing.T, fingerprint string) coded {
	t.Helper()
	return testkit.EnrollCoded(t, e.ctx, e.core.Agents, fingerprint)
}

// enrollCodedWith sends an enrollment with the given token; key (a CSR made earlier) and code are reused for a retry.
func (e *env) enrollCodedWith(t *testing.T, token, fingerprint string, csrDER []byte, code string) coded {
	t.Helper()
	return testkit.EnrollCodedWith(t, e.ctx, e.core.Agents, token, fingerprint, csrDER, code)
}

func (e *env) tryEnroll(t *testing.T, token, fingerprint string, csrDER []byte, code string) (coded, error) {
	t.Helper()
	return testkit.TryEnroll(t, e.ctx, e.core.Agents, token, fingerprint, csrDER, code)
}

func (e *env) poll(t *testing.T, id, secret string) *ikhnosv1.PollResponse {
	t.Helper()
	p, err := e.core.Agents.Poll(e.ctx, "10.0.0.1", &ikhnosv1.PollRequest{AgentId: id, PollSecret: secret})
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
	var e *kernel.Error
	if errors.As(err, &e) {
		if n, ok := e.Data["attemptsLeft"].(int); ok {
			return n, true
		}
	}
	return 0, false
}

func TestApprovalCodeApprovesAndOnlyItsHashIsStored(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, testkit.FP)
	a, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId)
	if len(a.ApprovalHash) != approval.HashLen {
		t.Fatalf("hash = %d bytes", len(a.ApprovalHash))
	}
	if strings.Contains(string(a.ApprovalHash), strings.ReplaceAll(c.Code, "-", "")) {
		t.Fatal("the code must not be stored")
	}

	// A wrong code says how many tries are left and is audited.
	err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, "ZZZZ-ZZZZ", 2)
	if testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("wrong code: %v", err)
	}
	if n, ok := attemptsLeft(err); !ok || n != 4 {
		t.Fatalf("attempts left = %d %v", n, ok)
	}
	if ag, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId); ag.Status != store.StatusPending {
		t.Fatal("still pending")
	}

	// The right code, typed loosely (lower case, no dash, O for zero), approves.
	typed := strings.ToLower(strings.ReplaceAll(c.Code, "-", " "))
	typed = strings.ReplaceAll(typed, "0", "o")
	if err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, typed, 2); err != nil {
		t.Fatalf("right code %q: %v", typed, err)
	}
	if p := e.poll(t, c.Resp.AgentId, c.Resp.PollSecret); p.State != ikhnosv1.PollResponse_APPROVED {
		t.Fatalf("state = %v", p.State)
	}
	log := strings.Join(e.auditActions(t), "\n")
	for _, want := range []string{"agent-enrolled", "approval-refused: wrong approval code (attempt 1 of 5)", "agent-approved", "approval code confirmed"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, strings.ReplaceAll(c.Code, "-", "")) || strings.Contains(log, c.Code) {
		t.Error("the code must never reach the audit trail")
	}
	e.chainOK(t)
}

func TestFiveWrongCodesRejectTheEnrollment(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, testkit.FP)
	for i, want := range []int{4, 3, 2, 1} {
		err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, fmt.Sprintf("ZZZZ-ZZZ%d", i), 2)
		if n, ok := attemptsLeft(err); !ok || n != want {
			t.Fatalf("attempt %d: left = %d %v (%v)", i+1, n, ok, err)
		}
	}
	err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, "ZZZZ-ZZZZ", 2)
	var se *kernel.Error
	if !errors.As(err, &se) || se.Data["locked"] != true {
		t.Fatalf("fifth wrong code should lock: %v", err)
	}
	if ag, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId); ag.Status != store.StatusRejected || !strings.Contains(ag.Reason, "wrong approval codes") {
		t.Fatalf("agent = %s %q", ag.Status, ag.Reason)
	}
	if p := e.poll(t, c.Resp.AgentId, c.Resp.PollSecret); p.State != ikhnosv1.PollResponse_REJECTED {
		t.Fatalf("the agent must learn it was rejected, got %v", p.State)
	}
	// Even the right code is too late now.
	if err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, c.Code, 2); testkit.KindOf(err) != kernel.KindConflict {
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
	c := e.enrollCoded(t, testkit.FP)
	var wg sync.WaitGroup
	var wrong, locked int
	var mu sync.Mutex
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, fmt.Sprintf("ZZZZ-%04d", 1000+i), 2)
			mu.Lock()
			defer mu.Unlock()
			var se *kernel.Error
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
	a, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId)
	if a.ApprovalAttempts > kernel.MaxApprovalAttempts+30 || a.Status != store.StatusRejected {
		t.Fatalf("agent = %+v", a)
	}
	if wrong > kernel.MaxApprovalAttempts-1 {
		t.Fatalf("%d guesses were answered as merely wrong; at most %d may be", wrong, kernel.MaxApprovalAttempts-1)
	}
	if locked < 1 {
		t.Fatal("the lock must be reported")
	}
}

func TestAMalformedCodeDoesNotSpendAnAttempt(t *testing.T) {
	e := newEnv(t)
	c := e.enrollCoded(t, testkit.FP)
	for _, junk := range []string{"", "abc", "K7QM-4TX", "K7QM-4TXDD", "K7QM-4TXU", "ünïcödé!"} {
		err := e.core.Agents.Approve(e.ctx, "alex", c.Resp.AgentId, junk, 2)
		if n, ok := attemptsLeft(err); testkit.KindOf(err) != kernel.KindInvalid || !ok || n != 5 {
			t.Errorf("%q: %v (left %d)", junk, err, n)
		}
	}
	if a, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId); a.ApprovalAttempts != 0 {
		t.Fatalf("attempts = %d", a.ApprovalAttempts)
	}
}

func TestAHashMadeForAnotherKeyDoesNotApprove(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "x", 2)
	victim, _ := testkit.CSR(t)
	other, _ := testkit.CSR(t)
	code, _ := approval.New()
	// The hash is bound to `other`'s key but sent with `victim`'s request.
	hash, _ := approval.Hash(code, other)
	resp, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &ikhnosv1.EnrollRequest{Token: secret, CsrDer: victim, ClusterFingerprint: testkit.FP, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.Agents.Approve(e.ctx, "alex", resp.AgentId, code, 2); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("a code bound to another key approved: %v", err)
	}
}

// An enrollment without an approval code hash cannot be approved by anything the enroller does not control, so it is
// not accepted at all (it used to be approved by echoing the first characters of the cluster fingerprint it reported
// itself).
func TestEnrollWithoutAnApprovalCodeIsRefused(t *testing.T) {
	e := newEnv(t)
	secret, _, err := e.core.Agents.CreateToken(e.ctx, "admin", "edge-patras", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := testkit.CSR(t)
	short, _ := testkit.ApprovalFor(t, d)
	for _, hash := range [][]byte{nil, {}, short[:8]} {
		_, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &ikhnosv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: testkit.FP, InstalledAccessTier: 2, ApprovalCodeHash: hash})
		if testkit.KindOf(err) != kernel.KindInvalid {
			t.Fatalf("hash of %d bytes: %v", len(hash), err)
		}
	}
	if agentList, _ := e.st.ListAgents(e.ctx, "org-1"); len(agentList) != 0 {
		t.Fatalf("%d agents were created", len(agentList))
	}
	// The refusals spent nothing: the token still enrolls an agent that has a code.
	e.enrollCodedWith(t, secret, testkit.FP, nil, "")
}

// ---- token bound to a cluster ----

func TestATokenBoundToAClusterRefusesAnotherAndIsNotSpent(t *testing.T) {
	e := newEnv(t)
	secret, tok, err := e.core.Agents.CreateTokenFor(e.ctx, "admin", "prod", 2, "  "+strings.ToUpper(testkit.FP)+" ")
	if err != nil || tok.ExpectedFingerprint != testkit.FP {
		t.Fatalf("%+v %v", tok, err)
	}
	other := "11111111-2222-4333-8444-555566667777"
	_, err = e.tryEnroll(t, secret, other, nil, "")
	if testkit.KindOf(err) != kernel.KindForbidden {
		t.Fatalf("a different cluster: %v", err)
	}
	log := strings.Join(e.auditActions(t), "\n")
	if !strings.Contains(log, "enrollment-refused") || !strings.Contains(log, kernel.ShortFP(testkit.FP)) || !strings.Contains(log, kernel.ShortFP(other)) {
		t.Errorf("audit:\n%s", log)
	}
	// The token was not consumed: the right cluster can still use it.
	if _, err := e.tryEnroll(t, secret, testkit.FP, nil, ""); err != nil {
		t.Fatalf("the right cluster: %v", err)
	}
	e.chainOK(t)
}

func TestExpectedClusterMustBeAKubeSystemUID(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{"prod", "1234", "8f3c2a9e-1111-4222-8333-94445555666"} {
		if _, _, err := e.core.Agents.CreateTokenFor(e.ctx, "admin", "prod", 2, bad); testkit.KindOf(err) != kernel.KindInvalid {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}

// ---- idempotent enrollment ----

func TestRetriedEnrollmentContinuesTheSameAgent(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	d, key := testkit.CSR(t)
	first := e.enrollCodedWith(t, secret, testkit.FP, d, "")
	// The response was lost; the agent, with the same key and code, asks again (its CSR is signed afresh).
	d2 := resign(t, key)
	again, err := e.tryEnroll(t, secret, testkit.FP, d2, first.Code)
	if err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if again.Resp.AgentId != first.Resp.AgentId {
		t.Fatalf("a retry made agent %s, not %s", again.Resp.AgentId, first.Resp.AgentId)
	}
	agentList, _ := e.st.ListAgents(e.ctx, "org-1")
	if len(agentList) != 1 {
		t.Fatalf("%d agents", len(agentList))
	}
	// The first poll secret went with the lost response; the new one works.
	if _, err := e.core.Agents.Poll(e.ctx, "10.0.0.1", &ikhnosv1.PollRequest{AgentId: first.Resp.AgentId, PollSecret: first.Resp.PollSecret}); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("the old poll secret still works: %v", err)
	}
	if p := e.poll(t, again.Resp.AgentId, again.Resp.PollSecret); p.State != ikhnosv1.PollResponse_PENDING {
		t.Fatalf("state = %v", p.State)
	}
	// Approving works once, with the code the agent printed, and the retry is audited as a retry.
	if err := e.core.Agents.Approve(e.ctx, "alex", first.Resp.AgentId, first.Code, 2); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(e.auditActions(t), "\n"), "agent-enrollment-retried") {
		t.Error("the retry must be in the audit trail")
	}
	e.chainOK(t)
}

func TestRetryAfterApprovalStillCollectsTheCertificate(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := testkit.CSR(t)
	first := e.enrollCodedWith(t, secret, testkit.FP, d, "")
	if err := e.core.Agents.Approve(e.ctx, "alex", first.Resp.AgentId, first.Code, 2); err != nil {
		t.Fatal(err)
	}
	// The agent crashed before it collected the certificate and asks again.
	again, err := e.tryEnroll(t, secret, testkit.FP, d, first.Code)
	if err != nil {
		t.Fatal(err)
	}
	if p := e.poll(t, again.Resp.AgentId, again.Resp.PollSecret); p.State != ikhnosv1.PollResponse_APPROVED || len(p.LeafDer) == 0 {
		t.Fatalf("state = %v", p.State)
	}
	// Once the agent has connected with its certificate, the poll secret is gone and the token is truly spent.
	if err := e.st.ClearPollSecret(e.ctx, "org-1", first.Resp.AgentId); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tryEnroll(t, secret, testkit.FP, d, first.Code); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("a retry after the agent went live: %v", err)
	}
}

func TestSpentTokenWithAnotherKeyOrCodeIsRefused(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := testkit.CSR(t)
	first := e.enrollCodedWith(t, secret, testkit.FP, d, "")
	// Somebody else with the spent token and their own key.
	if _, err := e.tryEnroll(t, secret, testkit.FP, nil, ""); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("another key: %v", err)
	}
	// The same key with a different code is not a retry of that enrollment.
	if _, err := e.tryEnroll(t, secret, testkit.FP, d, ""); testkit.KindOf(err) != kernel.KindConflict {
		t.Fatalf("another code: %v", err)
	}
	// Nor is the same key claiming another cluster.
	if _, err := e.tryEnroll(t, secret, "11111111-2222-4333-8444-555566667777", d, first.Code); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("another cluster: %v", err)
	}
	if agentList, _ := e.st.ListAgents(e.ctx, "org-1"); len(agentList) != 1 {
		t.Fatalf("%d agents", len(agentList))
	}
}

func TestARejectedEnrollmentCannotBeRetriedBack(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := testkit.CSR(t)
	first := e.enrollCodedWith(t, secret, testkit.FP, d, "")
	if err := e.core.Agents.Reject(e.ctx, "alex", first.Resp.AgentId, "no"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tryEnroll(t, secret, testkit.FP, d, first.Code); testkit.KindOf(err) != kernel.KindUnauthenticated {
		t.Fatalf("a rejected enrollment came back: %v", err)
	}
}

// ---- pending lifetime, polling hints ----

func TestPendingEnrollmentsExpireAndCanBeReopenedWithANewCode(t *testing.T) {
	e := newEnv(t)
	e.base.PendingTTL, e.core.PendingTTL = time.Hour, time.Hour
	secret, _, _ := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	d, _ := testkit.CSR(t)
	first := e.enrollCodedWith(t, secret, testkit.FP, d, "")
	p := e.poll(t, first.Resp.AgentId, first.Resp.PollSecret)
	if p.State != ikhnosv1.PollResponse_PENDING || p.PollAfterSeconds != kernel.PollIntervalSecond || p.ServerTimeUnix != e.now.Unix() {
		t.Fatalf("pending poll: %+v", p)
	}
	if first.Resp.PendingTtlSeconds != 3600 || first.Resp.ServerTimeUnix != e.now.Unix() {
		t.Fatalf("enroll response: %+v", first.Resp)
	}

	*e.now = e.now.Add(61 * time.Minute)
	if p := e.poll(t, first.Resp.AgentId, first.Resp.PollSecret); p.State != ikhnosv1.PollResponse_EXPIRED {
		t.Fatalf("after the lifetime: %v", p.State)
	}
	if err := e.core.Agents.Approve(e.ctx, "alex", first.Resp.AgentId, first.Code, 2); testkit.KindOf(err) != kernel.KindConflict {
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
	second, err := e.tryEnroll(t, secret, testkit.FP, d, "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Resp.AgentId != first.Resp.AgentId || second.Code == first.Code {
		t.Fatalf("reopen: agent %s code %s", second.Resp.AgentId, second.Code)
	}
	if err := e.core.Agents.Approve(e.ctx, "alex", first.Resp.AgentId, first.Code, 2); testkit.KindOf(err) != kernel.KindInvalid {
		t.Fatalf("the old code must not work: %v", err)
	}
	if err := e.core.Agents.Approve(e.ctx, "alex", second.Resp.AgentId, second.Code, 2); err != nil {
		t.Fatalf("the new code: %v", err)
	}
	e.chainOK(t)
}

func TestSweepExpiresAndPurges(t *testing.T) {
	e := newEnv(t)
	e.core.PendingTTL = time.Hour
	c := e.enrollCoded(t, testkit.FP)
	*e.now = e.now.Add(2 * time.Hour)
	if n := e.core.Agents.ExpirePending(e.ctx, "org-1"); n != 1 {
		t.Fatalf("expired %d", n)
	}
	if a, _ := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId); a.Status != store.StatusExpired {
		t.Fatalf("status = %s", a.Status)
	}
	if n := e.core.Agents.ExpirePending(e.ctx, "org-1"); n != 0 {
		t.Fatalf("expired again: %d", n)
	}
	*e.now = e.now.Add(8 * 24 * time.Hour)
	e.core.Agents.ExpirePending(e.ctx, "org-1")
	if _, err := e.st.GetAgent(e.ctx, "org-1", c.Resp.AgentId); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an expired record nobody reopened should be deleted: %v", err)
	}
}

// ---- admin API ----

func TestAdminApproveWithCodeAnswersWithAttemptsLeft(t *testing.T) {
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", accounts.RoleOwner)
	c := a.enrollCoded(t, testkit.FP)
	r := a.do("POST", "/api/v1/agents/"+c.Resp.AgentId+"/approve", map[string]any{"code": "ZZZZ-ZZZZ", "tier": 2}, withCookie(owner))
	if r.Code != 400 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	if j["attemptsLeft"] != float64(4) || j["locked"] != false || !strings.Contains(j["error"].(string), "4 attempts left") {
		t.Fatalf("%v", j)
	}
	if r := a.do("POST", "/api/v1/agents/"+c.Resp.AgentId+"/approve", map[string]any{"code": strings.ToLower(c.Code), "tier": 2}, withCookie(owner)); r.Code != 204 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
}

func TestAdminTokenCanBeBoundToACluster(t *testing.T) {
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", accounts.RoleOwner)
	r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "prod", "tier": 1, "expectedCluster": testkit.FP}, withCookie(owner))
	if r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if meta := r.json(t)["meta"].(map[string]any); meta["expectedCluster"] != testkit.FP {
		t.Fatalf("%v", meta)
	}
	if r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "prod", "tier": 1, "expectedCluster": "nope"}, withCookie(owner)); r.Code != 400 {
		t.Fatalf("a bad cluster id: %d", r.Code)
	}
	list := a.do("GET", "/api/v1/tokens", nil, withCookie(owner))
	if !strings.Contains(list.Body.String(), `"expectedCluster":"`+testkit.FP+`"`) {
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
