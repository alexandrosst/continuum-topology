package server

import (
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

func TestTheConfigHashChangesWithThePeers(t *testing.T) {
	base := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a"}
	h1 := ConfigHash(base)
	withPeer := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "beta", Addresses: []string{"10.1.0.1"}}}}
	h2 := ConfigHash(withPeer)
	moved := &continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{{ClusterId: "cl-b", Name: "beta", Addresses: []string{"10.1.0.2"}}}}
	if h1 == h2 || h2 == ConfigHash(moved) {
		t.Fatal("a change in the peer list does not change the config hash, so it would never be pushed")
	}
	if ConfigHash(withPeer) != h2 {
		t.Fatal("the hash is not stable")
	}
}

func TestTheConfigHashChangesWithThePairChoices(t *testing.T) {
	mk := func(lp *continuumv1.LinkPeer) string {
		lp.ClusterId, lp.Name, lp.Addresses = "cl-b", "beta", []string{"10.1.0.1"}
		return ConfigHash(&continuumv1.Config{ApprovedAccessTier: 2, ClusterId: "cl-a", LinkPeers: []*continuumv1.LinkPeer{lp}})
	}
	base := mk(&continuumv1.LinkPeer{})
	seen := map[string]bool{base: true}
	for name, lp := range map[string]*continuumv1.LinkPeer{
		"no probe": {NoProbe: true}, "skip probe": {SkipProbe: true}, "via mesh": {Via: "mesh"}, "via public": {Via: "public"}, "throughput": {Throughput: true}, "interval": {Throughput: true, ThroughputIntervalSeconds: 600}, "budget": {Throughput: true, ThroughputBudgetBytes: 1 << 20},
	} {
		h := mk(lp)
		if seen[h] {
			t.Errorf("%s does not change the hash, so it would never be pushed", name)
		}
		seen[h] = true
	}
}
