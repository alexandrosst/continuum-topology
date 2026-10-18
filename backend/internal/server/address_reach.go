package server

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"continuum/internal/interpret"
	"continuum/internal/measure"
	"continuum/internal/store"
)

// Testing an address from the cluster that will dial it.
//
// Whether an operator's address works depends on where it is dialled from, and the only place that knows is the sending
// cluster. The agent there already times TCP connections to the addresses the server names (see paths.go), so a test is
// that mechanism with an address an administrator just chose: the server adds it to the agent's targets for a short
// while, the agent connects and closes (it sends and reads nothing), and the result is read back here. It shows that the
// address answers on its port from that cluster. It does not show that the operator is what answers: the certificate is
// checked when data is sent, and the operator's stable name is what it is checked against.

const (
	// addressTestTTL is how long a test stays in the agent's targets and its result stays readable.
	addressTestTTL = 10 * time.Minute
	// maxAddressTests bounds the tests one agent holds at once, so a person cannot turn the agent into a port scanner.
	maxAddressTests = 6
	// sourceAddressTest marks a target issued for a test, as opposed to an observed or a manual one.
	sourceAddressTest = "address-test"
)

// addressTest is one address being tried from one agent. Kept on the agent's view and lost with it on a restart.
type addressTest struct {
	ID      string
	Address string
	Host    string
	Port    int
	Created time.Time
	// Quiet marks a test the server started itself (see address_drift.go), not one an administrator asked for.
	Quiet bool

	Done            bool
	Samples, Failed int
	Min, P50, P95   float64
	At              time.Time
}

// AddressTestDoc is what the UI reads about one test.
type AddressTestDoc struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	// State is pending (no result yet), reachable (a connection was made) or unreachable (every attempt failed).
	State    string  `json:"state"`
	Samples  int     `json:"samples,omitempty"`
	Failed   int     `json:"failed,omitempty"`
	RTTMinMs float64 `json:"rttMinMs,omitempty"`
	RTTP50Ms float64 `json:"rttP50Ms,omitempty"`
	RTTP95Ms float64 `json:"rttP95Ms,omitempty"`
	At       string  `json:"at,omitempty"`
	Created  string  `json:"created"`
}

func (t *addressTest) doc() AddressTestDoc {
	d := AddressTestDoc{ID: t.ID, Address: t.Address, State: "pending", Created: rfc(t.Created)}
	if t.Done {
		d.Samples, d.Failed, d.RTTMinMs, d.RTTP50Ms, d.RTTP95Ms, d.At = t.Samples, t.Failed, t.Min, t.P50, t.P95, rfc(t.At)
		d.State = "reachable"
		if t.Failed >= t.Samples {
			d.State = "unreachable"
		}
	}
	return d
}

// pruneAddressTests drops the tests whose time is up. Called with h.mu held.
func (h *Hub) pruneAddressTests(v *view, now time.Time) {
	for id, t := range v.addrTests {
		if now.Sub(t.Created) > addressTestTTL {
			delete(v.addrTests, id)
		}
	}
}

// addressTestTargets are the live tests as targets for configFor. Called with h.mu held.
func (h *Hub) addressTestTargets(v *view, now time.Time) []issuedTarget {
	h.pruneAddressTests(v, now)
	ts := make([]*addressTest, 0, len(v.addrTests))
	for _, t := range v.addrTests {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	out := make([]issuedTarget, 0, len(ts))
	for _, t := range ts {
		out = append(out, issuedTarget{ID: t.ID, Host: t.Host, Port: t.Port, Label: "address test", Source: sourceAddressTest})
	}
	return out
}

// noteAddressTest records what the agent measured for a test target. Called with h.mu held; reports whether t was one.
func (h *Hub) noteAddressTest(v *view, t issuedTarget, samples, failed uint32, min, p50, p95 float64, now time.Time) bool {
	if t.Source != sourceAddressTest {
		return false
	}
	if x := v.addrTests[t.ID]; x != nil {
		x.Done, x.Samples, x.Failed, x.Min, x.P50, x.P95, x.At = true, int(samples), int(failed), min, p50, p95, now
		v.noteReach(x)
	}
	return true
}

// StartAddressTests asks the agent to try each address. An address already under test is tested again from scratch.
func (h *Hub) StartAddressTests(ctx context.Context, actor, agentID string, addrs []string) ([]AddressTestDoc, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return nil, err
	}
	return h.startAddressTests(ctx, actor, a, addrs, false)
}

