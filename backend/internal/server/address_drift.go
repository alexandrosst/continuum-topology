package server

import (
	"time"
)

// Noticing that an address stopped working before data stops flowing.
//
// A node address, a load balancer address or a mesh address can change under a running install: a node is replaced, a
// Service is recreated with a new node port, a mesh peer is renumbered. Nothing the sender holds says so until its
// exporter starts failing. The same test a person runs before choosing an address (see address_reach.go) is run again by
// the server on its own for each address a sending cluster dials, from that cluster, every so often, and the last result
// is kept so the senders list can show it and its diagnosis can use it.
//
// It is the plain connection test and nothing more: no new kind of traffic, no change to any install, nothing audited
// (it is not an action by a person), and it is skipped for any cluster that is not connected, has paused connection
// timing, or has its own tests running.

const (
	// driftTick is how often the server looks for addresses whose last result is old; driftEvery is how old that is.
	driftTick  = 5 * time.Minute
	driftEvery = 15 * time.Minute
	// reachFresh is how long a result may be shown and used for a diagnosis: it is a statement about now.
	reachFresh = time.Hour
	// maxReach bounds the results one cluster's view keeps.
	maxReach = 32
)

// addrReach is what the last test of one address from one cluster found.
type addrReach struct {
	Address string
	At      time.Time
	// Reachable is false when every attempt failed.
	Reachable       bool
	Samples, Failed int
	P50             float64
}

// AddressReachDoc is the last test of an address, from the cluster that dials it.
type AddressReachDoc struct {
	// State is reachable (a connection was made) or unreachable (every attempt failed).
	State    string  `json:"state"`
	At       string  `json:"at"`
	RTTP50Ms float64 `json:"rttP50Ms,omitempty"`
}

// noteReach keeps what a finished test found. Called with h.mu held.
func (v *netView) noteReach(t *addressTest) {
	if v.reach == nil {
		v.reach = map[string]*addrReach{}
	}
	v.reach[t.Address] = &addrReach{Address: t.Address, At: t.At, Reachable: t.Failed < t.Samples, Samples: t.Samples, Failed: t.Failed, P50: t.P50}
	for len(v.reach) > maxReach {
		oldest := ""
		for k, r := range v.reach {
			if oldest == "" || r.At.Before(v.reach[oldest].At) {
				oldest = k
			}
		}
		delete(v.reach, oldest)
	}
}

// reachRecord is the stored result for an address; a nil view has none.
func (v *netView) reachRecord(addr string) *addrReach {
	if v == nil {
		return nil
	}
	return v.reach[addr]
}
