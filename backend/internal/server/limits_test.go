package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
	"continuum/internal/pki"
	"continuum/internal/store"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

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

func nodes(prefix string, n int) []*continuumv1.NodeFacts {
	var out []*continuumv1.NodeFacts
	for i := 0; i < n; i++ {
		out = append(out, &continuumv1.NodeFacts{Key: fmt.Sprintf("%s%d", prefix, i), Name: fmt.Sprintf("%s%d", prefix, i)})
	}
	return out
}

func TestADeltaStreamCannotGrowTheStateBeyondTheLimits(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 50, Namespaces: 50, Workloads: 50, Bytes: 1 << 20}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	s := r.agentStream(t, ctx, id, key, leaf)
	cluster := &continuumv1.ClusterFacts{Uid: fp}

	send := func(m *continuumv1.Sync) {
		if err := s.Send(syncMsg(m)); err != nil {
			t.Fatal(err)
		}
	}
	send(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Nodes: nodes("n", 40)})
	if m, err := s.Recv(); err != nil || m.GetAck().GetSeq() != 1 {
		t.Fatalf("the first full sync: %v %v", m, err)
	}
	// Each delta is small and within any per-message cap; the third takes the total past 50.
	for i := 2; i <= 3; i++ {
		send(&continuumv1.Sync{Seq: uint64(i), Nodes: nodes(fmt.Sprintf("d%d-", i), 5)})
		if m, err := s.Recv(); err != nil || m.GetAck().GetSeq() != uint64(i) {
			t.Fatalf("delta %d: %v %v", i, m, err)
		}
	}
	send(&continuumv1.Sync{Seq: 4, Nodes: nodes("d4-", 5)})
	_, err := s.Recv()
	if code(err) != codes.ResourceExhausted || !strings.Contains(err.Error(), "nodes") || !strings.Contains(err.Error(), "at most 50") {
		t.Fatalf("the delta that broke the limit: %v", err)
	}
	r.hub.mu.Lock()
	held := len(r.hub.views[id].state.Nodes)
	r.hub.mu.Unlock()
	if held != 50 {
		t.Fatalf("the server holds %d nodes; the refused delta must change nothing", held)
	}

	// The refusal is where an administrator looks: audit trail, events, and the agent's own entry.
	evs, _ := r.st.ListAudit(ctx, "org-1", 20)
	var audited bool
	for _, e := range evs {
		audited = audited || (e.Action == "sync-refused" && strings.Contains(e.Detail, "limit"))
	}
	if !audited {
		t.Fatalf("no sync-refused audit row: %+v", evs)
	}
	problems, _ := r.st.ListEvents(ctx, "org-1", store.EventQuery{Kind: "sync-refused", Since: time.Now().Add(-time.Hour)})
	if len(problems) != 1 || problems[0].Severity != "warning" {
		t.Fatalf("events: %+v", problems)
	}
	doc, _ := r.hub.State(ctx)
	var shown bool
	for _, a := range doc.Agents {
		for _, m := range a.Modules {
			shown = shown || (m.Name == "server limits" && m.Status == "error" && strings.Contains(m.Reason, "nodes"))
		}
	}
	if !shown {
		t.Fatalf("the agent's entry does not say why nothing new arrives: %+v", doc.Agents)
	}

	// A well-behaved next connection recovers: a full picture within the limits replaces the state and clears the message.
	s2 := r.agentStream(t, ctx, id, key, leaf)
	if err := s2.Send(syncMsg(&continuumv1.Sync{Seq: 5, Full: true, Cluster: cluster, Nodes: nodes("ok", 10)})); err != nil {
		t.Fatal(err)
	}
	if m, err := s2.Recv(); err != nil || m.GetAck().GetSeq() != 5 {
		t.Fatalf("recovery: %v %v", m, err)
	}
	doc, _ = r.hub.State(ctx)
	for _, a := range doc.Agents {
		for _, m := range a.Modules {
			if m.Name == "server limits" {
				t.Fatal("the message stayed after a good sync")
			}
		}
	}
}

func TestRepeatedRefusalsAreRecordedOncePerWindow(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 5, Namespaces: 5, Workloads: 5, Bytes: 1 << 20}
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	v := &view{state: facts.New()}
	r.hub.mu.Lock()
	r.hub.views[id] = v
	r.hub.mu.Unlock()
	for i := 0; i < 30; i++ {
		r.hub.refuseSync(r.ctx, a, v, fmt.Errorf("too much %d", i))
	}
	*r.now = r.now.Add(refusalEvery + time.Second)
	r.hub.refuseSync(r.ctx, a, v, fmt.Errorf("still too much"))
	n := 0
	evs, _ := r.st.ListAudit(r.ctx, "org-1", 100)
	for _, e := range evs {
		if e.Action == "sync-refused" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("%d audit rows for 31 refusals in two windows, want 2", n)
	}
}

