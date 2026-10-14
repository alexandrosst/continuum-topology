package agent_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/link"
	"continuum/internal/server"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

// addPeer starts a second agent (another cluster, with one node at nodeIP) against d's server and approves it at tier 2.
// It returns that cluster's id.
func (d *dx) addPeer(uid, nodeIP string) string {
	d.t.Helper()
	q := resource.MustParse
	kube := fake.NewSimpleClientset([]runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID("d1a90002-aaaa-4bbb-8ccc-" + uid)}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "p1"}, Status: corev1.NodeStatus{Capacity: corev1.ResourceList{"cpu": q("2"), "memory": q("4Gi")},
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: nodeIP}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"}},
	}...)
	ids := agent.FileStore{Dir: filepath.Join(d.t.TempDir(), "id")}
	token, _, err := d.core.CreateToken(context.Background(), "test", "peer", 2)
	if err != nil {
		d.t.Fatal(err)
	}
	cfg := d.cfg
	cfg.Kube, cfg.Identity, cfg.Token = kube, ids, token
	cfg.Probes, cfg.Flows, cfg.Links = nil, nil, nil
	ctx, cancel := context.WithCancel(context.Background())
	over := make(chan struct{})
	go func() { _ = agent.Run(ctx, cfg); close(over) }()
	d.t.Cleanup(func() {
		cancel()
		select {
		case <-over:
		case <-time.After(20 * time.Second):
		}
	})
	var peer string
	waitFor(d.t, "the peer cluster's agent", 20*time.Second, func() bool {
		for _, a := range d.state().Agents {
			if a.ID != d.id && a.Status == "pending" {
				peer = a.ID
				return true
			}
		}
		return false
	})
	if err := d.core.Approve(context.Background(), "test", peer, codeOf(d.t, ids), 2); err != nil {
		d.t.Fatal(err)
	}
	var cluster string
	waitFor(d.t, "the peer cluster to be known", 30*time.Second, func() bool {
		for _, a := range d.state().Agents {
			if a.ID == peer && a.ClusterID != "" && a.Status == "approved" {
				cluster = a.ClusterID
				return true
			}
		}
		return false
	})
	// The server names a peer's addresses from what its agent has reported, so wait until it has reported its node.
	waitFor(d.t, "the peer cluster's node to be reported", 30*time.Second, func() bool {
		for _, n := range d.state().Topology.Nodes {
			if n.IP == nodeIP {
				return true
			}
		}
		return false
	})
	return cluster
}

func metricsOf(m *link.Meter) string {
	var b bytes.Buffer
	m.Write(&b)
	return b.String()
}

