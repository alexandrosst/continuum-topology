package connectivity

import (
	"fmt"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agents"
	"continuum/internal/measure"
	"google.golang.org/protobuf/proto"
)

func baseFacts() ConfigFacts {
	return ConfigFacts{
		Tier: 2, ClusterID: "cl-a", Heartbeat: 30, Resync: 600, ServerTime: 1700000000, MeasureSeconds: 20,
		Peers: []*continuumv1.LinkPeer{
			{ClusterId: "cl-b", Name: "b", Addresses: []string{"10.0.0.2"}, Throughput: true},
			{ClusterId: "cl-c", Name: "c", Addresses: []string{"10.0.0.3"}},
		},
		IperfClients:   map[string]*continuumv1.LinkIperfClient{"cl-b": {Username: "u", Password: "p"}, "cl-c": {Username: "x", Password: "y"}},
		IperfServer:    &continuumv1.LinkIperfServer{UsersCsv: "u:p"},
		ManualTargets:  []IssuedTarget{{ID: "t-man", Host: "203.0.113.1", Port: 443, Source: "manual"}},
		AddressTargets: []IssuedTarget{{ID: "t-addr", Host: "203.0.113.2", Port: 80}},
		DerivedTargets: []IssuedTarget{{ID: "ob-1", Host: "203.0.113.3", Port: 8443, Source: "observed"}},
	}
}

func targetIDs(cfg *continuumv1.Config) []string {
	var ids []string
	for _, t := range cfg.ProbeTargets {
		ids = append(ids, t.Id)
	}
	return ids
}

func TestCompileConfigCarriesWhatTheFactsSayAndADigestOfIt(t *testing.T) {
	cfg, ts := CompileConfig(baseFacts())
	if cfg.ApprovedAccessTier != 2 || cfg.HeartbeatSeconds != 30 || cfg.ResyncSeconds != 600 || cfg.ServerTimeUnix != 1700000000 || cfg.ClusterId != "cl-a" {
		t.Fatalf("the plain fields are wrong: %+v", cfg)
	}
	if cfg.Digest == "" || cfg.Digest != ConfigHash(cfg) {
		t.Fatalf("the digest %q is not the hash of the Config", cfg.Digest)
	}
	if got := fmt.Sprint(targetIDs(cfg)); got != "[t-man t-addr ob-1]" || len(ts) != 3 || cfg.MeasureSeconds != 20 {
		t.Fatalf("targets %s (issued %d), measure %d", got, len(ts), cfg.MeasureSeconds)
	}
}

func TestCompileConfigIssuesObservedTargetsOnlyFromTierTwo(t *testing.T) {
	for tier, want := range map[int]string{0: "[t-man t-addr]", 1: "[t-man t-addr]", 2: "[t-man t-addr ob-1]"} {
		f := baseFacts()
		f.Tier = tier
		cfg, ts := CompileConfig(f)
		if got := fmt.Sprint(targetIDs(cfg)); got != want || len(ts) != len(cfg.ProbeTargets) {
			t.Errorf("tier %d: %s (issued %d), want %s", tier, got, len(ts), want)
		}
	}
}

func TestCompileConfigIssuesNothingWhenMeasuringIsPausedAndNoMeasureTimeWithoutTargets(t *testing.T) {
	f := baseFacts()
	f.Consent = agents.Consent{Paused: []string{"measure"}}
	cfg, ts := CompileConfig(f)
	if len(cfg.ProbeTargets) != 0 || len(ts) != 0 || cfg.MeasureSeconds != 0 {
		t.Fatalf("a paused measure collector was still given targets: %v %d", targetIDs(cfg), cfg.MeasureSeconds)
	}
	if fmt.Sprint(cfg.PausedCollectors) != "[measure]" {
		t.Fatalf("the pause itself is not carried: %v", cfg.PausedCollectors)
	}
	f = baseFacts()
	f.ManualTargets, f.AddressTargets, f.DerivedTargets = nil, nil, nil
	if cfg, _ := CompileConfig(f); cfg.MeasureSeconds != 0 {
		t.Fatalf("a measure time with nothing to measure: %d", cfg.MeasureSeconds)
	}
}

func TestCompileConfigIssuesAtMostTheLimitOfTargetsInOrder(t *testing.T) {
	f := baseFacts()
	f.ManualTargets, f.AddressTargets, f.DerivedTargets = nil, nil, nil
	for i := 0; i < measure.MaxTargets+5; i++ {
		f.AddressTargets = append(f.AddressTargets, IssuedTarget{ID: fmt.Sprintf("t-%02d", i), Host: "203.0.113.9", Port: 80})
	}
	cfg, ts := CompileConfig(f)
	if len(cfg.ProbeTargets) != measure.MaxTargets || len(ts) != measure.MaxTargets || cfg.ProbeTargets[0].Id != "t-00" {
		t.Fatalf("%d targets, first %v", len(cfg.ProbeTargets), cfg.ProbeTargets[0])
	}
}

func TestCompileConfigHandsTheIperfServerOnlyToTierTwoWithFlowRunning(t *testing.T) {
	for _, c := range []struct {
		name    string
		tier    int
		consent agents.Consent
		want    bool
	}{{"tier 2", 2, agents.Consent{}, true}, {"tier 1", 1, agents.Consent{}, false}, {"flow paused", 2, agents.Consent{Paused: []string{"flow"}}, false}} {
		f := baseFacts()
		f.Tier, f.Consent = c.tier, c.consent
		if cfg, _ := CompileConfig(f); (cfg.LinkIperfServer != nil) != c.want {
			t.Errorf("%s: server credentials present = %v, want %v", c.name, cfg.LinkIperfServer != nil, c.want)
		}
	}
}

func TestCompileConfigHandsClientCredentialsOnlyToThePeersItTestsForThroughput(t *testing.T) {
	cfg, _ := CompileConfig(baseFacts())
	if cfg.LinkPeers[0].IperfClient.GetUsername() != "u" {
		t.Fatalf("the throughput peer has no client credentials: %v", cfg.LinkPeers[0])
	}
	if cfg.LinkPeers[1].IperfClient != nil {
		t.Fatalf("a peer that is not tested for throughput was given credentials: %v", cfg.LinkPeers[1])
	}
}

func TestCompileConfigChangesNothingItIsGiven(t *testing.T) {
	f := baseFacts()
	f.Consent = agents.Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}
	before := make([]*continuumv1.LinkPeer, len(f.Peers))
	for i, p := range f.Peers {
		before[i] = proto.Clone(p).(*continuumv1.LinkPeer)
	}
	cfg, _ := CompileConfig(f)
	for i, p := range f.Peers {
		if !proto.Equal(p, before[i]) {
			t.Errorf("peer %d was changed: %v", i, p)
		}
	}
	cfg.PausedCollectors[0] = "changed"
	cfg.ExcludedNamespaces[0] = "changed"
	if f.Consent.Paused[0] != "flow" || f.Consent.Excluded[0] != "shop" {
		t.Fatal("the Config shares the consent's slices")
	}
}
