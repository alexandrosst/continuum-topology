package estate

import (
	"bytes"
	"context"
	"continuum/internal/accounts"
	"continuum/internal/approval"
	"continuum/internal/graph"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/connectivity"
	"continuum/internal/facts"
	"continuum/internal/kernel"
	"continuum/internal/observed"
	"continuum/internal/pki"
	"continuum/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// These are the shell's fixtures (a server with one organisation, enrolled agents, a gRPC listener, a scripted agent) in the form the
// Hub's own tests need: the same code, but over a Service instead of the shell's Core, and a platform of one organisation. They are a copy.
// A fixture package both can import takes the place of this file.

// ---- from applications_graph_test.go ----

// linkSpy is a store that keeps what the graph would be asked to link.
type linkSpy struct {
	store.Store
	sets map[string][]string
}

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

const fp = "8f3c2a9e-1111-4222-8333-944455556666"

type env struct {
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
	dbPath := filepath.Join(t.TempDir(), "t.db")
	st, err := store.OpenSQLite(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	base := newCore(st, ca, "", nil)
	base.DefaultOrg = "org-1"
	now := time.Now()
	e := &env{base: base, st: st, now: &now, ctx: context.Background(), dbPath: dbPath, codes: map[string]string{}}
	base.Now = func() time.Time { return *e.now }
	base.EnrollRL = kernel.NewLimiter(6000, 1000) // out of the way unless a test wants it
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

// approvalFor makes an approval code for the key in csrDER and the hash the agent sends for it.
func approvalFor(t *testing.T, csrDER []byte) (hash []byte, code string) {
	t.Helper()
	code, err := approval.New()
	if err != nil {
		t.Fatal(err)
	}
	if hash, err = approval.Hash(code, csrDER); err != nil {
		t.Fatal(err)
	}
	return hash, code
}

func kindOf(err error) kernel.Kind {
	var e *kernel.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// ---- from enroll_test.go ----

// coded is one enrollment the way a current agent makes it: a key, a code, and only the code's hash sent.
type coded struct {
	resp  *continuumv1.EnrollResponse
	req   *continuumv1.EnrollRequest
	code  string
	token string
}

func (e *env) enrollCoded(t *testing.T, fingerprint string) coded {
	t.Helper()
	secret, _, err := e.core.Agents.CreateToken(e.ctx, "admin", "edge-patras", 2)
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
	resp, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", req)
	return coded{resp: resp, req: req, code: code, token: token}, err
}

// ---- from fake_agent_test.go ----

// fakeAgent is a scripted agent on a real mutual-TLS stream to a real server. The real agent (internal/agent) is the thing the
// end-to-end tests run, and it cannot be told to misbehave: to be late, to say the wrong thing, to vanish, to come back. This
// one does only what the test tells it, so a rule of the protocol can be checked in the exact situation that breaks it. It keeps
// what it was sent, answers nothing by itself (no heartbeat, no echo), and the test says each step.
//
// Faults it can make, each one a method: drop (the stream ends with no goodbye), restart (a new stream from the same identity),
// beat with any digest (an echo that is late, from an older Config, or from one the server never sent), say nothing at all (the
// default), and send what it likes after being replaced.
type fakeAgent struct {
	t     *testing.T
	r     *hubRig
	ctx   context.Context
	id    string
	key   any
	leaf  []byte
	hello *continuumv1.Hello

	cancel   context.CancelFunc // ends the current stream
	stream   continuumv1.AgentService_ConnectClient
	incoming chan recvdMsg
	configs  []*continuumv1.Config // every Config received on any stream, in order
}

type recvdMsg struct {
	m   *continuumv1.ServerMessage
	err error
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
	id, key, leaf := r.approvedAgent(t, fp)
	f := &fakeAgent{t: t, r: r, ctx: ctx, id: id, key: key, leaf: leaf, hello: hello}
	f.connect()
	return f
}

// connect opens a stream, says hello and waits for the Config the server sends in answer.
func (f *fakeAgent) connect() *continuumv1.Config {
	f.t.Helper()
	cert := &tls.Certificate{Certificate: [][]byte{f.leaf}, PrivateKey: f.key}
	c := continuumv1.NewAgentServiceClient(f.r.dial(f.t, pki.ClientTLS(f.r.core.CA.Pin(), "127.0.0.1", cert)))
	sctx, cancel := context.WithCancel(f.ctx)
	s, err := c.Connect(sctx)
	if err != nil {
		cancel()
		f.t.Fatalf("connect: %v", err)
	}
	f.cancel, f.stream = cancel, s
	f.t.Cleanup(cancel)
	in := make(chan recvdMsg, 64)
	f.incoming = in
	go func() {
		for {
			m, err := s.Recv()
			in <- recvdMsg{m, err}
			if err != nil {
				return
			}
		}
	}()
	if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: f.hello}}); err != nil {
		f.t.Fatalf("hello: %v", err)
	}
	cfg := f.nextConfig(5 * time.Second)
	if cfg == nil {
		f.t.Fatal("the server sent no Config after hello")
	}
	return cfg
}

// nextConfig waits up to d for the next Config on the current stream and returns nil if none comes. Other messages are skipped.
func (f *fakeAgent) nextConfig(d time.Duration) *continuumv1.Config {
	f.t.Helper()
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case in := <-f.incoming:
			if in.err != nil {
				return nil
			}
			if c := in.m.GetConfig(); c != nil {
				f.configs = append(f.configs, c)
				return c
			}
		case <-timer.C:
			return nil
		}
	}
}

