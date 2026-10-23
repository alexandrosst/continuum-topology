package agent

import (
	"os"
	"slices"
	"strings"
	"testing"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
	"ikhnos/internal/agentcaps"
	"ikhnos/internal/link"
)

func TestTheHelloStatesTheProtocolVersionAndEveryCapabilityTheRegistryHolds(t *testing.T) {
	r := newTestRunner()
	r.cfg = Config{Version: "v9", Tier: 2, Namespace: "ns", ReleaseName: "rel"}
	h := r.hello()
	if h.ProtocolVersion != 1 {
		t.Fatalf("protocol version %d: the first version that states capabilities is 1", h.ProtocolVersion)
	}
	if !slices.Equal(h.Capabilities, agentcaps.Names()) {
		t.Fatalf("capabilities %v, the registry holds %v", h.Capabilities, agentcaps.Names())
	}
	// What the hello said before capabilities existed is still said, unchanged.
	if h.AgentVersion != "v9" || h.InstalledAccessTier != 2 || h.Namespace != "ns" || h.ReleaseName != "rel" || h.Diagnostics == nil {
		t.Fatalf("the older fields moved: %+v", h)
	}
}

// A name in the registry is a promise that the agent does the thing. Each name has a probe here that calls the code that does it,
// so a name cannot be added to the registry (or kept after the code is gone) without this test being told. A capability that can
// only be shown with a server on the other end is proved end to end, in the test named in e2eProofs.
var capabilityProbes = map[string]func(t *testing.T){
	agentcaps.Diagnostics: func(t *testing.T) {
		r := newTestRunner()
		r.cfg = Config{Tier: 2}
		d := r.hello().Diagnostics
		if d == nil || d.InstalledTier != 2 {
			t.Fatalf("the hello does not carry the agent's account of itself: %+v", d)
		}
	},
	agentcaps.ReleaseInfo: func(t *testing.T) {
		r := newTestRunner()
		r.cfg = Config{Namespace: "shop-system", ReleaseName: "ikhnos"}
		if h := r.hello(); h.Namespace != "shop-system" || h.ReleaseName != "ikhnos" {
			t.Fatalf("the hello does not carry the release: %q %q", h.Namespace, h.ReleaseName)
		}
	},
	agentcaps.SelfStats: func(t *testing.T) {
		if s := newTestRunner().selfStats(); s == nil || s.Goroutines == 0 {
			t.Fatalf("no self stats: %+v", s)
		}
	},
	agentcaps.LinkCapabilities: func(t *testing.T) {
		r := &runner{cfg: Config{Links: link.NewMeter(link.NewPeers())}}
		if c := r.linkCapabilities(); c == nil || !c.LinkMetrics {
			t.Fatalf("no link capabilities: %+v", c)
		}
	},
	agentcaps.ChunkedSync: func(t *testing.T) {
		big := &ikhnosv1.Sync{Seq: 1, Full: true}
		for i := 0; i < 50; i++ {
			big.Workloads = append(big.Workloads, &ikhnosv1.WorkloadFacts{Key: strings.Repeat("k", 40) + string(rune('a'+i%26)), Name: "w", Namespace: "ns", Kind: "Deployment"})
		}
		if n := len(splitSync(big, 256)); n < 2 {
			t.Fatalf("a large picture was sent in %d message(s)", n)
		}
	},
}

// e2eProofs names, for each capability that needs a server to show it, the end-to-end test that does.
var e2eProofs = map[string]string{
	agentcaps.ConfigDigest: "TestTheAgentEchoesTheDigestOfTheConfigItApplied",
}

func TestEveryDeclaredCapabilityIsImplemented(t *testing.T) {
	declared := agentcaps.Names()
	for _, n := range declared {
		_, probed := capabilityProbes[n]
		proof, proved := e2eProofs[n]
		switch {
		case probed == proved:
			t.Errorf("%q needs exactly one proof (a probe here or an end-to-end test): probe %v, e2e %v", n, probed, proved)
		case probed:
			t.Run(n, capabilityProbes[n])
		case proved:
			src, err := os.ReadFile("diag_e2e_test.go")
			if err != nil || !strings.Contains(string(src), "func "+proof+"(") {
				t.Errorf("%q is proved by %s, which is not in diag_e2e_test.go (%v)", n, proof, err)
			}
		}
	}
	for n := range capabilityProbes {
		if !slices.Contains(declared, n) {
			t.Errorf("a probe for %q, which the registry does not hold", n)
		}
	}
	for n := range e2eProofs {
		if !slices.Contains(declared, n) {
			t.Errorf("an end-to-end proof for %q, which the registry does not hold", n)
		}
	}
}
