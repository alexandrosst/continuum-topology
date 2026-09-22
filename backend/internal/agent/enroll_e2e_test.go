package agent_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent"
	agentcli "continuum/internal/cli/agent"
	"continuum/internal/pki"
	"continuum/internal/server"
	"continuum/internal/store"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// ---- a small rig: a real server on a real TLS listener and a fake cluster ----

type erig struct {
	t    *testing.T
	core *server.Core
	hub  *server.Hub
	st   *store.SQLite
	ca   *pki.CA
	addr string
	kube *fake.Clientset
	gets *atomic.Int64 // how many times the server looked an agent up (a poll does exactly one)
}

type countingStore struct {
	store.Store
	gets *atomic.Int64
}

func (c *countingStore) GetAgent(ctx context.Context, id string) (store.Agent, error) {
	c.gets.Add(1)
	return c.Store.GetAgent(ctx, id)
}

const rigUID = "e2e00010-aaaa-4bbb-8ccc-dddddddddddd"

func newERig(t *testing.T, tune func(*server.Core)) *erig {
	t.Helper()
	st, err := store.OpenSQLite(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, _ := pki.LoadOrCreate(t.TempDir())
	gets := &atomic.Int64{}
	core := server.NewCore(&countingStore{Store: st, gets: gets}, ca, "org", nil)
	core.EnrollRL = server.NewLimiter(6000, 1000)
	core.RenewRL = server.NewLimiter(6000, 1000)
	if tune != nil {
		tune(core)
	}
	hub := server.NewHub(core)
	srv := core.NewGRPC(pki.NewServerCerts(ca, []string{"127.0.0.1"}), hub)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	kube := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: k8stypes.UID(rigUID)}})
	return &erig{t: t, core: core, hub: hub, st: st, ca: ca, addr: l.Addr().String(), kube: kube, gets: gets}
}

// safeBuf is a log destination that can be read while the agent writes to it.
type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newLog() (*slog.Logger, *safeBuf) {
	b := &safeBuf{}
	return slog.New(slog.NewTextHandler(b, nil)), b
}

var codeRe = regexp.MustCompile(`(?i)approval code:\s+([0-9A-Z]{4}-[0-9A-Z]{4})`)

// codesLogged lists, in order, the approval codes an agent has printed in its log.
func codesLogged(log *safeBuf) []string {
	var out []string
	for _, m := range codeRe.FindAllStringSubmatch(log.String(), -1) {
		out = append(out, m[1])
	}
	return out
}

func (r *erig) cfg(ids agent.IdentityStore, token string, log *slog.Logger) agent.Config {
	return agent.Config{Server: r.addr, CAPin: r.ca.Pin(), Token: token, Tier: 2, Kube: r.kube, Identity: ids, Version: "test", Debounce: 100 * time.Millisecond,
		Log: log, Floors: agent.Floors{Poll: 100 * time.Millisecond}, RevokedHold: -1}
}

func (r *erig) start(cfg agent.Config) (stop func(), done <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- agent.Run(ctx, cfg) }()
	r.t.Cleanup(cancel)
	return cancel, ch
}

func (r *erig) agents() []server.AgentDoc { d, _ := r.hub.State(context.Background()); return d.Agents }

func (r *erig) token(name string) string {
	tok, _, err := r.core.CreateToken(context.Background(), "test", name, 2)
	if err != nil {
		r.t.Fatal(err)
	}
	return tok
}

func (r *erig) auditText() string {
	rows, _ := r.st.ListAudit(context.Background(), "org", 500)
	var b strings.Builder
	for _, e := range rows {
		b.WriteString(e.Action + ": " + e.Detail + "\n")
	}
	return b.String()
}

func (r *erig) synced() bool {
	d, _ := r.hub.State(context.Background())
	return len(d.Topology.Clusters) == 1
}

func waitDone(t *testing.T, done <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: the agent did not stop", what)
		return nil
	}
}

