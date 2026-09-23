package interpret

import "testing"

type nodeAndKind struct {
	n *N
	k string
}

func pair(key, kind string) nodeAndKind { return nodeAndKind{node(key, nil), kind} }

func withKinds(pairs ...nodeAndKind) ([]*N, map[string]string) {
	nodes := make([]*N, len(pairs))
	kinds := map[string]string{}
	for i, p := range pairs {
		nodes[i] = p.n
		kinds[p.n.Key] = p.k
	}
	return nodes, kinds
}

func TestTierForCloudProviders(t *testing.T) {
	for _, provider := range []string{"AWS", "Google Cloud", "Azure", "DigitalOcean", "Linode", "Scaleway", "OVHcloud", "Hetzner Cloud"} {
		tier, ev := tierFor(provider, nil, nil)
		if tier != "cloud" {
			t.Errorf("%s: tier = %s, want cloud", provider, tier)
		}
		if ev.Confidence == "" {
			t.Errorf("%s: expected evidence", provider)
		}
	}
	// Hetzner's dedicated-server line is not a cloud API: renting a physical box doesn't make it one.
	if tier, _ := tierFor("Hetzner Robot", nil, nil); tier == "cloud" {
		t.Error("Hetzner Robot (dedicated servers) should not be classified as cloud")
	}
}

func TestTierForFarEdgeUnanimous(t *testing.T) {
	nodes, kinds := withKinds(pair("a", "edge-device"), pair("b", "edge-device"), pair("c", "edge-device"))
	tier, ev := tierFor("On-prem", nodes, kinds)
	if tier != "far-edge" {
		t.Errorf("tier = %s, want far-edge", tier)
	}
	if ev.Confidence != "medium" {
		t.Errorf("confidence = %s, want medium when every node agrees", ev.Confidence)
	}
}

func TestTierForFarEdgeMajorityWithControlPlaneBox(t *testing.T) {
	// Five Pis and one x86 control-plane box that fell back to the low-confidence default kind: a
	// realistic small far-edge lab, not a cloud/DC cluster, and shouldn't be swamped by one node.
	nodes, kinds := withKinds(pair("a", "edge-device"), pair("b", "edge-device"), pair("c", "edge-device"), pair("d", "edge-device"), pair("e", "edge-device"), pair("f", "vm"))
	tier, ev := tierFor("On-prem", nodes, kinds)
	if tier != "far-edge" {
		t.Errorf("tier = %s, want far-edge (5 of 6 nodes are edge devices)", tier)
	}
	if ev.Confidence != "low" {
		t.Errorf("confidence = %s, want low when not every node agrees", ev.Confidence)
	}
}

func TestTierForUnknownKindNodesDoNotBlockFarEdge(t *testing.T) {
	// A node the probe never ran on (kind unknown/"") is left out of the count entirely, not treated as
	// a bare-metal/vm counterexample.
	nodes, kinds := withKinds(pair("a", "edge-device"), pair("b", "edge-device"), pair("c", ""))
	tier, _ := tierFor("On-prem", nodes, kinds)
	if tier != "far-edge" {
		t.Errorf("tier = %s, want far-edge (the only known kinds are edge-device)", tier)
	}
}

func TestTierForDefaultsToEdge(t *testing.T) {
	nodes, kinds := withKinds(pair("a", "vm"), pair("b", "bare-metal"))
	tier, ev := tierFor("On-prem", nodes, kinds)
	if tier != "edge" {
		t.Errorf("tier = %s, want edge", tier)
	}
	if ev.Confidence != "low" {
		t.Errorf("confidence = %s, want low - this is the shared, honestly-uncertain default", ev.Confidence)
	}
}

func TestTierForNoNodesDefaultsToEdge(t *testing.T) {
	if tier, _ := tierFor("On-prem", nil, nil); tier != "edge" {
		t.Errorf("tier = %s, want edge", tier)
	}
}

func TestDetectProviderSplitsHetznerCloudFromRobot(t *testing.T) {
	cloud, _ := detectProvider([]*N{node("a", func(n *N) { n.ProviderId = "hcloud://123" })})
	if cloud != "Hetzner Cloud" {
		t.Errorf("provider = %s, want Hetzner Cloud", cloud)
	}
	robot, _ := detectProvider([]*N{node("a", func(n *N) { n.ProviderId = "hrobot://456" })})
	if robot != "Hetzner Robot" {
		t.Errorf("provider = %s, want Hetzner Robot", robot)
	}
}
