package server

import (
	"testing"

	"continuum/internal/model"
)

func nodeWithTunnel(id, name string, tuns ...model.TunnelInterface) model.Node {
	return model.Node{ID: id, Name: name, Tunnels: tuns}
}

func TestCorrelateTunnelsConfirmsAReciprocalPair(t *testing.T) {
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "node-b" {
		t.Errorf("node-a's tunnel Confirmed = %q, want node-b", nodes[0].Tunnels[0].Confirmed)
	}
	if nodes[1].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("node-b's tunnel Confirmed = %q, want node-a", nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsLeavesAOneSidedTunnelUnconfirmed(t *testing.T) {
	// Only one side is visible in this topology (the other end is outside every onboarded cluster, a
	// completely ordinary case) - it must stay unconfirmed, not be guessed into a link that was never
	// actually corroborated.
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a}
	correlateTunnels(nodes)
	if got := nodes[0].Tunnels[0].Confirmed; got != "" {
		t.Errorf("Confirmed = %q, want empty (nothing corroborates it)", got)
	}
}

func TestCorrelateTunnelsRequiresAgreementInBothDirections(t *testing.T) {
	// node-a's tunnel address falls inside node-b's routed prefix, but node-b's own tunnel address does
	// NOT fall inside anything node-a routes - a one-way match is not enough to call it confirmed.
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"192.168.99.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.9.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "" || nodes[1].Tunnels[0].Confirmed != "" {
		t.Errorf("got %q / %q, want both empty (agreement is one-directional only)", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsNeverMatchesTwoTunnelsOnTheSameNode(t *testing.T) {
	a := nodeWithTunnel("a", "node-a",
		model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.9.0.0/24"}},
		model.TunnelInterface{Name: "wg1", Kind: "wireguard", Addresses: []string{"10.9.0.1/24"}, Routes: []string{"10.8.0.0/24"}},
	)
	nodes := []model.Node{a}
	correlateTunnels(nodes)
	for i, tu := range nodes[0].Tunnels {
		if tu.Confirmed != "" {
			t.Errorf("tunnel %d Confirmed = %q, want empty (a node cannot corroborate its own tunnel)", i, tu.Confirmed)
		}
	}
}

// TestCorrelateTunnelsRejectsCoincidentalOverlapOnABroadSharedSupernet pins a real fix: two unrelated
// tunnels that have never exchanged a packet, each independently routing the whole 10.0.0.0/8 (an
// entirely ordinary "route everything on this VPN" tunnel config, and a very commonly reused private
// range), used to confirm each other purely because their own /32-ish addresses happened to both fall
// inside that shared broad supernet - a false positive that directly contradicted this function's own
// "never fabricated" promise. A real point-to-point tunnel routes something specific; /8 is not that.
func TestCorrelateTunnelsRejectsCoincidentalOverlapOnABroadSharedSupernet(t *testing.T) {
	a := nodeWithTunnel("a", "customer-A-gateway", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.1.2.3/32"}, Routes: []string{"10.0.0.0/8"},
	})
	b := nodeWithTunnel("b", "customer-B-gateway", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.9.8.7/32"}, Routes: []string{"10.0.0.0/8"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "" || nodes[1].Tunnels[0].Confirmed != "" {
		t.Errorf("got %q / %q, want both empty - a shared /8 is not specific evidence of a real link", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

// TestCorrelateTunnelsStillConfirmsASpecificEnoughRoute makes sure the new specificity floor doesn't
// overcorrect: a realistic tunnel subnet (/24, well above the /16 floor) must still confirm normally.
func TestCorrelateTunnelsStillConfirmsASpecificEnoughRoute(t *testing.T) {
	a := nodeWithTunnel("a", "node-a", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"},
	})
	b := nodeWithTunnel("b", "node-b", model.TunnelInterface{
		Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"},
	})
	nodes := []model.Node{a, b}
	correlateTunnels(nodes)
	if nodes[0].Tunnels[0].Confirmed != "node-b" || nodes[1].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("a /24 route should still confirm normally: got %q / %q", nodes[0].Tunnels[0].Confirmed, nodes[1].Tunnels[0].Confirmed)
	}
}

func TestCorrelateTunnelsFindsAMatchAcrossManyNodes(t *testing.T) {
	nodes := []model.Node{
		nodeWithTunnel("x", "noise-1", model.TunnelInterface{Name: "gre0", Kind: "gre", Addresses: []string{"192.0.2.1/30"}, Routes: []string{"198.51.100.0/24"}}),
		nodeWithTunnel("a", "node-a", model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.1/24"}, Routes: []string{"10.8.0.0/24"}}),
		nodeWithTunnel("y", "noise-2"),
		nodeWithTunnel("b", "node-b", model.TunnelInterface{Name: "wg0", Kind: "wireguard", Addresses: []string{"10.8.0.2/24"}, Routes: []string{"10.8.0.0/24"}}),
	}
	correlateTunnels(nodes)
	if nodes[1].Tunnels[0].Confirmed != "node-b" || nodes[3].Tunnels[0].Confirmed != "node-a" {
		t.Errorf("node-a/node-b did not confirm each other: %q / %q", nodes[1].Tunnels[0].Confirmed, nodes[3].Tunnels[0].Confirmed)
	}
}