// ---- approval codes end to end ----

func TestApprovalByCodeEndToEndWithAWrongCodeFirst(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("e2e"), log))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	agentID := r.agents()[0].ID

	if got := codesLogged(buf); len(got) == 0 || got[0] != code {
		t.Fatalf("the agent must print the code it saved in its own log: %v vs %s\n%s", got, code, buf.String())
	}
	if strings.Contains(r.auditText(), code) {
		t.Fatal("the code reached the server's audit trail")
	}
	a := r.agents()[0]
	if a.LegacyEnrollment || a.ApprovalAttemptsLeft == nil || *a.ApprovalAttemptsLeft != 5 {
		t.Fatalf("the server must know this agent has a code: %+v", a)
	}
	if err := r.core.Approve(context.Background(), "test", agentID, "AAAA-AAAA", 2); err == nil {
		t.Fatal("a wrong code approved")
	}
	if err := r.core.Approve(context.Background(), "test", agentID, code, 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, r.synced)
	if id, _ := ids.Load(context.Background()); id == nil || id.ApprovalCode != "" || len(id.CertDER) == 0 {
		t.Fatalf("once approved the code is forgotten and the certificate kept: %+v", id)
	}
}

func TestFiveWrongCodesEndTheAgentPermanently(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	_, done := r.start(r.cfg(ids, r.token("e2e"), log))
	codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	agentID := r.agents()[0].ID
	for i := 0; i < 5; i++ {
		_ = r.core.Approve(context.Background(), "test", agentID, "AAAA-AAA"+string(rune('A'+i)), 2)
	}
	err := waitDone(t, done, "after the lock")
	if !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("err = %v", err)
	}
	id, _ := ids.Load(context.Background())
	if id == nil || !id.Ended() || id.Key != nil || !strings.Contains(id.Revoked, "wrong approval codes") {
		t.Fatalf("marker: %+v", id)
	}
	if !strings.Contains(buf.String(), "helm upgrade") || !strings.Contains(buf.String(), "permanent") {
		t.Fatalf("the log must say plainly what to do:\n%s", buf.String())
	}
}

// ---- idempotent enrollment ----

func TestCrashAndRestartWhilePendingKeepsTheCodeAndTheAgent(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token := r.token("e2e")

	log1, buf1 := newLog()
	stop1, done1 := r.start(r.cfg(ids, token, log1))
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	code := codeOf(t, ids)
	agentID := r.agents()[0].ID
	stop1() // the pod dies
	waitDone(t, done1, "first run")

	log2, buf2 := newLog()
	r.start(r.cfg(ids, token, log2))
	waitFor(t, "second run pending", 10*time.Second, func() bool { return len(codesLogged(buf2)) > 0 })
	if got := codesLogged(buf2)[0]; got != code || codesLogged(buf1)[0] != code {
		t.Fatalf("the same code must be printed again after a restart: %s / %s / %s", codesLogged(buf1)[0], got, code)
	}
	// Still one agent, still pending, and the very same one.
	time.Sleep(500 * time.Millisecond)
	if a := r.agents(); len(a) != 1 || a[0].ID != agentID || a[0].Status != "pending" {
		t.Fatalf("agents after the restart: %+v", a)
	}
	if err := r.core.Approve(context.Background(), "test", agentID, code, 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync after the restart", 15*time.Second, r.synced)
}

// failOnceStore loses the identity the agent writes right after the server answered its enrollment: the
// response arrived, but the agent never kept it (a crash between Enroll and storing the answer).
type failOnceStore struct {
	agent.IdentityStore
	failed atomic.Bool
}

func (f *failOnceStore) Save(ctx context.Context, id *agent.Identity) error {
	if id.AgentID != "" && len(id.CertDER) == 0 && !f.failed.Swap(true) {
		return errors.New("simulated crash before the answer was stored")
	}
	return f.IdentityStore.Save(ctx, id)
}

func TestLostEnrollResponseDoesNotBurnTheTokenOrDuplicateTheAgent(t *testing.T) {
	r := newERig(t, nil)
	ids := &failOnceStore{IdentityStore: agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}}
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("e2e"), log))
	waitFor(t, "the agent to enroll again after losing the answer", 20*time.Second, func() bool {
		id, _ := ids.Load(context.Background())
		return id != nil && id.AgentID != ""
	})
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	if !ids.failed.Load() {
		t.Fatal("the simulated crash never happened")
	}
	a := r.agents()
	if len(a) != 1 {
		t.Fatalf("a retried enrollment must not create a second agent: %+v", a)
	}
	id, _ := ids.Load(context.Background())
	if id.AgentID != a[0].ID {
		t.Fatalf("the agent and the server disagree about who the agent is: %s vs %s", id.AgentID, a[0].ID)
	}
	if got := codesLogged(buf); len(got) < 2 || got[0] != code || got[len(got)-1] != code {
		t.Fatalf("the same code must be printed each time: %v", got)
	}
	if !strings.Contains(r.auditText(), "agent-enrollment-retried") {
		t.Fatalf("the retry must be audited:\n%s", r.auditText())
	}
	if err := r.core.Approve(context.Background(), "test", a[0].ID, code, 2); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first sync", 15*time.Second, r.synced)
}