func TestUniqueKeyFloodThroughApplySyncStopsAtTheLimit(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 100, Workloads: 300, Bytes: 1 << 30}
	id, _, _ := r.approvedAgent(t, fp)
	a, _ := r.st.GetAgent(r.ctx, id)
	a.AccessTier = 2
	r.hub.mu.Lock()
	r.hub.views[id] = &view{state: facts.New()}
	r.hub.mu.Unlock()
	refused := 0
	for i := 0; i < 1000; i++ {
		s := &continuumv1.Sync{Seq: uint64(i), Cluster: &continuumv1.ClusterFacts{Uid: fp}, Workloads: []*continuumv1.WorkloadFacts{{Key: fmt.Sprintf("ns/Deployment/%d", i), Name: "x"}}}
		if _, _, err := r.hub.applySync(a, s, true); err != nil {
			refused++
		}
	}
	r.hub.mu.Lock()
	held := len(r.hub.views[id].state.Workloads)
	r.hub.mu.Unlock()
	if held != 300 || refused != 700 {
		t.Fatalf("held %d, refused %d", held, refused)
	}
}

func TestLongStringsAreRefusedAndLongLabelValuesAreCut(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, fp)
	cluster := &continuumv1.ClusterFacts{Uid: fp}

	s := r.agentStream(t, ctx, id, key, leaf)
	long := &continuumv1.WorkloadFacts{Key: "ns/Deployment/a", Name: "a", Labels: map[string]string{"l": strings.Repeat("v", 50000)}}
	if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 1, Full: true, Cluster: cluster, Workloads: []*continuumv1.WorkloadFacts{long}})); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Recv(); err != nil || m.GetAck() == nil {
		t.Fatalf("a long label value should be cut, not refused: %v %v", m, err)
	}
	r.hub.mu.Lock()
	got := r.hub.views[id].state.Workloads["ns/Deployment/a"].Labels["l"]
	r.hub.mu.Unlock()
	if len(got) != facts.MaxMapValue {
		t.Fatalf("stored label is %d bytes", len(got))
	}

	for name, w := range map[string]*continuumv1.WorkloadFacts{
		"name":      {Key: "ns/Deployment/b", Name: strings.Repeat("n", 5000)},
		"key":       {Key: strings.Repeat("k", 600)},
		"empty key": {Key: ""},
		"image":     {Key: "ns/Deployment/c", Images: []*continuumv1.ContainerImage{{Image: strings.Repeat("i", 2000)}}},
		"label key": {Key: "ns/Deployment/d", Labels: map[string]string{strings.Repeat("k", 2000): "v"}},
	} {
		s := r.agentStream(t, ctx, id, key, leaf)
		if err := s.Send(syncMsg(&continuumv1.Sync{Seq: 9, Cluster: cluster, Workloads: []*continuumv1.WorkloadFacts{w}})); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Recv(); code(err) != codes.InvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	n := &continuumv1.NodeFacts{Key: "n", Name: strings.Repeat("n", 2000)}
	s = r.agentStream(t, ctx, id, key, leaf)
	_ = s.Send(syncMsg(&continuumv1.Sync{Seq: 10, Cluster: cluster, Nodes: []*continuumv1.NodeFacts{n}}))
	if _, err := s.Recv(); code(err) != codes.InvalidArgument {
		t.Errorf("long node name: %v", err)
	}
	// Heartbeat module lists are bounded too; they were not before.
	s = r.agentStream(t, ctx, id, key, leaf)
	mods := make([]*continuumv1.ModuleStatus, 100)
	for i := range mods {
		mods[i] = &continuumv1.ModuleStatus{Name: "m", Reason: strings.Repeat("r", 100000)}
	}
	_ = s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Heartbeat{Heartbeat: &continuumv1.Heartbeat{Modules: mods}}})
	if _, err := s.Recv(); code(err) != codes.InvalidArgument {
		t.Errorf("huge heartbeat modules: %v", err)
	}
}

