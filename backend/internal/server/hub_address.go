package server

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"continuum/internal/connectivity"
	"continuum/internal/interpret"
	"continuum/internal/kernel"
	"continuum/internal/measure"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// pruneAddressTests drops the tests whose time is up. Called with h.mu held.
func (h *Hub) pruneAddressTests(v *view, now time.Time) {
	for id, t := range v.AddrTests {
		if now.Sub(t.Created) > connectivity.AddressTestTTL {
			delete(v.AddrTests, id)
		}
	}
}

// addressTestTargets are the live tests as targets for configFor. Called with h.mu held.
func (h *Hub) addressTestTargets(v *view, now time.Time) []connectivity.IssuedTarget {
	h.pruneAddressTests(v, now)
	ts := make([]*connectivity.AddressTest, 0, len(v.AddrTests))
	for _, t := range v.AddrTests {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	out := make([]connectivity.IssuedTarget, 0, len(ts))
	for _, t := range ts {
		out = append(out, connectivity.IssuedTarget{ID: t.ID, Host: t.Host, Port: t.Port, Label: "address test", Source: connectivity.SourceAddressTest})
	}
	return out
}

// noteAddressTest records what the agent measured for a test target. Called with h.mu held; reports whether t was one.
func (h *Hub) noteAddressTest(v *view, t connectivity.IssuedTarget, samples, failed uint32, min, p50, p95 float64, now time.Time) bool {
	if t.Source != connectivity.SourceAddressTest {
		return false
	}
	if x := v.AddrTests[t.ID]; x != nil {
		x.Done, x.Samples, x.Failed, x.Min, x.P50, x.P95, x.At = true, int(samples), int(failed), min, p50, p95, now
		v.NoteReach(x)
	}
	return true
}

// StartAddressTests asks the agent to try each address. An address already under test is tested again from scratch.
func (h *Hub) StartAddressTests(ctx context.Context, actor, agentID string, addrs []string) ([]connectivity.AddressTestDoc, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return h.startAddressTests(ctx, actor, a, addrs, false)
}

// startAddressTests is StartAddressTests for an agent already looked up. A quiet test is the server's own check (see
// address_drift.go): an address that cannot be tested is skipped rather than refused, and nothing is audited.
func (h *Hub) startAddressTests(ctx context.Context, actor string, a store.Agent, addrs []string, quiet bool) ([]connectivity.AddressTestDoc, error) {
	if a.Status != store.StatusApproved {
		return nil, kernel.Errf(kernel.KindConflict, "agent is %s: only an approved agent can test an address", a.Status)
	}
	type want struct {
		addr, host string
		port       int
	}
	var wants []want
	seen := map[string]bool{}
	for _, in := range addrs {
		addr, err := operators.ValidOperatorAddress(in)
		if err != nil {
			if quiet {
				continue
			}
			return nil, err
		}
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		host, ps, _ := net.SplitHostPort(addr)
		port, _ := strconv.Atoi(ps)
		if err := measure.CheckTarget(host, port); err != nil {
			if quiet {
				continue
			}
			return nil, kernel.Errf(kernel.KindInvalid, "%s cannot be tested from a cluster: %v", addr, err)
		}
		wants = append(wants, want{addr, host, port})
	}
	if len(wants) == 0 {
		return nil, kernel.Errf(kernel.KindInvalid, "give at least one address to test")
	}
	if len(wants) > connectivity.MaxAddressTests {
		return nil, kernel.Errf(kernel.KindInvalid, "at most %d addresses are tested at once", connectivity.MaxAddressTests)
	}
	if h.consentOf(a.ID).Has("measure") {
		return nil, kernel.Errf(kernel.KindConflict, "connection timing is paused for this cluster, and a test is a connection timing. Resume it in the cluster's access settings first")
	}
	now := h.C.Now()
	h.mu.Lock()
	v := h.viewFor(a.ID)
	if h.sessions[a.ID] == nil {
		h.mu.Unlock()
		return nil, kernel.Errf(kernel.KindConflict, "this agent is not connected right now, so it cannot test anything. Try again once it is back")
	}
	if d := v.Ext.Diag; d != nil {
		for _, c := range d.GetCollectors() {
			if c.GetName() == "measure" && !c.GetConfigured() {
				h.mu.Unlock()
				return nil, kernel.Errf(kernel.KindConflict, "connection timing is switched off in this agent's install, so it cannot test an address")
			}
		}
	}
	if v.AddrTests == nil {
		v.AddrTests = map[string]*connectivity.AddressTest{}
	}
	h.pruneAddressTests(v, now)
	for id, t := range v.AddrTests {
		// Tested again: the old result must not pass for the new one. A person's test also takes the place of the server's
		// own quiet ones, which come back at the next sweep, so that checking in the background never uses up the room.
		if seen[t.Address] || (t.Quiet && !quiet) {
			delete(v.AddrTests, id)
		}
	}
	if len(v.AddrTests)+len(wants) > connectivity.MaxAddressTests {
		h.mu.Unlock()
		return nil, kernel.Errf(kernel.KindConflict, "this agent is already testing %d addresses; wait for them to finish (they expire after %d minutes)", len(v.AddrTests), int(connectivity.AddressTestTTL/time.Minute))
	}
	// A fresh id each time: the agent re-times when its targets change, and an unchanged list would wait for its next round.
	out := make([]connectivity.AddressTestDoc, 0, len(wants))
	for _, w := range wants {
		t := &connectivity.AddressTest{ID: "at-" + interpret.Hash(w.addr, strconv.FormatInt(now.UnixNano(), 10))[:12], Address: w.addr, Host: w.host, Port: w.port, Created: now, Quiet: quiet}
		v.AddrTests[t.ID] = t
		out = append(out, t.Doc())
	}
	h.mu.Unlock()

	names := make([]string, 0, len(wants))
	for _, w := range wants {
		names = append(names, w.addr)
	}
	if !quiet {
		detail := fmt.Sprintf("%q: asked to test %s", a.Name, strings.Join(names, ", "))
		h.C.Audit(ctx, actor, "address-test-started", "agent", a.ID, detail)
	}
	h.pushOne(a.ID)
	// When the test expires its target has to leave the agent's list too.
	time.AfterFunc(connectivity.AddressTestTTL+time.Second, func() { h.pushOne(a.ID) })
	return out, nil
}

// AddressTests lists the live tests of an agent, newest first.
func (h *Hub) AddressTests(ctx context.Context, agentID string) ([]connectivity.AddressTestDoc, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return nil, err
	}
	now := h.C.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.viewFor(a.ID)
	h.pruneAddressTests(v, now)
	ts := make([]*connectivity.AddressTest, 0, len(v.AddrTests))
	for _, t := range v.AddrTests {
		if !t.Quiet {
			ts = append(ts, t)
		}
	}
	sort.Slice(ts, func(i, j int) bool {
		if !ts[i].Created.Equal(ts[j].Created) {
			return ts[i].Created.After(ts[j].Created)
		}
		return ts[i].ID < ts[j].ID
	})
	out := make([]connectivity.AddressTestDoc, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Doc())
	}
	return out, nil
}

// ReachOf is the last test of an address from this agent's cluster, when it is recent enough to be a statement about now.
func (h *Hub) ReachOf(agentID, address string, now time.Time) *connectivity.AddressReachDoc {
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.views[agentID]
	if v == nil {
		return nil
	}
	r := v.Reach[address]
	if r == nil || now.Sub(r.At) > connectivity.ReachFresh {
		return nil
	}
	d := &connectivity.AddressReachDoc{State: "unreachable", At: kernel.RFC(r.At)}
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
			for _, t := range v.AddrTests {
				live[t.Address] = true
			}
		}
		for _, addr := range targets[id] {
			if v != nil { // an agent with no view yet has never been tested
				if r := v.ReachRecord(addr); r != nil && now.Sub(r.At) < connectivity.DriftEvery {
					continue
				}
			}
			if !live[addr] {
				due = append(due, addr)
			}
		}
		h.mu.Unlock()
		if len(due) == 0 {
			continue
		}
		if len(due) > connectivity.MaxAddressTests {
			due = due[:connectivity.MaxAddressTests]
		}
		// A refusal (not connected, timing paused) is the answer: nothing is tested and the last result ages out.
		_, _ = h.startAddressTests(ctx, "", a, due, true)
	}
}