func TestSpentTokenWithAnotherKeyStopsTheSecondAgent(t *testing.T) {
	r := newERig(t, nil)
	token := r.token("e2e")
	idsA := agent.FileStore{Dir: filepath.Join(t.TempDir(), "a")}
	r.start(r.cfg(idsA, token, nil))
	waitFor(t, "first agent pending", 10*time.Second, func() bool { return len(r.agents()) == 1 })

	idsB := agent.FileStore{Dir: filepath.Join(t.TempDir(), "b")}
	log, buf := newLog()
	_, done := r.start(r.cfg(idsB, token, log))
	if err := waitDone(t, done, "second agent"); !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(buf.String(), "invalid, expired or already used") || len(r.agents()) != 1 {
		t.Fatalf("agents=%d log:\n%s", len(r.agents()), buf.String())
	}
}

// ---- token bound to a cluster ----

func TestABoundTokenIsRefusedByTheWrongClusterAndTheAgentStops(t *testing.T) {
	r := newERig(t, nil)
	token, _, err := r.core.CreateTokenFor(context.Background(), "test", "prod", 2, "11111111-2222-4333-8444-555566667777")
	if err != nil {
		t.Fatal(err)
	}
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	_, done := r.start(r.cfg(ids, token, log))
	if err := waitDone(t, done, "wrong cluster"); !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(buf.String(), "different cluster") || len(r.agents()) != 0 {
		t.Fatalf("agents=%d log:\n%s", len(r.agents()), buf.String())
	}
	if !strings.Contains(r.auditText(), "enrollment-refused") {
		t.Fatalf("audit:\n%s", r.auditText())
	}
	// The token was not spent: a cluster it is bound to can still use it.
	right, _, _ := r.core.CreateTokenFor(context.Background(), "test", "prod", 2, rigUID)
	r.start(r.cfg(agent.FileStore{Dir: filepath.Join(t.TempDir(), "ok")}, right, nil))
	waitFor(t, "the right cluster enrolls", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
}

// ---- pending lifetime and polling ----

func TestAnExpiredRequestIsReEnrolledWithANewCode(t *testing.T) {
	r := newERig(t, func(c *server.Core) { c.PendingTTL, c.PollAfter = 2*time.Second, time.Second })
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("e2e"), log))
	first := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	agentID := r.agents()[0].ID
	waitFor(t, "a new code after the request expired", 30*time.Second, func() bool { return len(codesLogged(buf)) >= 2 && codesLogged(buf)[len(codesLogged(buf))-1] != first })
	newCode := codesLogged(buf)[len(codesLogged(buf))-1]
	if !strings.Contains(buf.String(), "enrolling again with a new approval code") {
		t.Fatalf("log:\n%s", buf.String())
	}
	waitFor(t, "the reopened enrollment", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].ID == agentID && a[0].Status == "pending" })
	evs, _ := r.st.ListEvents(context.Background(), "org", store.EventQuery{})
	sawEvent := false
	for _, e := range evs {
		sawEvent = sawEvent || e.Kind == "enrollment-expired"
	}
	if !sawEvent || !strings.Contains(r.auditText(), "agent-enrollment-expired") || !strings.Contains(r.auditText(), "agent-enrollment-reopened") {
		t.Fatalf("expiry and reopening must be recorded (event %v):\n%s", sawEvent, r.auditText())
	}
	if err := r.core.Approve(context.Background(), "test", agentID, first, 2); err == nil {
		t.Fatal("the code of the expired request still works")
	}
	// Approve before the new request runs out too.
	waitFor(t, "approval with the new code", 10*time.Second, func() bool {
		return r.core.Approve(context.Background(), "test", agentID, newCode, 2) == nil
	})
	waitFor(t, "first sync", 15*time.Second, r.synced)
}