func marshalState(t *testing.T, m *continuumv1.Sync) []byte {
	t.Helper()
	m.Full = true
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAPoisonedStoredSnapshotIsDroppedOnRestore(t *testing.T) {
	e := newEnv(t)
	good, _, _ := e.approvedAgent(t, fp)
	tooMany, _, _ := e.approvedAgent(t, "aaaaaaaa-1111-4222-8333-944455556666")
	longName, _, _ := e.approvedAgent(t, "bbbbbbbb-1111-4222-8333-944455556666")
	tooBig, _, _ := e.approvedAgent(t, "cccccccc-1111-4222-8333-944455556666")
	garbage, _, _ := e.approvedAgent(t, "dddddddd-1111-4222-8333-944455556666")

	small := facts.Limits{Nodes: 10, Namespaces: 10, Workloads: 10, Bytes: 4 << 10}
	save := func(id string, b []byte) {
		if err := e.st.SaveSnapshot(e.ctx, id, b, *e.now); err != nil {
			t.Fatal(err)
		}
	}
	save(good, marshalState(t, &continuumv1.Sync{Seq: 3, Cluster: &continuumv1.ClusterFacts{Uid: fp}, Nodes: nodes("n", 5)}))
	save(tooMany, marshalState(t, &continuumv1.Sync{Nodes: nodes("n", 11)}))
	save(longName, marshalState(t, &continuumv1.Sync{Nodes: []*continuumv1.NodeFacts{{Key: "n", Name: strings.Repeat("x", 3000)}}}))
	save(tooBig, marshalState(t, &continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{{Key: "w", Labels: map[string]string{"a": strings.Repeat("y", 1000), "b": strings.Repeat("y", 1000), "c": strings.Repeat("y", 1000), "d": strings.Repeat("y", 1000), "e": strings.Repeat("y", 1000)}}}}))
	save(garbage, []byte("this is not a protobuf \xff\xff\xff"))

	p := NewPlatform(e.base, nil)
	p.Tune = func(h *Hub) { h.Limits = &small }
	tn, err := p.Tenant(e.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	h := tn.Hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if v := h.views[good]; v == nil || len(v.state.Nodes) != 5 || v.state.Seq != 3 {
		t.Fatalf("a valid snapshot was not restored: %+v", v)
	}
	for name, id := range map[string]string{"too many nodes": tooMany, "long string": longName, "too many bytes": tooBig, "garbage": garbage} {
		if h.views[id] != nil {
			t.Errorf("a snapshot with %s was restored", name)
		}
	}
}

func TestOversizeSnapshotIsNotPersisted(t *testing.T) {
	r := newHubRig(t)
	r.hub.Limits = &facts.Limits{Nodes: 100, Namespaces: 100, Workloads: 100, Bytes: 1 << 10}
	id, _, _ := r.approvedAgent(t, fp)
	v := &view{state: facts.New(), dirtyAt: time.Now()}
	// build a state that is over the cap by going around Check (as a bug or a poisoned store would)
	v.state.Apply(&continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{{Key: "w", Labels: map[string]string{"a": strings.Repeat("z", 1000)}}}, Nodes: nodes("n", 1)})
	for i := 0; i < 30; i++ {
		v.state.Apply(&continuumv1.Sync{Workloads: []*continuumv1.WorkloadFacts{{Key: fmt.Sprint("w", i), Labels: map[string]string{"a": strings.Repeat("z", 1000)}}}})
	}
	r.hub.mu.Lock()
	r.hub.views[id] = v
	r.hub.mu.Unlock()
	r.hub.persist(r.ctx, id, true)
	if _, _, err := r.st.LoadSnapshot(r.ctx, id); err == nil {
		t.Fatal("an over-limit state was written to the database")
	}
}

func TestPoisonedFlowTableIsDroppedOnRestore(t *testing.T) {
	ok := &continuumv1.FlowTable{Edges: []*continuumv1.FlowEdge{edge("ns/Deployment/a", "1.2.3.4")}}
	bad := &continuumv1.FlowTable{Edges: []*continuumv1.FlowEdge{edge(strings.Repeat("k", 5000), "1.2.3.4")}}
	badIP := &continuumv1.FlowTable{Edges: []*continuumv1.FlowEdge{edge("ns/Deployment/a", "127.0.0.1")}}
	for name, tb := range map[string]*continuumv1.FlowTable{"ok": ok, "long ref": bad, "loopback ip": badIP} {
		b, _ := proto.Marshal(tb)
		_, err := loadFlowTable(b)
		if (name == "ok") != (err == nil) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := loadFlowTable(make([]byte, facts.MaxBytes+1)); err == nil {
		t.Error("an oversized flow table was loaded")
	}
}

func edge(ref, ip string) *continuumv1.FlowEdge {
	now := time.Now()
	t := tsProto(&now)
	return &continuumv1.FlowEdge{FirstSeen: t, LastSeen: t, Key: &continuumv1.Flow{Port: 80, Protocol: "tcp",
		Src: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_WORKLOAD, Ref: ref},
		Dst: &continuumv1.FlowEndpoint{Kind: continuumv1.FlowEndpoint_EXTERNAL, Ip: ip}}}
}
