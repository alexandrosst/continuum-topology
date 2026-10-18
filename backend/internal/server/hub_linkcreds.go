package server

import (
	"continuum/internal/connectivity"
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// linkCredsFor is the delivery for one agent's Config, or the zero delivery when the cluster takes part in no pair with throughput
// tests or the credentials cannot be had (which is logged, without material, and leaves the tests failing closed).
func (h *Hub) linkCredsFor(a store.Agent, links []kernel.LinkSetting) connectivity.LinkCredDelivery {
	if a.ClusterID == "" {
		return connectivity.LinkCredDelivery{}
	}
	involved := false
	for _, p := range connectivity.ThroughputPairs(links) {
		if p[0] == a.ClusterID || p[1] == a.ClusterID {
			involved = true
			break
		}
	}
	if !involved {
		return connectivity.LinkCredDelivery{}
	}
	set, err := h.C.Connectivity.LinkCredentials(bg(), links, false)
	if err != nil {
		h.C.Log.Warn("link credentials unavailable; throughput tests of the pair will fail until they are", "err", err.Error())
		return connectivity.LinkCredDelivery{}
	}
	return set.ForCluster(a.ClusterID)
}

// SyncLinkCredentials makes the credentials of the saved pairs and removes those of pairs that no longer ask for tests. It reads the
// settings as they are when it runs, so two of them running out of order still end at the current state.
func (h *Hub) SyncLinkCredentials() {
	if _, err := h.C.Connectivity.LinkCredentials(bg(), h.C.Settings().Links, true); err != nil {
		h.C.Log.Warn("could not update the link credentials", "err", err.Error())
	}
}
