package estate

import (
	"context"
	"continuum/internal/testkit"
	"crypto/ecdsa"
	"crypto/tls"
	"flag"
	"log/slog"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
	"continuum/internal/kernel"
	"continuum/internal/observed"
	"continuum/internal/pki"
	"continuum/internal/store"

	"google.golang.org/grpc"
)

// These are the shell's fixtures (a server with one organisation, enrolled agents, a gRPC listener, a scripted agent) in the form the
// Hub's own tests need: the same code, but over a Service instead of the shell's Core, and a platform of one organisation. They are a copy.
// A fixture package both can import takes the place of this file.

// ---- from applications_graph_test.go ----

// ---- from capabilities_test.go ----

// connectedWith is a fake agent whose hello is h.
func (r *hubRig) connectedWith(t *testing.T, h *continuumv1.Hello) *fakeAgent {
	t.Helper()
	f := r.newFakeAgentWith(t, fakeAgentCtx(t), h)
	return f
}

// agentDoc is the document the API serves for one agent, as the hub builds it.
func (r *hubRig) agentDoc(t *testing.T, id string) AgentDoc {
	t.Helper()
	s, err := r.hub.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range s.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %s is not in the state", id)
	return AgentDoc{}
}

// ---- from config_matrix_test.go ----

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

// ---- from consent_test.go ----

func (r *hubRig) connect(t *testing.T, ctx context.Context, key any, leaf []byte, hello *continuumv1.Hello) continuumv1.AgentService_ConnectClient {
	t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	s, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: hello}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	return s
}

// ---- from core_test.go ----