func TestPollingFollowsTheServersPaceButNotBelowTheFloor(t *testing.T) {
	// The server asks for a poll every second; with the test floor lowered the agent follows that. If it
	// ignored the server it would poll every 100 ms (the floor) and count about 40 in four seconds.
	r := newERig(t, func(c *server.Core) { c.PollAfter = time.Second })
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	r.start(r.cfg(ids, r.token("e2e"), nil))
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	time.Sleep(300 * time.Millisecond) // let the first responses land; the pace is the server's from now on
	before := r.gets.Load()
	time.Sleep(4 * time.Second)
	if n := r.gets.Load() - before; n < 2 || n > 7 {
		t.Fatalf("polled %d times in 4 s; the server asked for one a second (jitter allows 3-5)", n)
	}
}

func TestPollingWithoutATestFloorNeverGoesBelowTwoSeconds(t *testing.T) {
	r := newERig(t, func(c *server.Core) { c.PollAfter = time.Second }) // the server asks for 1 s: too fast
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	cfg := r.cfg(ids, r.token("e2e"), nil)
	cfg.Floors = agent.Floors{} // the real minimum
	r.start(cfg)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 && a[0].Status == "pending" })
	time.Sleep(500 * time.Millisecond)
	before := r.gets.Load()
	time.Sleep(6 * time.Second)
	if n := r.gets.Load() - before; n > 4 {
		t.Fatalf("polled %d times in 6 s; the floor is one every 2 s (at most 4 with jitter)", n)
	}
}

// ---- revocation ----

func TestARevokedRunningAgentReturnsErrRevokedAndKeepsOnlyAMarker(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	_, done := r.start(r.cfg(ids, r.token("e2e"), log))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 })
	id0 := r.agents()[0].ID
	_ = r.core.Approve(context.Background(), "test", id0, code, 2)
	waitFor(t, "first sync", 15*time.Second, r.synced)
	if err := r.core.Revoke(context.Background(), "test", id0, "cluster decommissioned"); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, done, "revocation"); !errors.Is(err, agent.ErrRevoked) || !strings.Contains(err.Error(), "cluster decommissioned") {
		t.Fatalf("err = %v", err)
	}
	if strings.Count(buf.String(), "will not connect again") != 1 || !strings.Contains(buf.String(), "helm upgrade") {
		t.Fatalf("one plain message, with what to do:\n%s", buf.String())
	}
	if id, _ := ids.Load(context.Background()); id == nil || !id.Ended() || id.Key != nil || len(id.CertDER) != 0 || id.PollSecret != "" {
		t.Fatalf("marker: %+v", id)
	}
}

