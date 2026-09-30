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