type env struct {
	w    *testkit.World
	base *Service // the platform-wide core: accounts, sign-in, the gRPC listener
	core *Service // organisation "org-1", which most tests work in
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

// ---- from enroll_test.go ----

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

// ---- from fake_agent_test.go ----

// fakeAgent is the scripted agent of testkit with the server's side of it: what the sync hub in front of the test's gRPC listener has
// seen of it.
type fakeAgent struct {
	*testkit.FakeAgent
	r *hubRig
}

// newFakeAgent makes an approved agent and connects it: the hello is sent and the first Config, which the server sends at once, is
// received before it returns.
func (r *hubRig) newFakeAgent(t *testing.T, ctx context.Context) *fakeAgent {
	t.Helper()
	return r.newFakeAgentWith(t, ctx, &continuumv1.Hello{AgentVersion: "fake"})
}

// newFakeAgentWith is newFakeAgent with a hello of the test's choosing.
func (r *hubRig) newFakeAgentWith(t *testing.T, ctx context.Context, hello *continuumv1.Hello) *fakeAgent {
	t.Helper()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	f := &fakeAgent{r: r, FakeAgent: &testkit.FakeAgent{T: t, Ctx: ctx, ID: id, Key: key, Leaf: leaf, Hello: hello,
		Dial: func(c *tls.Certificate) *grpc.ClientConn {
			return r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", c))
		}}}
	f.Connect()
	return f
}

// drop ends the stream the way a crash or a cut network does: nothing is said, the connection just stops.
func (f *fakeAgent) drop() {
	f.T.Helper()
	f.Cancel()
	f.WaitUntil("the server to notice the stream ended", func() bool {
		f.r.hub.Mu.Lock()
		defer f.r.hub.Mu.Unlock()
		return f.r.hub.Sessions[f.ID] == nil
	})
}

// restart is a drop and a new stream with the same identity, which is what a restarted agent pod does.
func (f *fakeAgent) restart() *continuumv1.Config {
	f.T.Helper()
	f.drop()
	return f.Connect()
}

// inForce is the digest the server believes is in force, and since when (zero if it is not).
func (f *fakeAgent) inForce() (sent, applied string, since time.Time) {
	f.r.hub.Mu.Lock()
	defer f.r.hub.Mu.Unlock()
	v := f.r.hub.Views[f.ID]
	return v.CfgHash, v.CfgApplied, v.CfgInForceAt
}

// changeTier is an administrator's change that makes the next Config differ, and the push that follows it.
func (f *fakeAgent) changeTier() *continuumv1.Config {
	f.T.Helper()
	cur, err := f.r.st.GetAgent(f.Ctx, f.ID)
	if err != nil {
		f.T.Fatal(err)
	}
	if err := f.r.st.SetAccessTier(f.Ctx, f.ID, (cur.AccessTier+1)%3); err != nil {
		f.T.Fatal(err)
	}
	f.r.hub.PushOne(f.ID)
	c := f.NextConfig(5 * time.Second)
	if c == nil {
		f.T.Fatal("no Config was pushed after the change")
	}
	return c
}

// ---- from grpc_test.go ----

type rig struct {
	*env
	addr string
}

func (r *rig) dial(t *testing.T, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	return testkit.Dial(t, r.addr, cfg)
}

// ---- from history_test.go ----

func liveView(now time.Time, nodes ...string) *View {
	st := facts.New()
	st.Cluster = &continuumv1.ClusterFacts{Uid: testkit.FP}
	for _, n := range nodes {
		st.Nodes[n] = &continuumv1.NodeFacts{Key: n, Name: n, Ready: true}
	}
	return &View{EstView: EstView{State: st, LastSync: now, LastBeat: now, Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
}

// ---- from limits_test.go ----

// agentStream connects an approved agent and returns its stream, past the hello.
func (r *hubRig) agentStream(t *testing.T, ctx context.Context, id string, key any, leaf []byte) continuumv1.AgentService_ConnectClient {
	t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
	s, err := c.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: &continuumv1.Hello{AgentVersion: "t"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil { // config
		t.Fatal(err)
	}
	return s
}

// ---- from linkcreds_test.go ----

// credRig is a hub with three approved clusters: first and second are the pair (first runs the tests, second serves them), third
// is in no pair.
type credRig struct {
	*hubRig
	first, second, third store.Agent
	log                  *testkit.LockedBuf
}

func newCredRig(t *testing.T) *credRig {
	t.Helper()
	r := newHubRig(t)
	cr := &credRig{hubRig: r, log: &testkit.LockedBuf{}}
	r.core.Log = slog.New(slog.NewTextHandler(cr.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var ags []store.Agent
	for _, f := range []string{testkit.FP, testkit.FP2, testkit.FP3} {
		id, _, _ := r.approvedAgent(t, f)
		a, err := r.st.GetAgent(r.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		r.hub.Mu.Lock()
		r.hub.Views[a.ID] = &View{EstView: EstView{State: testkit.NodeState("10.0." + strings.TrimPrefix(f[len(f)-2:], "0") + ".1"), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
		r.hub.Mu.Unlock()
		ags = append(ags, a)
	}
	// Order the first two by cluster id: the first sorted is the runner.
	if ags[1].ClusterID < ags[0].ClusterID {
		ags[0], ags[1] = ags[1], ags[0]
	}
	cr.first, cr.second, cr.third = ags[0], ags[1], ags[2]
	return cr
}

func (cr *credRig) save(t *testing.T, links ...kernel.LinkSetting) {
	t.Helper()
	if _, err := cr.core.SaveSettings(cr.ctx, "alex", kernel.Settings{Links: links}); err != nil {
		t.Fatal(err)
	}
}

func (cr *credRig) cfg(a store.Agent) *continuumv1.Config {
	cr.hub.Mu.Lock()
	v := cr.hub.Views[a.ID]
	cr.hub.Mu.Unlock()
	return cr.hub.ConfigFor(a, v)
}

func (cr *credRig) pair(throughput bool) kernel.LinkSetting {
	l := kernel.LinkSetting{A: cr.first.ClusterID, B: cr.second.ClusterID}
	if throughput {
		l.Throughput = &kernel.LinkThroughput{}
	}
	return l
}

// settle waits for the credentials work a saved settings change starts in the background.
func (cr *credRig) settle() {
	cr.hub.SyncLinkCredentials()
}

// ---- from links_setup_test.go ----

// ---- from links_test.go ----

func linkHub(views map[string]*View) *Hub { return &Hub{Views: views} }

// ---- from observed_fixtures_test.go ----

// ---- from operators_test.go ----

// ---- from paths_links_test.go ----

// ---- from proposals_test.go ----

// ---- from routes_test.go ----

// ---- from security_test.go ----

// approvedAgent enrolls and approves an agent, returning its id, private key and first certificate.
func (e *env) approvedAgent(t *testing.T, fingerprint string) (string, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	return testkit.ApprovedAgent(t, e.ctx, e.core.Agents, fingerprint)
}

type hubRig struct {
	*rig
	hub  *Hub
	plat *testPlatform
}

// newHubRig is newRig with the real sync hub behind the gRPC server.
func newHubRig(t *testing.T) *hubRig {
	t.Helper()
	e := newEnv(t)
	p := newTestPlatform(e.base)
	tn, err := p.Tenant(e.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	hub := tn.Hub
	e.core = tn.C // the hub reads its settings from this core
	addr := testkit.Serve(t, e.base.Agents, pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	return &hubRig{rig: &rig{env: e, addr: addr}, hub: hub, plat: p}
}

// ---- from tenancy_test.go ----

// ---- from twin_test.go ----

// ---- from applications_graph_test.go ----

// ---- from grpc_test.go ----

// dialProxied is r.dial, but first writes a v1 PROXY protocol line declaring proxiedIP on the raw TCP
// connection, the way a load balancer configured to send it would - before grpc-go layers TLS on top of
// the same connection.
func (r *rig) dialProxied(t *testing.T, cfg *tls.Config, proxiedIP string) *grpc.ClientConn {
	t.Helper()
	return testkit.DialProxied(t, r.addr, cfg, proxiedIP)
}

// ---- from history_test.go ----

// ---- from linkcreds_test.go ----

func (cr *credRig) rows(t *testing.T) []store.LinkCredential {
	t.Helper()
	rows, err := cr.st.ListLinkCredentials(cr.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// ---- from paths_links_test.go ----

// ---- from fake_agent_test.go ----

func fakeAgentCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
