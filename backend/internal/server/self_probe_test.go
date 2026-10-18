package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/probe"
)

// The node probes the server's own chart runs report to a receiver in the server. They make the server's cluster known, with
// its nodes' addresses, without an agent there; an agent that is there is believed over them.

const selfProbeSecret = "0123456789abcdef0123456789abcdef"

// selfProbes starts a receiver on the rig's admin, and returns a function that has a node's probe report to it.
func selfProbes(t *testing.T, a *intentRig, every time.Duration) func(node string, addrs ...*continuumv1.HostAddress) {
	t.Helper()
	rcv := probe.NewReceiver([]byte(selfProbeSecret), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(rcv.Handler())
	t.Cleanup(srv.Close)
	a.a.SelfProbe, a.a.SelfProbeEvery = rcv, every
	return func(node string, addrs ...*continuumv1.HostAddress) {
		t.Helper()
		if _, err := probe.Push(context.Background(), http.DefaultClient, srv.URL, []byte(selfProbeSecret), node, &continuumv1.HostProbe{HostAddresses: addrs}, time.Now()); err != nil {
			t.Fatalf("report from %s: %v", node, err)
		}
	}
}

func candidateSet(t *testing.T, got map[string]any) map[string]bool {
	t.Helper()
	by := map[string]bool{}
	for _, c := range got["candidates"].([]any) {
		by[c.(map[string]any)["address"].(string)] = true
	}
	return by
}

func TestNodeProbesOfTheServersChartListTheNodesWhenNoAgentIsInTheCluster(t *testing.T) {
	a, f, _ := centralRig(t, "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c")
	f.ClusterUID = "11111111-2222-4333-8444-555555555555" // the install's cluster has no agent
	report := selfProbes(t, a, time.Minute)
	report("k3s-1", &continuumv1.HostAddress{Interface: "eth0", Address: "10.1.0.5/24"}, &continuumv1.HostAddress{Interface: "wt0", Kind: "wireguard", Address: "100.64.0.9/10"})

	got := centralCandidates(t, a)
	sc := serverCluster(t, got)
	if sc["state"] != "known" || sc["source"] != "install" || sc["nodes"] != "probe" {
		t.Fatalf("server cluster: %v", sc)
	}
	by := candidateSet(t, got)
	if !by["100.64.0.9:31999"] || !by["10.1.0.5:31999"] {
		t.Fatalf("the probe's mesh and LAN addresses on the node port: %v", got["candidates"])
	}
}

func TestNodeProbesSayWhichClusterTheServerIsInWhenNothingElseDid(t *testing.T) {
	a, _, _ := centralRig(t, "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c") // no identity from the install, nothing matches
	if sc := serverCluster(t, centralCandidates(t, a)); sc["state"] != "unknown" {
		t.Fatalf("before any probe: %v", sc)
	}
	selfProbes(t, a, time.Minute)("k3s-1", &continuumv1.HostAddress{Interface: "eth0", Address: "10.1.0.5/24"})
	sc := serverCluster(t, centralCandidates(t, a))
	if sc["state"] != "known" || sc["source"] != "probe" || sc["nodes"] != "probe" {
		t.Fatalf("server cluster: %v", sc)
	}
}

func TestAnAgentsOwnInterfaceAddressesAreNotReplacedByTheProbes(t *testing.T) {
	const uid = "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c"
	a, f, _ := centralRig(t, uid)
	f.ClusterUID = uid
	report := selfProbes(t, a, time.Minute)
	// The agent already lists nuc-1's interfaces (centralRig); the probe on the same node says something else.
	report("nuc-1", &continuumv1.HostAddress{Interface: "wt0", Kind: "wireguard", Address: "100.64.0.77/10"})
	by := candidateSet(t, centralCandidates(t, a))
	if !by["100.64.0.139:31999"] || by["100.64.0.77:31999"] {
		t.Fatalf("the agent's address wins: %v", by)
	}
}

func TestProbesFillInWhatAnAgentWithoutItsOwnNodeProbeCannotSee(t *testing.T) {
	const uid = "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c"
	a, f, agentID := centralRig(t, uid)
	f.ClusterUID = uid
	a.hub().Mu.Lock()
	a.hub().Views[agentID].State.Nodes["nuc-1"].Probe = nil // the agent's chart runs no node probe
	a.hub().Mu.Unlock()
	selfProbes(t, a, time.Minute)("nuc-1", &continuumv1.HostAddress{Interface: "wt0", Kind: "wireguard", Address: "100.64.0.77/10"})
	sc := serverCluster(t, centralCandidates(t, a))
	if sc["nodes"] != "agent" {
		t.Fatalf("an agent lists the nodes: %v", sc)
	}
	if by := candidateSet(t, centralCandidates(t, a)); !by["100.64.0.77:31999"] || !by["10.0.0.7:31999"] {
		t.Fatalf("the agent's internal address and the probe's mesh one: %v", by)
	}
}

func TestAProbeThatWentQuietIsNoLongerBelieved(t *testing.T) {
	a, f, _ := centralRig(t, "3f2b6c1e-9a4d-4e7b-8c55-0d1e2f3a4b5c")
	f.ClusterUID = "11111111-2222-4333-8444-555555555555"
	selfProbes(t, a, 5*time.Millisecond)("k3s-1", &continuumv1.HostAddress{Interface: "eth0", Address: "10.1.0.5/24"})
	time.Sleep(60 * time.Millisecond) // three silent intervals is 15ms
	got := centralCandidates(t, a)
	if sc := serverCluster(t, got); sc["state"] != "noAgent" {
		t.Fatalf("server cluster: %v", sc)
	}
	for addr := range candidateSet(t, got) {
		if strings.HasPrefix(addr, "10.1.0.5") {
			t.Fatalf("a stale report is listed: %v", got["candidates"])
		}
	}
}

func TestAReportWithTheWrongSecretIsRefused(t *testing.T) {
	rcv := probe.NewReceiver([]byte(selfProbeSecret), slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(rcv.Handler())
	defer srv.Close()
	_, err := probe.Push(context.Background(), http.DefaultClient, srv.URL, []byte("some-other-secret-entirely-0000"), "k3s-1", &continuumv1.HostProbe{}, time.Now())
	if err == nil || len(rcv.Snapshot(time.Hour)) != 0 {
		t.Fatalf("an unsigned-by-us report must not be kept: %v", err)
	}
}