func TestTheServerNamesLinkPeersAndTheAgentLabelsTrafficWithThem(t *testing.T) {
	secret := []byte("secret-secret-secret-secret-1234")
	meter := link.NewMeter(link.NewPeers())
	var pipe *flow.Pipeline
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		pipe = flow.NewPipeline(secret, func() *collect.Index { return nil }, nil)
		pipe.Links = meter
		c.Flows, c.Links = pipe, meter
	}})
	waitFor(t, "first picture", 20*time.Second, func() bool { return services(d, "shop") == 1 })
	mine := d.agentDoc().ClusterID
	peer := d.addPeer("eeeeeeeeeeee", "10.9.9.9")

	// Nothing is measured until a pair is switched on.
	time.Sleep(500 * time.Millisecond)
	if !meter.Peers.Empty() {
		t.Fatalf("peers were named before any pair was enabled: %v", meter.Peers.List())
	}

	if _, err := d.core.SaveSettings(context.Background(), "test", server.Settings{Links: []server.LinkSetting{{A: mine, B: peer}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the peer list to reach the agent", 30*time.Second, func() bool { return !meter.Peers.Empty() })
	ps := meter.Peers.List()
	if len(ps) != 1 || ps[0].ID != peer || meter.Peers.Own() != mine || meter.Peers.Lookup("10.9.9.9") != peer {
		t.Fatalf("own %q peers %+v", meter.Peers.Own(), ps)
	}

	// Traffic seen to the peer's node is labelled with the pair; traffic anywhere else is not.
	pipe.Ingest(&continuumv1.FlowReport{Method: "ebpf", Node: "n1", BytesKnown: true, Flows: []*continuumv1.RawFlow{
		{Client: true, LocalIp: "10.0.0.5", PeerIp: "10.9.9.9", Port: 443, Protocol: "tcp", Connections: 3, BytesOut: 5000, BytesIn: 200, SegsOut: 40, Retransmits: 2, RttUs: 25000},
		{Client: true, LocalIp: "10.0.0.5", PeerIp: "8.8.8.8", Port: 443, Protocol: "tcp", Connections: 9, BytesOut: 1},
	}})
	out := metricsOf(meter)
	for _, want := range []string{
		`continuum_link_connections_total{cluster="` + mine + `",peer="` + peer + `"} 3`,
		`continuum_link_retransmits_total{cluster="` + mine + `",peer="` + peer + `"} 2`,
		`continuum_link_sent_bytes_total{cluster="` + mine + `",peer="` + peer + `"} 5000`,
		`continuum_link_rtt_seconds_count{cluster="` + mine + `",peer="` + peer + `"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "8.8.8.8") {
		t.Error("an address leaked into the metrics")
	}

	// Pausing the flow collector for this agent stops it: no peers, no series.
	if _, err := d.hub.SetConsent(context.Background(), "test", d.id, server.Consent{Paused: []string{"flow"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the peers to be withdrawn while flow is paused", 30*time.Second, func() bool { return meter.Peers.Empty() })
	if _, err := d.hub.SetConsent(context.Background(), "test", d.id, server.Consent{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the peers to return", 30*time.Second, func() bool { return !meter.Peers.Empty() })

	// Removing the pair removes the peer and its series.
	if _, err := d.core.SaveSettings(context.Background(), "test", server.Settings{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the peer to be withdrawn", 30*time.Second, func() bool { return meter.Peers.Empty() })
	if got := metricsOf(meter); strings.Contains(got, peer) {
		t.Errorf("series for a removed peer remain:\n%s", got)
	}
}

// What the install can do reaches the server in the heartbeat, and the checklist of a pair is built from it: this install has
// Cluster links, probing and telemetry export on, the other agent (a plain one) has Cluster links off, and both are said so.
func TestTheServerLearnsWhatTheInstallCanDoAndChecksThePair(t *testing.T) {
	meter := link.NewMeter(link.NewPeers())
	meter.SetInstall(link.Install{Probe: true, TelemetryExport: true})
	d := newDx(t, dxOpts{tune: func(c *agent.Config, _ *server.Core, _ *server.Hub) {
		pipe := flow.NewPipeline([]byte("secret-secret-secret-secret-1234"), func() *collect.Index { return nil }, nil)
		pipe.Links = meter
		c.Flows, c.Links = pipe, meter
	}})
	waitFor(t, "first picture", 20*time.Second, func() bool { return services(d, "shop") == 1 })
	mine := d.agentDoc().ClusterID
	peer := d.addPeer("eeeeeeeeeeee", "10.9.9.9") // an agent whose install has no Cluster links
	links := []server.LinkSetting{{A: mine, B: peer}}

	steps := func() map[string]string {
		_, setup := d.hub.LinkReport(context.Background(), links, true)
		out := map[string]string{}
		if len(setup) == 1 {
			for _, s := range setup[0].Steps {
				out[s.Code+"/"+s.Cluster] = s.State
			}
		}
		return out
	}
	// each agent's report arrives with its first heartbeat
	waitFor(t, "both agents' capabilities to be known", 30*time.Second, func() bool {
		s := steps()
		return s["links-on/"+mine] != "" && s["links-on/"+peer] != ""
	})
	got := steps()
	if got["links-on/"+mine] != "ok" {
		t.Errorf("Cluster links and probing are on in this install: %v", got)
	}
	if got["links-on/"+peer] != "todo" {
		t.Errorf("Cluster links is off in the peer's install: %v", got)
	}
	if got["capabilities/"+mine] != "" || got["capabilities/"+peer] != "" {
		t.Errorf("both agents reported, so neither is unknown: %v", got)
	}
	// the prober has never asked this agent for its peers
	if got["prober/"+mine] != "" && got["prober/"+mine] != "todo" {
		t.Errorf("the prober never asked: %v", got)
	}
}
