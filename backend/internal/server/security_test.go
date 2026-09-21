package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/pki"

	"google.golang.org/grpc/codes"
)

// approvedAgent enrolls and approves an agent, returning its id, private key and first certificate.
func (e *env) approvedAgent(t *testing.T, fingerprint string) (string, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	secret, _, err := e.core.CreateToken(e.ctx, "admin", "edge", 2)
	if err != nil {
		t.Fatal(err)
	}
	d, key := csr(t)
	resp, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fingerprint, InstalledAccessTier: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.core.Approve(e.ctx, "alex", resp.AgentId, fingerprint[:8], 2); err != nil {
		t.Fatal(err)
	}
	p, err := e.core.Poll(e.ctx, "10.0.0.1", &continuumv1.PollRequest{AgentId: resp.AgentId, PollSecret: resp.PollSecret})
	if err != nil {
		t.Fatal(err)
	}
	return resp.AgentId, key, p.LeafDer
}

func csrWith(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	d, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestFingerprintMustBeCanonicalUUID(t *testing.T) {
	e := newEnv(t)
	for _, bad := range []string{
		"aaaaaaaa-bbbb",                            // the old "anything 8-64 chars" shape
		"8F3C2A9E-1111-4222-8333-944455556666",     // upper case: k8s UIDs are lower case
		"8f3c2a9e-1111-4222-8333-94445555666",      // one digit short
		"8f3c2a9e-1111-4222-8333-944455556666-abc", // trailing junk
		"../../etc/passwd-0000-0000-0000-000000000000",
		"",
	} {
		secret, _, _ := e.core.CreateToken(e.ctx, "admin", "c", 1)
		d, _ := csr(t)
		_, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: bad})
		if kindOf(err) != KindInvalid {
			t.Errorf("fingerprint %q: %v, want invalid", bad, err)
		}
	}
}

func TestEnrollRejectsOversizedVersionAndScrubsControlCharacters(t *testing.T) {
	e := newEnv(t)
	secret, _, _ := e.core.CreateToken(e.ctx, "admin", "c", 1)
	d, _ := csr(t)
	_, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, AgentVersion: strings.Repeat("x", 65)})
	if kindOf(err) != KindInvalid {
		t.Fatalf("long version: %v", err)
	}
	resp, err := e.core.Enroll(e.ctx, "10.0.0.1", &continuumv1.EnrollRequest{Token: secret, CsrDer: d, ClusterFingerprint: fp, AgentVersion: "1.0\n\x1b[31mred"})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.GetAgent(e.ctx, resp.AgentId)
	if strings.ContainsAny(a.Version, "\n\x1b") {
		t.Fatalf("control characters survived: %q", a.Version)
	}
}

func TestRejoinAfterOutageAndItsLimits(t *testing.T) {
	e := newEnv(t)
	id, key, leaf := e.approvedAgent(t, fp)
	real := *e.now
	rejoin := func(leafDER []byte, k *ecdsa.PrivateKey) error {
		_, err := e.core.Rejoin(e.ctx, "10.0.0.1", &continuumv1.RejoinRequest{ExpiredLeafDer: leafDER, CsrDer: csrWith(t, k)})
		return err
	}

	// Offline for two days: the 24 h certificate is long expired, rejoin works.
	*e.now = real.Add(48 * time.Hour)
	if err := rejoin(leaf, key); err != nil {
		t.Fatalf("rejoin after a two day outage: %v", err)
	}
	cur, _ := e.st.GetAgent(e.ctx, id)
	if string(cur.LeafDER) == string(leaf) {
		t.Fatal("the new certificate was not stored")
	}

	// A request made with a different key is refused even though the certificate is genuine.
	_, otherKey := csr(t)
	if err := rejoin(leaf, otherKey); kindOf(err) != KindUnauthenticated {
		t.Fatalf("someone else's key: %v", err)
	}
	// A certificate from a different CA is refused.
	other, _ := pki.LoadOrCreate(t.TempDir())
	parsed, _ := pki.ParseCSR(csrWith(t, key))
	foreign, _, _ := other.IssueAgent(parsed, id, "org-1", time.Hour)
	if err := rejoin(foreign, key); kindOf(err) != KindUnauthenticated {
		t.Fatalf("foreign CA: %v", err)
	}
	// Garbage is refused.
	if err := rejoin([]byte("nope"), key); kindOf(err) != KindUnauthenticated {
		t.Fatalf("garbage: %v", err)
	}
	// Too long ago: the window is 7 days.
	*e.now = real.Add(10 * 24 * time.Hour)
	if err := rejoin(leaf, key); kindOf(err) != KindUnauthenticated {
		t.Fatalf("outside the window: %v", err)
	}
	// Revocation always wins, even inside the window.
	*e.now = real.Add(48 * time.Hour)
	if err := e.core.Revoke(e.ctx, "alex", id, "test"); err != nil {
		t.Fatal(err)
	}
	if err := rejoin(leaf, key); kindOf(err) != KindUnauthenticated {
		t.Fatalf("revoked agent rejoined: %v", err)
	}
}

func TestRejoinIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t)
	e.core.EnrollRL = NewLimiter(1, 2)
	for i := 0; i < 5; i++ {
		_, err := e.core.Rejoin(e.ctx, "10.9.9.9", &continuumv1.RejoinRequest{ExpiredLeafDer: []byte("x"), CsrDer: []byte("x")})
		want := KindUnauthenticated
		if i >= 2 {
			want = KindRateLimited
		}
		if kindOf(err) != want {
			t.Fatalf("attempt %d: %v, want kind %v", i, err, want)
		}
	}
}

func TestRenewIsRateLimitedPerAgent(t *testing.T) {
	e := newEnv(t)
	id, _, _ := e.approvedAgent(t, fp)
	e.core.RenewRL = NewLimiter(1, 2)
	var limited int
	for i := 0; i < 5; i++ {
		d, _ := csr(t)
		if _, _, err := e.core.Renew(e.ctx, id, d); kindOf(err) == KindRateLimited {
			limited++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if limited != 3 {
		t.Fatalf("limited %d of 5 renewals, want 3", limited)
	}
}

func TestLimiterForgetsIdleKeysAndEvictsTheOldestAtTheCap(t *testing.T) {
	l := NewLimiter(60, 2)
	clock := time.Unix(1_000_000, 0)
	l.now = func() time.Time { return clock }
	for i := 0; i < 10; i++ {
		l.Allow(fmt.Sprintf("k%d", i))
	}
	if len(l.buckets) != 10 {
		t.Fatalf("buckets = %d", len(l.buckets))
	}
	clock = clock.Add(time.Minute) // every bucket has refilled: forgetting them is lossless
	l.Allow("fresh")
	if len(l.buckets) != 1 || l.order.Len() != 1 {
		t.Fatalf("idle buckets were not swept: %d left", len(l.buckets))
	}

	// At the cap a newcomer is admitted (it is not locked out by a spray of other addresses), the key used
	// longest ago is the one forgotten, and a key that is being throttled right now is kept.
	l = NewLimiter(1, 1)
	l.max = 100
	l.now = func() time.Time { return clock }
	if !l.Allow("victim") || l.Allow("victim") {
		t.Fatal("setup: the victim should be out of tokens")
	}
	for i := 0; i < 500; i++ {
		clock = clock.Add(time.Millisecond)
		if !l.Allow(fmt.Sprintf("spray%d", i)) {
			t.Fatalf("spray key %d: a new key was refused at the cap", i)
		}
		if i%10 == 0 && l.Allow("victim") {
			t.Fatal("the throttled key was forgotten and got a fresh bucket")
		}
	}
	if len(l.buckets) != 100 || l.order.Len() != 100 {
		t.Fatalf("memory is not bounded: %d keys, %d in the order", len(l.buckets), l.order.Len())
	}
	if _, ok := l.buckets["spray0"]; ok {
		t.Fatal("the oldest key was not the one evicted")
	}
	if _, ok := l.buckets["spray499"]; !ok {
		t.Fatal("the newest key was evicted")
	}
}

func TestLimitKeyGroupsIPv6ByPrefix(t *testing.T) {
	for in, want := range map[string]string{
		"203.0.113.9":                      "203.0.113.9",
		"::ffff:203.0.113.9":               "203.0.113.9",
		"2001:db8:1:2:aaaa:bbbb:cccc:dddd": "2001:db8:1:2::/64",
		"2001:db8:1:2:1111:2222:3333:4444": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":                  "2001:db8:1:3::/64",
		"fe80::1%eth0":                     "fe80::/64",
		"not an address":                   "not an address",
		"unknown":                          "unknown",
	} {
		if got := LimitKey(in); got != want {
			t.Errorf("LimitKey(%q) = %q, want %q", in, got, want)
		}
	}
	// One host cycling through addresses in its /64 shares one allowance.
	l := NewLimiter(1, 3)
	allowed := 0
	for i := 0; i < 100; i++ {
		if l.Allow(LimitKey(fmt.Sprintf("2001:db8:9:9::%x", i+1))) {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("%d of 100 requests from one /64 were allowed, want the burst of 3", allowed)
	}
}

func TestTapCutsOffUnauthenticatedCallersBeforeTheBodyIsRead(t *testing.T) {
	r := newRig(t)
	r.base.TapRL = NewLimiter(1, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := r.core.CA.Pin()

	// Enrollment: the third call from one address in a burst is refused by the tap.
	enr := continuumv1.NewEnrollmentClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	var limited int
	for i := 0; i < 4; i++ {
		_, err := enr.PollEnrollment(ctx, &continuumv1.PollRequest{AgentId: "ag-x", PollSecret: "s"})
		if code(err) == codes.ResourceExhausted {
			limited++
		}
	}
	if limited != 2 {
		t.Fatalf("tap limited %d of 4, want 2", limited)
	}

	// AgentService without a certificate: refused even with a 12 MB message.
	anon := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(pin, "127.0.0.1", nil)))
	if _, err := anon.Renew(ctx, &continuumv1.RenewRequest{CsrDer: make([]byte, 12<<20)}); code(err) != codes.Unauthenticated {
		t.Fatalf("large anonymous message: %v", err)
	}
}

func TestSyncMustMatchTheApprovedClusterAndStayInsideItsTier(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	// Pretend the agent last connected from another address: that must be audited, not blocked.
	_ = r.st.Touch(ctx, id, "203.0.113.7", "", "", *r.now)

	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	connect := func() continuumv1.AgentService_ConnectClient {
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

	// A sync claiming another cluster is refused and the stream ends.
	s := connect()
	_ = s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Sync{Sync: &continuumv1.Sync{Seq: 1, Full: true,
		Cluster: &continuumv1.ClusterFacts{Uid: "11111111-2222-4333-8444-555555555555"},
		Nodes:   []*continuumv1.NodeFacts{{Key: "n1", Name: "n1"}}}}})
	if _, err := s.Recv(); code(err) != codes.PermissionDenied {
		t.Fatalf("foreign cluster uid: %v", err)
	}
	if st, _ := r.hub.State(ctx); len(st.Topology.Nodes) != 0 {
		t.Fatal("facts from the wrong cluster were kept")
	}

	// A sync with too many items is refused.
	s = connect()
	big := make([]*continuumv1.NodeFacts, maxNodes+1)
	for i := range big {
		big[i] = &continuumv1.NodeFacts{Key: fmt.Sprint(i)}
	}
	_ = s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Sync{Sync: &continuumv1.Sync{Seq: 1, Full: true, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: big}}})
	if _, err := s.Recv(); code(err) != codes.InvalidArgument {
		t.Fatalf("oversized sync: %v", err)
	}

	// The audit trail recorded both refusals and the address change.
	var actions []string
	events, _ := r.st.ListAudit(ctx, "org-1", 50)
	for _, ev := range events {
		actions = append(actions, ev.Action)
	}
	all := strings.Join(actions, ",")
	for _, want := range []string{"sync-refused", "agent-address-changed"} {
		if !strings.Contains(all, want) {
			t.Errorf("audit log lacks %q: %s", want, all)
		}
	}
}