func TestAgentRevokedWhileOfflineLearnsItOnReconnect(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	token := r.token("e2e")
	stop, done := r.start(r.cfg(ids, token, nil))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { a := r.agents(); return len(a) == 1 })
	id0 := r.agents()[0].ID
	_ = r.core.Approve(context.Background(), "test", id0, code, 2)
	waitFor(t, "first sync", 15*time.Second, r.synced)
	stop()
	waitDone(t, done, "stop")
	if err := r.core.Revoke(context.Background(), "test", id0, "lost the laptop"); err != nil {
		t.Fatal(err)
	}
	log, buf := newLog()
	_, done2 := r.start(r.cfg(ids, token, log))
	err := waitDone(t, done2, "restart after revocation")
	if !errors.Is(err, agent.ErrRevoked) || !strings.Contains(err.Error(), "lost the laptop") {
		t.Fatalf("err = %v\n%s", err, buf.String())
	}
}

func TestRestartedRevokedAgentWaitsAndNeverContactsTheServer(t *testing.T) {
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	if err := ids.Save(context.Background(), &agent.Identity{AgentID: "ag-1", TokenID: "x", Revoked: "revoked by an administrator"}); err != nil {
		t.Fatal(err)
	}
	log, buf := newLog()
	// The server address is nothing that listens: any contact attempt would show up as an error.
	cfg := agent.Config{Server: "127.0.0.1:1", CAPin: "00", Kube: fake.NewSimpleClientset(), Identity: ids, Log: log, RevokedHold: 400 * time.Millisecond}
	started := time.Now()
	err := agent.Run(context.Background(), cfg)
	if !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("err = %v", err)
	}
	if took := time.Since(started); took < 400*time.Millisecond {
		t.Fatalf("it exited after %v; it must hold first so that restarts are spread out", took)
	}
	out := buf.String()
	if strings.Contains(out, "disconnected") || strings.Contains(out, "could not") || !strings.Contains(out, "will not connect again") || !strings.Contains(out, "helm upgrade") {
		t.Fatalf("log:\n%s", out)
	}
	// Ending while it waits (the pod is being deleted) is not an error to report.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	cfg.RevokedHold = time.Hour
	if err := agent.Run(ctx, cfg); errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("a hold cut short by shutdown returned %v", err)
	}
}

func TestANewTokenAfterRevocationStartsOver(t *testing.T) {
	r := newERig(t, nil)
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	old := r.token("old")
	stop, done := r.start(r.cfg(ids, old, nil))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(r.agents()) == 1 })
	id0 := r.agents()[0].ID
	_ = r.core.Approve(context.Background(), "test", id0, code, 2)
	waitFor(t, "first sync", 15*time.Second, r.synced)
	_ = r.core.Revoke(context.Background(), "test", id0, "rotate")
	waitDone(t, done, "revocation")
	stop()

	// Same token: still down, and no new agent appears on the server.
	if err := agent.Run(context.Background(), r.cfg(ids, old, nil)); !errors.Is(err, agent.ErrRevoked) {
		t.Fatalf("with the old token: %v", err)
	}
	if len(r.agents()) != 1 {
		t.Fatalf("agents: %+v", r.agents())
	}
	// A new token (helm upgrade): the agent enrolls again, with a fresh key and a new code.
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("new"), log))
	waitFor(t, "the second enrollment", 10*time.Second, func() bool { return len(r.agents()) == 2 })
	if len(codesLogged(buf)) == 0 || codesLogged(buf)[0] == code {
		t.Fatalf("a new code is expected: %v (old %s)", codesLogged(buf), code)
	}
	id1 := r.agents()[1].ID
	waitFor(t, "approval of the new enrollment", 10*time.Second, func() bool {
		return r.core.Approve(context.Background(), "test", id1, codesLogged(buf)[0], 2) == nil
	})
	waitFor(t, "sync of the new agent", 15*time.Second, r.synced)
}