// startAddressTests is StartAddressTests for an agent already looked up. A quiet test is the server's own check (see
// address_drift.go): an address that cannot be tested is skipped rather than refused, and nothing is audited.
func (h *Hub) startAddressTests(ctx context.Context, actor string, a store.Agent, addrs []string, quiet bool) ([]AddressTestDoc, error) {
	if a.Status != store.StatusApproved {
		return nil, Errf(KindConflict, "agent is %s: only an approved agent can test an address", a.Status)
	}
	type want struct {
		addr, host string
		port       int
	}
	var wants []want
	seen := map[string]bool{}
	for _, in := range addrs {
		addr, err := validOperatorAddress(in)
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
			return nil, Errf(KindInvalid, "%s cannot be tested from a cluster: %v", addr, err)
		}
		wants = append(wants, want{addr, host, port})
	}
	if len(wants) == 0 {
		return nil, Errf(KindInvalid, "give at least one address to test")
	}
	if len(wants) > maxAddressTests {
		return nil, Errf(KindInvalid, "at most %d addresses are tested at once", maxAddressTests)
	}
	if h.consentOf(a.ID).has("measure") {
		return nil, Errf(KindConflict, "connection timing is paused for this cluster, and a test is a connection timing. Resume it in the cluster's access settings first")
	}
	now := h.C.Now()
	h.mu.Lock()
	v := h.viewFor(a.ID)
	if h.sessions[a.ID] == nil {
		h.mu.Unlock()
		return nil, Errf(KindConflict, "this agent is not connected right now, so it cannot test anything. Try again once it is back")
	}
	if d := v.ext.diag; d != nil {
		for _, c := range d.GetCollectors() {
			if c.GetName() == "measure" && !c.GetConfigured() {
				h.mu.Unlock()
				return nil, Errf(KindConflict, "connection timing is switched off in this agent's install, so it cannot test an address")
			}
		}
	}
	if v.addrTests == nil {
		v.addrTests = map[string]*addressTest{}
	}
	h.pruneAddressTests(v, now)
	for id, t := range v.addrTests {
		// Tested again: the old result must not pass for the new one. A person's test also takes the place of the server's
		// own quiet ones, which come back at the next sweep, so that checking in the background never uses up the room.
		if seen[t.Address] || (t.Quiet && !quiet) {
			delete(v.addrTests, id)
		}
	}
	if len(v.addrTests)+len(wants) > maxAddressTests {
		h.mu.Unlock()
		return nil, Errf(KindConflict, "this agent is already testing %d addresses; wait for them to finish (they expire after %d minutes)", len(v.addrTests), int(addressTestTTL/time.Minute))
	}
	// A fresh id each time: the agent re-times when its targets change, and an unchanged list would wait for its next round.
	out := make([]AddressTestDoc, 0, len(wants))
	for _, w := range wants {
		t := &addressTest{ID: "at-" + interpret.Hash(w.addr, strconv.FormatInt(now.UnixNano(), 10))[:12], Address: w.addr, Host: w.host, Port: w.port, Created: now, Quiet: quiet}
		v.addrTests[t.ID] = t
		out = append(out, t.doc())
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
	time.AfterFunc(addressTestTTL+time.Second, func() { h.pushOne(a.ID) })
	return out, nil
}

// AddressTests lists the live tests of an agent, newest first.
func (h *Hub) AddressTests(ctx context.Context, agentID string) ([]AddressTestDoc, error) {
	a, err := h.C.AgentInOrg(ctx, agentID)
	if err != nil {
		return nil, err
	}
	now := h.C.Now()
	h.mu.Lock()
	defer h.mu.Unlock()
	v := h.viewFor(a.ID)
	h.pruneAddressTests(v, now)
	ts := make([]*addressTest, 0, len(v.addrTests))
	for _, t := range v.addrTests {
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
	out := make([]AddressTestDoc, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.doc())
	}
	return out, nil
}
