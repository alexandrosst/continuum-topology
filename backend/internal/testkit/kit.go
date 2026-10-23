// Package testkit is what the server's tests and the estate's tests build their worlds from: the fingerprints of the clusters they enrol,
// a certificate request and its approval code, the facts, flows and workloads they feed a hub, the twin's inputs, and the stubs that
// make a store or a history fail on purpose. It is a library for tests; nothing outside a test imports it.
package testkit

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/approval"
	"ikhnos/internal/connectivity"
	"ikhnos/internal/facts"
	"ikhnos/internal/kernel"
	"ikhnos/internal/observed"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ikhnos/internal/graph"
	"ikhnos/internal/store"
)

// LinkSpy is a store that keeps what the graph would be asked to link.
type LinkSpy struct {
	store.Store
	Sets map[string][]string
}

// LinkEntitiesBatch records the member sets it is asked to link, by entity.
func (s *LinkSpy) LinkEntitiesBatch(_ context.Context, _ string, _ time.Time, _, _, _ string, sets []graph.MemberSet) error {
	s.Sets = map[string][]string{}
	for _, m := range sets {
		s.Sets[m.ID] = m.TargetIDs
	}
	return nil
}

// FP is the fingerprint (the cluster's UID) of the first cluster a test enrols.
const FP = "8f3c2a9e-1111-4222-8333-944455556666"

// CSR makes a key and a certificate request for it.
func CSR(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, k)
	if err != nil {
		t.Fatal(err)
	}
	return der, k
}

// ApprovalFor makes an approval code for the key in csrDER and the hash the agent sends for it.
func ApprovalFor(t *testing.T, csrDER []byte) (hash []byte, code string) {
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

// HashFor is approvalFor when the code is not needed.
func HashFor(t *testing.T, csrDER []byte) []byte {
	t.Helper()
	hash, _ := ApprovalFor(t, csrDER)
	return hash
}

// KindOf is the kind of a kernel error, or the zero kind when err is not one.
func KindOf(err error) kernel.Kind {
	var e *kernel.Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return 0
}

// StatusCode is the gRPC code of err.
func StatusCode(err error) codes.Code { return status.Code(err) }

// ExtEdge adds to ft one flow from a workload to an external address, with the protocol and the noise class given.
func ExtEdge(ft *observed.FlowTable, src, dstIP string, port uint32, proto string, conns uint64, noise string) {
	f := FlowOf(WorkloadEndpoint(src), ExternalEndpoint(dstIP), port, conns)
	f.Protocol = proto
	f.Noise = noise
	ft.Apply(&ikhnosv1.FlowBatch{WindowSeconds: 60, Flows: []*ikhnosv1.Flow{f}}, time.Now())
}

// FailHistory fails the event and snapshot writes while its flags are set.
type FailHistory struct {
	store.Store
	Events, Snapshots atomic.Bool
}

// AddEvents fails while Events is set.
func (f *FailHistory) AddEvents(ctx context.Context, org string, evs []store.Event) error {
	if f.Events.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddEvents(ctx, org, evs)
}

// AddHistory fails while Snapshots is set.
func (f *FailHistory) AddHistory(ctx context.Context, org string, at time.Time, data []byte) error {
	if f.Snapshots.Load() {
		return errors.New("database is locked")
	}
	return f.Store.AddHistory(ctx, org, at, data)
}

// SyncMsg wraps a picture as the message an agent sends.
func SyncMsg(s *ikhnosv1.Sync) *ikhnosv1.AgentMessage {
	return &ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Sync{Sync: s}}
}

// FP3 is the fingerprint of a third cluster.
const FP3 = "8f3c2a9e-3333-4222-8333-944455556688"

// LockedBuf is a buffer a logger can write to while a test reads it.
type LockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

