package server

import (
	"context"
	"sort"
	"time"

	"continuum/internal/store"
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
func (v *view) noteReach(t *addressTest) {
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

// reachOf is the last test of an address from this agent's cluster, when it is recent enough to be a statement about now.
func (h *Hub) reachOf(agentID, address string, now time.Time) *AddressReachDoc {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return nil
	}
	r := v.reach[address]
	if r == nil || now.Sub(r.At) > reachFresh {
		return nil
	}
	d := &AddressReachDoc{State: "unreachable", At: rfc(r.At)}
	if r.Reachable {
		d.State, d.RTTP50Ms = "reachable", r.P50
	}
	return d
}

// driftTargets are the addresses each sending cluster dials, read from the telemetry intents that send to an operator:
// the address its sender chose, else the one recorded for the operator. A name inside the cluster is not an address to
// test from anywhere.
func (h *Hub) driftTargets(ctx context.Context) map[string][]string {
	intents, err := h.C.Store.ListTelemetryIntents(ctx, h.C.OrgID)
	if err != nil {
		return nil
	}
	ops, err := h.C.Store.ListOperators(ctx, h.C.OrgID)
	if err != nil {
		return nil
	}
	recorded := map[string]string{}
	for _, op := range ops {
		if op.Status == store.OperatorActive {
			recorded[op.ID] = op.Address
		}
	}
	out := map[string][]string{}
	seen := map[string]bool{}
	for _, ti := range intents {
		if ti.Status != store.TelemetryIntentActive {
			continue
		}
		dests := []store.Destination{ti.Destination}
		for _, d := range ti.Routes {
			dests = append(dests, d)
		}
		for _, d := range dests {
			if d.Kind != store.DestinationOperator {
				continue
			}
			addr := d.Address
			if addr == "" {
				addr = recorded[d.TargetOperatorID]
			}
			if addr == "" || seen[ti.AgentID+"|"+addr] {
				continue
			}
			seen[ti.AgentID+"|"+addr] = true
			out[ti.AgentID] = append(out[ti.AgentID], addr)
		}
	}
	return out
}

// driftSweep re-tests, from each connected sending cluster, the addresses whose last result is old.
func (h *Hub) driftSweep(ctx context.Context) {
	now := h.C.Now()
	targets := h.driftTargets(ctx)
	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a, err := h.C.AgentInOrg(ctx, id)
		if err != nil || a.Status != store.StatusApproved {
			continue
		}
		var due []string
		h.mu.Lock()
		v := h.views[id]
		live := map[string]bool{}
		if v != nil {
			h.pruneAddressTests(v, now) // a test past its time is not running
			for _, t := range v.addrTests {
				live[t.Address] = true
			}
		}
		for _, addr := range targets[id] {
			if r := v.reachRecord(addr); r != nil && now.Sub(r.At) < driftEvery {
				continue
			}
			if !live[addr] {
				due = append(due, addr)
			}
		}
		h.mu.Unlock()
		if len(due) == 0 {
			continue
		}
		if len(due) > maxAddressTests {
			due = due[:maxAddressTests]
		}
		// A refusal (not connected, timing paused) is the answer: nothing is tested and the last result ages out.
		_, _ = h.startAddressTests(ctx, "", a, due, true)
	}
}

// reachRecord is the stored result for an address; a nil view has none.
func (v *view) reachRecord(addr string) *addrReach {
	if v == nil {
		return nil
	}
	return v.reach[addr]
}