func TestTierZeroDropsClassListsToo(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	a.AccessTier = 0
	r.hub.mu.Lock()
	r.hub.views[id] = &view{state: facts.New()}
	r.hub.mu.Unlock()
	r.hub.applySync(a, &continuumv1.Sync{Full: true,
		Cluster: &continuumv1.ClusterFacts{Uid: fp, StorageClasses: []string{"gp3"}, IngressClasses: []string{"nginx"}},
		Nodes:   []*continuumv1.NodeFacts{{Key: "n", Name: "n"}}}, false)
	st := r.hub.views[id].state
	if len(st.Nodes) != 0 || len(st.Cluster.StorageClasses) != 0 || len(st.Cluster.IngressClasses) != 0 {
		t.Fatalf("tier 0 kept tier 1 facts: %+v", st.Cluster)
	}
}

// Tier 1 approval covers nodes but not pods, so pod-derived node figures must be unknown, not zero and not
// whatever a tier-2 agent happened to send.
func TestTierOneDropsPodDerivedNodeFigures(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	a.AccessTier = 1
	r.hub.mu.Lock()
	r.hub.views[id] = &view{state: facts.New()}
	r.hub.mu.Unlock()
	cnt := int32(12)
	r.hub.applySync(a, &continuumv1.Sync{Full: true,
		Cluster:   &continuumv1.ClusterFacts{Uid: fp},
		Nodes:     []*continuumv1.NodeFacts{{Key: "n", Name: "n", PodCapacity: 110, PodCount: &cnt, CpuRequestedMillis: 900, MemoryRequestedBytes: 1 << 30}},
		Workloads: []*continuumv1.WorkloadFacts{{Key: "ns/Deployment/x", VolumeClaims: []*continuumv1.VolumeClaim{{Name: "v"}}}}}, false)
	st := r.hub.views[id].state
	n := st.Nodes["n"]
	if n == nil || n.PodCapacity != 110 || n.PodCount != nil || n.CpuRequestedMillis != 0 || n.MemoryRequestedBytes != 0 {
		t.Fatalf("tier 1 node = %+v", n)
	}
	if len(st.Workloads) != 0 {
		t.Fatal("tier 1 kept workloads")
	}
}