func TestTheCommandExitsWithCode3WhenRevoked(t *testing.T) {
	dir := t.TempDir()
	ids := agent.FileStore{Dir: filepath.Join(dir, "state")}
	if err := ids.Save(context.Background(), &agent.Identity{Revoked: "revoked by an administrator", TokenID: "x"}); err != nil {
		t.Fatal(err)
	}
	kc := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kc, []byte(`apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "https://127.0.0.1:1"}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
users: [{name: u, user: {token: t}}]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTINUUM_TOKEN", "")
	code := agentcli.Main([]string{"--server", "127.0.0.1:1", "--ca-pin", "00", "--kubeconfig", kc, "--state-dir", filepath.Join(dir, "state"), "--revoked-hold", "-1ns"})
	if code != agent.ExitRevoked || agent.ExitRevoked != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// ---- clock skew ----

func TestAClockThatIsOffIsWarnedAboutAndReported(t *testing.T) {
	// The server's clock is ten minutes behind the agent's.
	r := newERig(t, func(c *server.Core) { c.Now = func() time.Time { return time.Now().Add(-10 * time.Minute) } })
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("e2e"), log))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(r.agents()) == 1 })
	_ = r.core.Approve(context.Background(), "test", r.agents()[0].ID, code, 2)
	waitFor(t, "first sync", 15*time.Second, r.synced)
	waitFor(t, "the skew on the agent record", 15*time.Second, func() bool {
		a := r.agents()
		return len(a) == 1 && a[0].ClockSkewMs > 590_000 && a[0].ClockSkewMs < 620_000
	})
	if got := strings.Count(buf.String(), "clock is 10m"); got != 1 || !strings.Contains(buf.String(), "ahead of the Continuum server") {
		t.Fatalf("exactly one warning is expected (got %d):\n%s", got, buf.String())
	}
}

func TestASmallClockDifferenceIsNotWarnedAbout(t *testing.T) {
	r := newERig(t, func(c *server.Core) { c.Now = func() time.Time { return time.Now().Add(30 * time.Second) } })
	ids := agent.FileStore{Dir: filepath.Join(t.TempDir(), "id")}
	log, buf := newLog()
	r.start(r.cfg(ids, r.token("e2e"), log))
	code := codeOf(t, ids)
	waitFor(t, "pending agent", 10*time.Second, func() bool { return len(r.agents()) == 1 })
	_ = r.core.Approve(context.Background(), "test", r.agents()[0].ID, code, 2)
	waitFor(t, "first sync", 15*time.Second, r.synced)
	waitFor(t, "the skew on the agent record", 15*time.Second, func() bool {
		a := r.agents()
		return len(a) == 1 && a[0].ClockSkewMs < -25_000 && a[0].ClockSkewMs > -40_000
	})
	if strings.Contains(buf.String(), "clock is") {
		t.Fatalf("a 30 s difference is not worth a warning:\n%s", buf.String())
	}
}

// ---- a legacy agent (no approval code) still enrolls and is approved by fingerprint ----

func TestLegacyAgentStillEnrollsAndPollsForItsCertificate(t *testing.T) {
	r := newERig(t, nil)
	// An older agent's enrollment: no approval hash. Made directly, since the agent in this tree always sends one.
	token := r.token("legacy")
	d, _ := (func() ([]byte, error) { k, _ := agent.NewKey(); return agent.NewCSR(k) })()
	resp, err := r.core.Enroll(context.Background(), "10.0.0.1", &continuumv1.EnrollRequest{Token: token, CsrDer: d, ClusterFingerprint: rigUID, InstalledAccessTier: 2})
	if err != nil {
		t.Fatal(err)
	}
	a := r.agents()
	if len(a) != 1 || !a[0].LegacyEnrollment {
		t.Fatalf("%+v", a)
	}
	if err := r.core.Approve(context.Background(), "test", resp.AgentId, rigUID[:8], 2); err != nil {
		t.Fatal(err)
	}
}