// Write appends to the buffer.
func (l *LockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// String is what has been written.
func (l *LockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// SetupNow is the time the link-setup tests treat as now.
var SetupNow = time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)

// Caps is a fresh report: Cluster links and probing on, telemetry sent onward, the prober seen a minute ago.
func Caps(f ...func(*agents.LinkCaps)) agents.LinkCaps {
	c := agents.LinkCaps{Reported: SetupNow.Add(-30 * time.Second), LinkMetrics: true, ProbeEnabled: true, TelemetryExport: true, ProberSeen: SetupNow.Add(-time.Minute)}
	for _, g := range f {
		g(&c)
	}
	return c
}

// Without clears one thing a fresh report has: links, probe, export or prober.
func Without(what string) func(*agents.LinkCaps) {
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

// StepStates writes the steps as "code/cluster=state", in order.
func StepStates(steps []connectivity.LinkSetupStep) string {
	var out []string
	for _, s := range steps {
		k := string(s.Code)
		if s.Cluster != "" {
			k += "/" + s.Cluster
		}
		out = append(out, k+"="+string(s.State))
	}
	return strings.Join(out, " ")
}

// StepNamed is the step with the code, for the cluster, or a test failure.
func StepNamed(t *testing.T, steps []connectivity.LinkSetupStep, code connectivity.StepCode, cluster string) connectivity.LinkSetupStep {
	t.Helper()
	for _, s := range steps {
		if s.Code == code && s.Cluster == cluster {
			return s
		}
	}
	t.Fatalf("no %s step for %q in %s", code, cluster, StepStates(steps))
	return connectivity.LinkSetupStep{}
}

// NodeState is one node with the given internal address and, if any, external ones.
func NodeState(internal string, external ...string) *facts.State {
	n := &ikhnosv1.NodeFacts{InternalIps: []string{internal}}
	for _, e := range external {
		if e != "" {
			n.ExternalIps = append(n.ExternalIps, e)
		}
	}
	return &facts.State{Nodes: map[string]*ikhnosv1.NodeFacts{"n1": n}}
}

// Agent is an approved agent for a cluster, at a tier, connecting from ip.
func Agent(id, cluster, name, ip string, tier int) store.Agent {
	return store.Agent{ID: id, ClusterID: cluster, Name: name, Status: store.StatusApproved, AccessTier: tier, ConnectingIP: ip}
}

// Workload is a workload fact with the key a namespace, kind and name make, and the addresses it is reachable on.
func Workload(ns, kind, name string, reach ...*ikhnosv1.Address) *ikhnosv1.WorkloadFacts {
	return &ikhnosv1.WorkloadFacts{Key: ns + "/" + kind + "/" + name, Namespace: ns, Kind: kind, Name: name, Reachable: reach}
}

// WorkloadEndpoint is the end of a flow that is a workload.
func WorkloadEndpoint(key string) *ikhnosv1.FlowEndpoint {
	return &ikhnosv1.FlowEndpoint{Kind: ikhnosv1.FlowEndpoint_WORKLOAD, Ref: key}
}

// ExternalEndpoint is the end of a flow that is an address outside the cluster.
func ExternalEndpoint(ip string) *ikhnosv1.FlowEndpoint {
	return &ikhnosv1.FlowEndpoint{Kind: ikhnosv1.FlowEndpoint_EXTERNAL, Ip: ip}
}

// FlowOf is a TCP flow with the connections given and the bytes they imply.
func FlowOf(src, dst *ikhnosv1.FlowEndpoint, port uint32, conns uint64) *ikhnosv1.Flow {
	return &ikhnosv1.Flow{Src: src, Dst: dst, Port: port, Protocol: "tcp", Connections: conns, BytesOut: 1000 * conns, BytesIn: 3000 * conns, Method: "ebpf", BytesKnown: true}
}

// FP2 is the fingerprint of a second cluster.
const FP2 = "8f3c2a9e-2222-4222-8333-944455556677"

// AgentsDown is a store whose list of the organisation's agents cannot be read (a busy database).
type AgentsDown struct{ store.Store }

// ListAgents fails.
func (AgentsDown) ListAgents(context.Context, string) ([]store.Agent, error) {
	return nil, errors.New("database is locked")
}

// AsRole is a context whose caller holds the role.
func AsRole(role string) context.Context {
	return accounts.WithCaller(context.Background(), accounts.Principal{Role: role})
}

// LineDiff lists the lines found in only one of the two texts, "-" for the golden file and "+" for what the source now says.
func LineDiff(want, got string) string {
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

// CSRWith is a certificate request for a key the test already has.
func CSRWith(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	d, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// CertOrg is the one organisation a certificate was issued for.
func CertOrg(t *testing.T, der []byte) string {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil || len(c.Subject.Organization) != 1 {
		t.Fatalf("certificate: %v %v", c, err)
	}
	return c.Subject.Organization[0]
}

// TwinNode is a ready node of 4 CPUs and 8 GiB with the machine id given.
func TwinNode(name, machine string) *ikhnosv1.NodeFacts {
	return &ikhnosv1.NodeFacts{Key: name, Name: name, MachineId: machine, CpuCapacityMillis: 4000, CpuAllocatableMillis: 3800,
		MemoryCapacityBytes: 8 << 30, MemoryAllocatableBytes: 7 << 30, Ready: true}
}

// TwinWorkload is a Deployment with one ready replica.
func TwinWorkload(ns, name string) *ikhnosv1.WorkloadFacts {
	return &ikhnosv1.WorkloadFacts{Key: ns + "/Deployment/" + name, Namespace: ns, Kind: "Deployment", Name: name, Replicas: 1, ReadyReplicas: 1, CpuRequestMillis: 250, MemoryRequestBytes: 256 << 20}
}

// TwinFull is a full picture of a cluster with the nodes and workloads given.
func TwinFull(cluster string, nodes []*ikhnosv1.NodeFacts, ws ...*ikhnosv1.WorkloadFacts) *ikhnosv1.Sync {
	return &ikhnosv1.Sync{Full: true, Cluster: &ikhnosv1.ClusterFacts{Uid: cluster}, Nodes: nodes, Workloads: ws,
		Namespaces: []*ikhnosv1.NamespaceFacts{{Key: "shop", Name: "shop"}}}
}
