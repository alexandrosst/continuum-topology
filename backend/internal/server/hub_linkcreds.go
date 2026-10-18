package server

import (
	"continuum/internal/kernel"
	"continuum/internal/store"
)

// linkCredsFor is the delivery for one agent's Config, or the zero delivery when the cluster takes part in no pair with throughput
// tests or the credentials cannot be had (which is logged, without material, and leaves the tests failing closed).
func (h *Hub) linkCredsFor(a store.Agent, links []kernel.LinkSetting) linkCredDelivery {
	if a.ClusterID == "" {
		return linkCredDelivery{}
	}
	involved := false
	for _, p := range throughputPairs(links) {
		if p[0] == a.ClusterID || p[1] == a.ClusterID {
			involved = true
			break
		}
	}
	if !involved {
		return linkCredDelivery{}
	}
	set, err := h.C.linkCredentials(bg(), links, false)
	if err != nil {
		h.C.Log.Warn("link credentials unavailable; throughput tests of the pair will fail until they are", "err", err.Error())
		return linkCredDelivery{}
	}
	return set.forCluster(a.ClusterID)
}

// syncLinkCredentials makes the credentials of the saved pairs and removes those of pairs that no longer ask for tests. It reads the
// settings as they are when it runs, so two of them running out of order still end at the current state.
func (h *Hub) syncLinkCredentials() {
	if _, err := h.C.linkCredentials(bg(), h.C.Settings().Links, true); err != nil {
		h.C.Log.Warn("could not update the link credentials", "err", err.Error())
	}
}
