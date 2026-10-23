package estate

import (
	"context"
	"crypto/tls"
	"fmt"
	"strings"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/facts"
	"ikhnos/internal/pki"
	"ikhnos/internal/testkit"

	"google.golang.org/grpc/codes"
)

func TestSyncMustMatchTheApprovedClusterAndStayInsideItsTier(t *testing.T) {
	r := newHubRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, key, leaf := r.approvedAgent(t, testkit.FP)
	// Pretend the agent last connected from another address: that must be audited, not blocked.
	_ = r.st.Touch(ctx, "org-1", id, "203.0.113.7", "", "", *r.now)

	cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
	connect := func() ikhnosv1.AgentService_ConnectClient {
		c := ikhnosv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(r.core.CA.Pin(), "127.0.0.1", cert)))
		s, err := c.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Hello{Hello: &ikhnosv1.Hello{AgentVersion: "t"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Recv(); err != nil { // config
			t.Fatal(err)
		}
		return s
	}

	// A sync claiming another cluster is refused and the stream ends.
	s := connect()
	_ = s.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Sync{Sync: &ikhnosv1.Sync{Seq: 1, Full: true,
		Cluster: &ikhnosv1.ClusterFacts{Uid: "11111111-2222-4333-8444-555555555555"},
		Nodes:   []*ikhnosv1.NodeFacts{{Key: "n1", Name: "n1"}}}}})
	if _, err := s.Recv(); testkit.StatusCode(err) != codes.PermissionDenied {
		t.Fatalf("foreign cluster uid: %v", err)
	}
	if st, _ := r.hub.State(ctx); len(st.Topology.Nodes) != 0 {
		t.Fatal("facts from the wrong cluster were kept")
	}

	// A sync with too many items is refused.
	s = connect()
	big := make([]*ikhnosv1.NodeFacts, maxNodes+1)
	for i := range big {
		big[i] = &ikhnosv1.NodeFacts{Key: fmt.Sprint(i)}
	}
	_ = s.Send(&ikhnosv1.AgentMessage{Msg: &ikhnosv1.AgentMessage_Sync{Sync: &ikhnosv1.Sync{Seq: 1, Full: true, Cluster: &ikhnosv1.ClusterFacts{Uid: testkit.FP}, Nodes: big}}})
	if _, err := s.Recv(); testkit.StatusCode(err) != codes.InvalidArgument {
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
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)
	a.AccessTier = 0
	r.hub.Mu.Lock()
	r.hub.Views[id] = &View{EstView: EstView{State: facts.New()}}
	r.hub.Mu.Unlock()
	r.hub.ApplySync(a, &ikhnosv1.Sync{Full: true,
		Cluster: &ikhnosv1.ClusterFacts{Uid: testkit.FP, StorageClasses: []string{"gp3"}, IngressClasses: []string{"nginx"}},
		Nodes:   []*ikhnosv1.NodeFacts{{Key: "n", Name: "n"}}}, false)
	st := r.hub.Views[id].State
	if len(st.Nodes) != 0 || len(st.Cluster.StorageClasses) != 0 || len(st.Cluster.IngressClasses) != 0 {
		t.Fatalf("tier 0 kept tier 1 facts: %+v", st.Cluster)
	}
}

// Tier 1 approval covers nodes but not pods, so pod-derived node figures must be unknown, not zero and not
// whatever a tier-2 agent happened to send.
func TestTierOneDropsPodDerivedNodeFigures(t *testing.T) {
	r := newHubRig(t)
	id, _, _ := r.approvedAgent(t, testkit.FP)
	a, _ := r.st.GetAgent(r.ctx, "org-1", id)
	a.AccessTier = 1
	r.hub.Mu.Lock()
	r.hub.Views[id] = &View{EstView: EstView{State: facts.New()}}
	r.hub.Mu.Unlock()
	cnt := int32(12)
	r.hub.ApplySync(a, &ikhnosv1.Sync{Full: true,
		Cluster:   &ikhnosv1.ClusterFacts{Uid: testkit.FP},
		Nodes:     []*ikhnosv1.NodeFacts{{Key: "n", Name: "n", PodCapacity: 110, PodCount: &cnt, CpuRequestedMillis: 900, MemoryRequestedBytes: 1 << 30}},
		Workloads: []*ikhnosv1.WorkloadFacts{{Key: "ns/Deployment/x", VolumeClaims: []*ikhnosv1.VolumeClaim{{Name: "v"}}}}}, false)
	st := r.hub.Views[id].State
	n := st.Nodes["n"]
	if n == nil || n.PodCapacity != 110 || n.PodCount != nil || n.CpuRequestedMillis != 0 || n.MemoryRequestedBytes != 0 {
		t.Fatalf("tier 1 node = %+v", n)
	}
	if len(st.Workloads) != 0 {
		t.Fatal("tier 1 kept workloads")
	}
}

// What is merely too big is cut, not refused: a refusal ends the agent's stream and the cluster never gets in.
func TestValidateSyncCutsOversizedFacts(t *testing.T) {
	claims := make([]*ikhnosv1.VolumeClaim, facts.MaxRepeated+1)
	for i := range claims {
		claims[i] = &ikhnosv1.VolumeClaim{Name: "v"}
	}
	w := &ikhnosv1.WorkloadFacts{Key: "k", VolumeClaims: claims, Hosts: make([]string, facts.MaxRepeated+1),
		Autoscaler: &ikhnosv1.Autoscaler{Targets: []string{strings.Repeat("a", facts.MaxString+1)}}}
	cl := &ikhnosv1.ClusterFacts{Scope: &ikhnosv1.ScopeFacts{Description: strings.Repeat("ns,", 1000)}}
	s := &ikhnosv1.Sync{Cluster: cl, Workloads: []*ikhnosv1.WorkloadFacts{w}, DeletedNodes: []string{strings.Repeat("n", facts.MaxString+1)}}
	if err := validateSync(s); err != nil {
		t.Fatalf("an oversized cluster was refused: %v", err)
	}
	if len(w.VolumeClaims) != facts.MaxRepeated || len(w.Hosts) != facts.MaxRepeated || len(w.Autoscaler.Targets[0]) > facts.MaxString || len(cl.Scope.Description) > facts.MaxString || len(s.DeletedNodes) != 0 {
		t.Errorf("not cut: %d claims, %d hosts, %d target bytes, %d description bytes, %d deleted keys", len(w.VolumeClaims), len(w.Hosts), len(w.Autoscaler.Targets[0]), len(cl.Scope.Description), len(s.DeletedNodes))
	}
	// What cannot be cut is refused: a fact that names nothing.
	for name, bad := range map[string]*ikhnosv1.Sync{
		"no key":   {Workloads: []*ikhnosv1.WorkloadFacts{{}}},
		"long key": {Nodes: []*ikhnosv1.NodeFacts{{Key: strings.Repeat("k", maxStr+1)}}},
	} {
		if validateSync(bad) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