// beat sends a heartbeat that names digest as the Config in force ("" says nothing, as an older agent does).
func (f *fakeAgent) beat(digest string) {
	f.t.Helper()
	m := &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{AppliedConfigDigest: digest}}}
	if err := f.stream.Send(m); err != nil {
		f.t.Fatalf("heartbeat: %v", err)
	}
}

// inForce is the digest the server believes is in force, and since when (zero if it is not).
func (f *fakeAgent) inForce() (sent, applied string, since time.Time) {
	f.r.hub.Mu.Lock()
	defer f.r.hub.Mu.Unlock()
	v := f.r.hub.Views[f.id]
	return v.CfgHash, v.CfgApplied, v.CfgInForceAt
}

// changeTier is an administrator's change that makes the next Config differ, and the push that follows it.
func (f *fakeAgent) changeTier() *continuumv1.Config {
	f.t.Helper()
	cur, err := f.r.st.GetAgent(f.ctx, f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.r.st.SetAccessTier(f.ctx, f.id, (cur.AccessTier+1)%3); err != nil {
		f.t.Fatal(err)
	}
	f.r.hub.PushOne(f.id)
	c := f.nextConfig(5 * time.Second)
	if c == nil {
		f.t.Fatal("no Config was pushed after the change")
	}
	return c
}

func fakeAgentCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ---- from grpc_test.go ----

type rig struct {
	*env
	addr string
}

func (r *rig) dial(t *testing.T, cfg *tls.Config) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(r.addr, grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

func code(err error) codes.Code { return status.Code(err) }

// ---- from history_test.go ----

func extEdge(ft *observed.FlowTable, src, dstIP string, port uint32, proto string, conns uint64, noise string) {
	f := flowOf(wep(src), xep(dstIP), port, conns)
	f.Protocol = proto
	f.Noise = noise
	ft.Apply(&continuumv1.FlowBatch{WindowSeconds: 60, Flows: []*continuumv1.Flow{f}}, time.Now())
}

func liveView(now time.Time, nodes ...string) *View {
	st := facts.New()
	st.Cluster = &continuumv1.ClusterFacts{Uid: fp}
	for _, n := range nodes {
		st.Nodes[n] = &continuumv1.NodeFacts{Key: n, Name: n, Ready: true}
	}
	return &View{EstView: EstView{State: st, LastSync: now, LastBeat: now, Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
}

// failHistory fails the event and snapshot writes while its flags are set.
type failHistory struct {
	store.Store
	events, snapshots atomic.Bool
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

func syncMsg(s *continuumv1.Sync) *continuumv1.AgentMessage {
	return &continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Sync{Sync: s}}
}

// ---- from linkcreds_test.go ----

const fp3 = "8f3c2a9e-3333-4222-8333-944455556688"

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// credRig is a hub with three approved clusters: first and second are the pair (first runs the tests, second serves them), third
// is in no pair.
type credRig struct {
	*hubRig
	first, second, third store.Agent
	log                  *lockedBuf
}

func newCredRig(t *testing.T) *credRig {
	t.Helper()
	r := newHubRig(t)
	cr := &credRig{hubRig: r, log: &lockedBuf{}}
	r.core.Log = slog.New(slog.NewTextHandler(cr.log, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var ags []store.Agent
	for _, f := range []string{fp, fp2, fp3} {
		id, _, _ := r.approvedAgent(t, f)
		a, err := r.st.GetAgent(r.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		r.hub.Mu.Lock()
		r.hub.Views[a.ID] = &View{EstView: EstView{State: nodeState("10.0." + strings.TrimPrefix(f[len(f)-2:], "0") + ".1"), Flows: observed.NewFlowTable()}, NetView: connectivity.NetView{Paths: map[string]*connectivity.PathTrack{}, Targets: map[string]connectivity.IssuedTarget{}}}
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

var setupNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// caps is a fresh report: Cluster links and probing on, telemetry sent onward, the prober seen a minute ago.
func caps(f ...func(*agents.LinkCaps)) agents.LinkCaps {
	c := agents.LinkCaps{Reported: setupNow.Add(-30 * time.Second), LinkMetrics: true, ProbeEnabled: true, TelemetryExport: true, ProberSeen: setupNow.Add(-time.Minute)}
	for _, g := range f {
		g(&c)
	}
	return c
}

func without(what string) func(*agents.LinkCaps) {
	return func(c *agents.LinkCaps) {
		switch what {
		case "links":
			c.LinkMetrics = false
		case "probe":
			c.ProbeEnabled = false
		case "export":
			c.TelemetryExport = false
		case "prober":
			c.ProberSeen = time.Time{}
		}
	}
}

// states writes the steps as "code/cluster=state", in order.
func states(steps []connectivity.LinkSetupStep) string {
	var out []string
	for _, s := range steps {
		k := s.Code
		if s.Cluster != "" {
			k += "/" + s.Cluster
		}
		out = append(out, k+"="+s.State)
	}
	return strings.Join(out, " ")
}

func step(t *testing.T, steps []connectivity.LinkSetupStep, code, cluster string) connectivity.LinkSetupStep {
	t.Helper()
	for _, s := range steps {
		if s.Code == code && s.Cluster == cluster {
			return s
		}
	}
	t.Fatalf("no %s step for %q in %s", code, cluster, states(steps))
	return connectivity.LinkSetupStep{}
}

// ---- from links_test.go ----

// nodeState is one node with the given internal address and, if any, external ones.
func nodeState(internal string, external ...string) *facts.State {
	n := &continuumv1.NodeFacts{InternalIps: []string{internal}}
	for _, e := range external {
		if e != "" {
			n.ExternalIps = append(n.ExternalIps, e)
		}
	}
	return &facts.State{Nodes: map[string]*continuumv1.NodeFacts{"n1": n}}
}

func linkHub(views map[string]*View) *Hub { return &Hub{Views: views} }

func ag(id, cluster, name, ip string, tier int) store.Agent {
	return store.Agent{ID: id, ClusterID: cluster, Name: name, Status: store.StatusApproved, AccessTier: tier, ConnectingIP: ip}
}

// ---- from observed_fixtures_test.go ----

func wk(ns, kind, name string, reach ...*continuumv1.Address) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: ns + "/" + kind + "/" + name, Namespace: ns, Kind: kind, Name: name, Reachable: reach}
}

func wep(key string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: key}
}

func xep(ip string) *continuumv1.FlowEndpoint {
	return &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: ip}
}

func flowOf(src, dst *continuumv1.FlowEndpoint, port uint32, conns uint64) *continuumv1.Flow {
	return &continuumv1.Flow{Src: src, Dst: dst, Port: port, Protocol: "tcp", Connections: conns, BytesOut: 1000 * conns, BytesIn: 3000 * conns, Method: "ebpf", BytesKnown: true}
}

// ---- from operators_test.go ----

const fp2 = "8f3c2a9e-2222-4222-8333-944455556677"

// ---- from paths_links_test.go ----

// agentsDown is a store whose list of the organisation's agents cannot be read (a busy database).
type agentsDown struct{ store.Store }

// ---- from proposals_test.go ----

func asRole(role string) context.Context {
	return accounts.WithCaller(context.Background(), accounts.Principal{Role: role})
}

// ---- from routes_test.go ----

// lineDiff lists the lines found in only one of the two texts, "-" for the golden file and "+" for what the source now says.
func lineDiff(want, got string) string {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
			m[l]++
		}
		return m
	}
	w, g := count(want), count(got)
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(want, "\n"), "\n") {
		if g[l] < w[l] {
			b.WriteString("- " + l + "\n")
			w[l]--
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if w[l] < g[l] {
			b.WriteString("+ " + l + "\n")
			g[l]--
		}
	}
	return b.String()
}

// ---- from security_test.go ----

// approvedAgent enrolls and approves an agent, returning its id, private key and first certificate.
func (e *env) approvedAgent(t *testing.T, fingerprint string) (string, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	secret, _, err := e.core.Agents.CreateToken(e.ctx, "admin", "edge", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, key := csr(t)
	hash, code := approvalFor(t, d)
	resp, err := e.core.Agents.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fingerprint, InstalledAccessTier: 2, ApprovalCodeHash: hash})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.Agents.Approve(e.ctx, "alex", resp.AgentId, code, 2); err != nil {
		t.Fatal(err)
	}
	p, err := e.core.Agents.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
	if err != nil {
		t.Fatal(err)
	}
	return resp.AgentId, key, p.LeafDer
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
	srv := e.base.Agents.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: hub, plat: p}
}

// ---- from tenancy_test.go ----

func certOrg(t *testing.T, der []byte) string {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil || len(c.Subject.Organization) != 1 {
		t.Fatalf("certificate: %v %v", c, err)
	}
	return c.Subject.Organization[0]
}

// ---- from twin_test.go ----

// twinAgent approves an agent for a cluster in the hub's organisation and gives it an empty picture, connected.
func twinAgent(t *testing.T, e *env, h *Hub, cluster string) store.Agent {
	t.Helper()
	id, _, _ := e.approvedAgent(t, cluster)
	a, err := e.st.GetAgent(e.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	h.Mu.Lock()
	v := NewView()
	v.Connected = true
	v.LastBeat = h.C.Now()
	h.Views[id] = v
	h.Mu.Unlock()
	return a
}

func twinNode(name, machine string) *continuumv1.NodeFacts {
	return &continuumv1.NodeFacts{Key: name, Name: name, MachineId: machine, CpuCapacityMillis: 4000, CpuAllocatableMillis: 3800,
		MemoryCapacityBytes: 8 << 30, MemoryAllocatableBytes: 7 << 30, Ready: true}
}

func twinWorkload(ns, name string) *continuumv1.WorkloadFacts {
	return &continuumv1.WorkloadFacts{Key: ns + "/Deployment/" + name, Namespace: ns, Kind: "Deployment", Name: name, Replicas: 1, ReadyReplicas: 1, CpuRequestMillis: 250, MemoryRequestBytes: 256 << 20}
}

func twinFull(cluster string, nodes []*continuumv1.NodeFacts, ws ...*continuumv1.WorkloadFacts) *continuumv1.Sync {
	return &continuumv1.Sync{Full: true, Cluster: &continuumv1.ClusterFacts{Uid: cluster}, Nodes: nodes, Workloads: ws,
		Namespaces: []*continuumv1.NamespaceFacts{{Key: "shop", Name: "shop"}}}
}

// ---- from applications_graph_test.go ----

func (s *linkSpy) LinkEntitiesBatch(_ context.Context, _ string, _ time.Time, _, _, _ string, sets []graph.MemberSet) error {
	s.sets = map[string][]string{}
	for _, m := range sets {
		s.sets[m.ID] = m.TargetIDs
	}
	return nil
}

// ---- from grpc_test.go ----

// dialProxied is r.dial, but first writes a v1 PROXY protocol line declaring proxiedIP on the raw TCP
// connection, the way a load balancer configured to send it would - before grpc-go layers TLS on top of
// the same connection.
func (r *rig) dialProxied(t *testing.T, cfg *tls.Config, proxiedIP string) *grpc.ClientConn {
	t.Helper()
	cc, err := grpc.NewClient(r.addr,
		grpc.WithTransportCredentials(credentials.NewTLS(cfg)),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			c, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			if _, err := c.Write([]byte("PROXY TCP4 " + proxiedIP + " 127.0.0.1 54321 8443\r\n")); err != nil {
				c.Close()
				return nil, err
			}
			return c, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cc.Close() })
	return cc
}

// ---- from history_test.go ----

func (f *failHistory) AddEvents(ctx context.Context, org string, evs []store.Event) error {
	if f.events.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddEvents(ctx, org, evs)
}

func (f *failHistory) AddHistory(ctx context.Context, org string, at time.Time, data []byte) error {
	if f.snapshots.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddHistory(ctx, org, at, data)
}

// ---- from linkcreds_test.go ----

func (cr *credRig) rows(t *testing.T) []store.LinkCredential {
	t.Helper()
	rows, err := cr.st.ListLinkCredentials(cr.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// ---- from paths_links_test.go ----

func (agentsDown) ListAgents(context.Context, string) ([]store.Agent, error) {
	return nil, errors.New("database is locked")
}

// ---- from fake_agent_test.go ----

// drop ends the stream the way a crash or a cut network does: nothing is said, the connection just stops.
func (f *fakeAgent) drop() {
	f.t.Helper()
	f.cancel()
	f.waitUntil("the server to notice the stream ended", func() bool {
		f.r.hub.Mu.Lock()
		defer f.r.hub.Mu.Unlock()
		return f.r.hub.Sessions[f.id] == nil
	})
}

// waitUntil polls until cond is true. The server handles a message on its own goroutine, so a test that sent one has to wait for it.
func (f *fakeAgent) waitUntil(what string, cond func() bool) {
	f.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