func TestValidateSyncBoundsStorageAndScalingFacts(t *testing.T) {
	claims := make([]*continuumv1.VolumeClaim, 201)
	for i := range claims {
		claims[i] = &continuumv1.VolumeClaim{Name: "v"}
	}
	for name, w := range map[string]*continuumv1.WorkloadFacts{
		"too many claims":   {Key: "k", VolumeClaims: claims},
		"huge claim name":   {Key: "k", VolumeClaims: []*continuumv1.VolumeClaim{{Name: strings.Repeat("a", maxStr+1)}}},
		"pinned to a crowd": {Key: "k", VolumeClaims: []*continuumv1.VolumeClaim{{Name: "v", PinnedNodes: make([]string, 101)}}},
		"many targets":      {Key: "k", Autoscaler: &continuumv1.Autoscaler{Targets: make([]string, 51)}},
		"long budget":       {Key: "k", Disruption: &continuumv1.Disruption{MinAvailable: strings.Repeat("9", 33)}},
	} {
		if validateSync(&continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{w}}) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	ok := &continuumv1.WorkloadFacts{Key: "k", VolumeClaims: claims[:200], Autoscaler: &continuumv1.Autoscaler{Targets: make([]string, 50)}, Disruption: &continuumv1.Disruption{MinAvailable: "50%"}}
	if err := validateSync(&continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{ok}}); err != nil {
		t.Errorf("a normal workload was refused: %v", err)
	}
}

type hubRig struct {
	*rig
	hub *Hub
}

// newHubRig is newRig with the real sync hub behind the gRPC server.
func newHubRig(t *testing.T) *hubRig {
	t.Helper()
	e := newEnv(t)
	p := NewPlatform(e.base, nil)
	tn, err := p.Tenant(e.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	hub := tn.Hub
	e.core = tn.C // the hub reads its settings from this core
	srv := e.base.NewGRPC(pki.NewServerCerts(e.core.CA, []string{"127.0.0.1"}), p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	return &hubRig{rig: &rig{env: e, addr: l.Addr().String()}, hub: hub}
}
