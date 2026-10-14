package link

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func TestAddressesAreSortedIntoMeshPrivateAndPublic(t *testing.T) {
	for addr, want := range map[string]string{
		"100.64.0.1":             "mesh",
		"100.127.255.254":        "mesh",
		"100.63.255.255":         "public", // just below the shared address space
		"100.128.0.1":            "public",
		"fd7a:115c:a1e0::1":      "mesh",
		"fd7a:115c:a1e0:ab12::4": "mesh",
		"fd7a:115c:a1e1::1":      "private", // a unique local address of another network
		"10.1.2.3":               "private",
		"172.16.0.1":             "private",
		"172.32.0.1":             "public",
		"192.168.1.1":            "private",
		"fd00::1":                "private",
		"203.0.113.9":            "public",
		"8.8.8.8":                "public",
		"2001:db8::1":            "public",
		"::ffff:10.0.0.1":        "private", // a mapped address is the address inside it
		"::ffff:100.64.3.3":      "mesh",
		"127.0.0.1":              "",
		"169.254.1.1":            "",
		"fe80::1":                "",
		"224.0.0.1":              "",
		"0.0.0.0":                "",
	} {
		if got := Class(netip.MustParseAddr(addr)); got != want {
			t.Errorf("Class(%s) = %q, want %q", addr, got, want)
		}
	}
	if got := Class(netip.Addr{}); got != "" {
		t.Errorf("the zero address has class %q", got)
	}
}

func TestOnlyTheKnownKindsAreValidToPinAPairTo(t *testing.T) {
	for _, s := range []string{"", "mesh", "private", "public"} {
		if !ValidVia(s) {
			t.Errorf("%q should be valid", s)
		}
	}
	for _, s := range []string{"Mesh", " mesh", "tailscale", "any", "10.0.0.0/8"} {
		if ValidVia(s) {
			t.Errorf("%q should not be valid", s)
		}
	}
}

// pinned is a peer with a mesh, a private and two public addresses, as a cluster on a Tailscale network might have.
func pinned(via string, preferred ...string) Peer {
	lp := peer("cl-a", "A", "100.64.0.7", "10.0.0.1", "203.0.113.1", "203.0.113.2")
	lp.Via = via
	p := NewPeers()
	p.set("cl-me", []*continuumv1.LinkPeer{lp}, map[string][]string{"cl-a": preferred})
	return p.List()[0]
}

func sorted(s []string) []string { s = slices.Clone(s); slices.Sort(s); return s }

func TestWithoutAPreferenceTheOrderIsTheOneItAlwaysWas(t *testing.T) {
	p := pinned("")
	for range 20 {
		got := orderAddresses(p, "10.0.0.1", 5)
		if len(got) != 4 || got[0] != "10.0.0.1" {
			t.Fatalf("the address that answered last is not first: %v", got)
		}
		if !slices.Equal(sorted(got[1:]), []string{"100.64.0.7", "203.0.113.1", "203.0.113.2"}) {
			t.Fatalf("%v", got)
		}
	}
	if got := orderAddresses(p, "", 2); len(got) != 2 {
		t.Errorf("the limit is kept: %v", got)
	}
	if got := orderAddresses(p, "10.0.0.1", 2); len(got) != 3 || got[0] != "10.0.0.1" {
		t.Errorf("the one that answered is on top of the limit, as before: %v", got)
	}
	// An address that answered, but is no longer the peer's, is not offered.
	if got := orderAddresses(p, "198.51.100.9", 5); slices.Contains(got, "198.51.100.9") || len(got) != 4 {
		t.Errorf("%v", got)
	}
}

func TestTheAddressesTrafficUsesComeFirstThenTheOneThatAnsweredThenTheRest(t *testing.T) {
	p := pinned("", "203.0.113.2", "100.64.0.7")
	for range 20 {
		got := orderAddresses(p, "10.0.0.1", 5)
		if !slices.Equal(got[:3], []string{"203.0.113.2", "100.64.0.7", "10.0.0.1"}) || len(got) != 4 || got[3] != "203.0.113.1" {
			t.Fatalf("%v", got)
		}
	}
	// A preferred address that also answered last is not tried twice.
	if got := orderAddresses(p, "203.0.113.2", 5); len(got) != 4 || got[0] != "203.0.113.2" || got[1] != "100.64.0.7" {
		t.Errorf("%v", got)
	}
	// The limit counts the preferred ones and the random rest together.
	if got := orderAddresses(p, "", 2); !slices.Equal(got, []string{"203.0.113.2", "100.64.0.7"}) {
		t.Errorf("%v", got)
	}
	if got := orderAddresses(p, "10.0.0.1", 1); !slices.Equal(got, []string{"203.0.113.2", "10.0.0.1"}) {
		t.Errorf("%v", got)
	}
	// An address that is not the peer's is ignored, and so is a repeat.
	q := pinned("", "198.51.100.1", "10.0.0.1", "10.0.0.1")
	if got := orderAddresses(q, "", 5); got[0] != "10.0.0.1" || len(got) != 4 {
		t.Errorf("%v", got)
	}
}

func TestAPairPinnedToAKindIsOnlyOfferedAddressesOfThatKind(t *testing.T) {
	for via, want := range map[string][]string{
		"mesh":    {"100.64.0.7"},
		"private": {"10.0.0.1"},
		"public":  {"203.0.113.1", "203.0.113.2"},
	} {
		// Whatever answered last, and whatever the traffic uses, the pin decides.
		p := pinned(via, "10.0.0.1", "100.64.0.7", "203.0.113.2")
		for range 10 {
			got := orderAddresses(p, "203.0.113.1", 5)
			if !slices.Equal(sorted(got), want) {
				t.Fatalf("%s: %v, want %v", via, got, want)
			}
		}
		if noAddress(p) {
			t.Errorf("%s: the peer has addresses of that kind", via)
		}
	}
	// With a preference of the pinned kind, that comes first.
	if got := orderAddresses(pinned("public", "203.0.113.2"), "", 5); !slices.Equal(got, []string{"203.0.113.2", "203.0.113.1"}) {
		t.Errorf("%v", got)
	}
}

func TestAPinToAKindThePeerHasNoneOfOffersNothingAndFallsBackToNothing(t *testing.T) {
	lp := peer("cl-a", "A", "10.0.0.1", "203.0.113.1")
	lp.Via = "mesh"
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{lp})
	pe := p.List()[0]
	if got := orderAddresses(pe, "10.0.0.1", 5); len(got) != 0 {
		t.Fatalf("offered %v", got)
	}
	if !noAddress(pe) || traceTarget(pe, "10.0.0.1") != "" {
		t.Error("a pin with no matching address must be reported, and have no route target")
	}
	// A peer that follows the traffic is never without.
	if noAddress(pinned("")) {
		t.Error("an unpinned peer has no address")
	}
}

func TestTheRouteIsTracedTowardsTheFirstOfTheOrderAndStaysThereFromOneTraceToTheNext(t *testing.T) {
	// No preference: what answered last, else the first address of the list, as before.
	if got := traceTarget(pinned(""), ""); got != "100.64.0.7" {
		t.Errorf("no history: %s", got)
	}
	if got := traceTarget(pinned(""), "203.0.113.2"); got != "203.0.113.2" {
		t.Errorf("answered last: %s", got)
	}
	// The address traffic uses wins over the one that happened to answer.
	if got := traceTarget(pinned("", "10.0.0.1"), "203.0.113.2"); got != "10.0.0.1" {
		t.Errorf("preferred: %s", got)
	}
	// A pin picks inside its kind, in the list's order, and the same one every time.
	for range 20 {
		if got := traceTarget(pinned("public"), ""); got != "203.0.113.1" {
			t.Fatalf("pinned: %s", got)
		}
	}
}

// clockedMeter is a meter whose clock the test moves.
func clockedMeter(peers ...*continuumv1.LinkPeer) (*Meter, *clock) {
	p := NewPeers()
	p.Set("cl-me", peers)
	m := NewMeter(p)
	ck := &clock{t: time.Unix(1_700_000_000, 0)}
	m.now = ck.now
	return m, ck
}

func flow(ip string, connections, bytes uint64) *continuumv1.RawFlow {
	return &continuumv1.RawFlow{Client: true, PeerIp: ip, Connections: connections, BytesOut: bytes, SegsOut: 10}
}

func preferredOf(m *Meter, peer string) []string {
	for _, d := range m.peersDoc().Peers {
		if d.ID == peer {
			return d.Preferred
		}
	}
	return nil
}

func TestTheMeterRemembersWhichAddressesTrafficUsesBusiestFirst(t *testing.T) {
	m, ck := clockedMeter(peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.1"))
	if got := preferredOf(m, "cl-a"); len(got) != 0 {
		t.Fatalf("nothing seen yet: %v", got)
	}
	m.Observe([]*continuumv1.RawFlow{flow("100.64.0.7", 1, 1000), flow("203.0.113.1", 5, 4<<20), flow("203.0.113.1", 1, 0)})
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"203.0.113.1", "100.64.0.7"}) {
		t.Fatalf("%v", got)
	}
	// What is busy now outweighs what was busy a while ago.
	ck.add(40 * time.Minute)
	m.Observe([]*continuumv1.RawFlow{flow("10.0.0.1", 3, 1<<20)})
	if got := preferredOf(m, "cl-a"); got[0] != "10.0.0.1" || len(got) != 3 {
		t.Fatalf("%v", got)
	}
	// An address not seen for an hour is forgotten, the rest stay.
	ck.add(30 * time.Minute)
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"10.0.0.1"}) {
		t.Fatalf("after expiry: %v", got)
	}
	m.Forget()
	if n := len(m.per["cl-a"].used); n != 1 {
		t.Errorf("the expired entries are still held: %d", n)
	}
	// Rows with no byte counts still count their connections, and a row that is not a peer's counts for nothing.
	m.ObserveRows([]*continuumv1.RawFlow{flow("203.0.113.1", 2, 0), flow("198.51.100.1", 9, 9<<20)}, false)
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"10.0.0.1", "203.0.113.1"}) {
		t.Errorf("%v", got)
	}
}

func TestTheTableOfAddressesInUseIsBounded(t *testing.T) {
	var addrs []string
	for i := 1; i <= 40; i++ {
		addrs = append(addrs, fmt.Sprintf("10.0.0.%d", i))
	}
	m, ck := clockedMeter(peer("cl-a", "A", addrs...))
	for i, a := range addrs {
		ck.add(time.Second)
		m.Observe([]*continuumv1.RawFlow{flow(a, 1, uint64(i+1)<<10)})
	}
	if n := len(m.per["cl-a"].used); n != maxUsedAddresses {
		t.Fatalf("%d addresses held", n)
	}
	got := preferredOf(m, "cl-a")
	if len(got) != maxUsedAddresses || got[0] != "10.0.0.40" {
		t.Fatalf("%v", got)
	}
	// The strongest stay when a new one comes in.
	ck.add(time.Second)
	m.Observe([]*continuumv1.RawFlow{flow("10.0.0.1", 1, 0)})
	if got := preferredOf(m, "cl-a"); !slices.Contains(got, "10.0.0.40") || !slices.Contains(got, "10.0.0.1") {
		t.Errorf("%v", got)
	}
}

func TestWhatTheMeterRemembersFollowsThePeerListAndIsForgottenWithThePeer(t *testing.T) {
	m, _ := clockedMeter(peer("cl-a", "A", "10.0.0.1", "10.0.0.2"), peer("cl-b", "B", "10.1.0.1"))
	m.Observe([]*continuumv1.RawFlow{flow("10.0.0.1", 1, 10), flow("10.0.0.2", 1, 10<<10), flow("10.1.0.1", 1, 1)})
	// The peer loses an address: the document never names it, and Forget drops it.
	m.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.2"), peer("cl-b", "B", "10.1.0.1")})
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"10.0.0.2"}) {
		t.Errorf("%v", got)
	}
	m.Forget()
	if n := len(m.per["cl-a"].used); n != 1 {
		t.Errorf("%d held", n)
	}
	// The peer goes away and comes back: nothing is remembered.
	m.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-b", "B", "10.1.0.1")})
	m.Forget()
	m.Peers.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.2"), peer("cl-b", "B", "10.1.0.1")})
	if got := preferredOf(m, "cl-a"); len(got) != 0 {
		t.Errorf("%v", got)
	}
}

func TestNoAddressInUseBecomesALabelOrASeries(t *testing.T) {
	m, _ := clockedMeter(peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.1"))
	m.EnableProbes()
	m.Observe([]*continuumv1.RawFlow{flow("100.64.0.7", 1, 1000), flow("203.0.113.1", 5, 4<<20), flow("10.0.0.1", 1, 1)})
	after := render(m)
	for k := range after {
		for _, bad := range []string{"10.0.0.1", "100.64.0.7", "203.0.113.1", "address", "via"} {
			if strings.Contains(k, bad) {
				t.Errorf("an address or a choice of address is in the series %s", k)
			}
		}
	}
	// The count of series is the same whichever addresses carry the traffic.
	m2, _ := clockedMeter(peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.1"))
	m2.EnableProbes()
	m2.Observe([]*continuumv1.RawFlow{flow("10.0.0.1", 1, 1)})
	if a, b := len(render(m2)), len(after); a != b {
		t.Errorf("%d series with one address in use, %d with three", a, b)
	}
}

func TestThePinAndThePreferenceTravelFromTheAgentThroughTheDocumentToTheProber(t *testing.T) {
	lpA := peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.1")
	lpA.Via = "public"
	lpB := peer("cl-b", "B", "10.1.0.1")
	lpC := peer("cl-c", "C", "10.2.0.1")
	lpC.Via = "something-new"
	m, _ := clockedMeter(lpA, lpB, lpC)
	m.Observe([]*continuumv1.RawFlow{flow("203.0.113.1", 2, 1<<20), flow("100.64.0.7", 1, 1)})
	got := map[string]Peer{}
	for _, x := range m.Peers.List() {
		got[x.ID] = x
	}
	if got["cl-a"].Via != "public" || got["cl-b"].Via != "" || got["cl-c"].Via != "" {
		t.Errorf("the pin did not reach the agent's peers, or an unknown kind was kept: %+v", got)
	}
	doc := m.peersDoc()
	byID := map[string]PeerDoc{}
	for _, d := range doc.Peers {
		byID[d.ID] = d
	}
	if byID["cl-a"].Via != "public" || !slices.Equal(byID["cl-a"].Preferred, []string{"203.0.113.1", "100.64.0.7"}) || byID["cl-b"].Via != "" || len(byID["cl-b"].Preferred) != 0 {
		t.Errorf("document: %+v", byID)
	}
	// The prober hears the same, and cannot be told about an address the peer does not have.
	byID["cl-a"] = func(d PeerDoc) PeerDoc { d.Preferred = append(d.Preferred, "198.51.100.5", "not-an-address"); return d }(byID["cl-a"])
	doc.Peers = []PeerDoc{byID["cl-a"], byID["cl-b"]}
	q := NewPeers()
	q.SetFromDoc(doc)
	var a Peer
	for _, x := range q.List() {
		if x.ID == "cl-a" {
			a = x
		}
	}
	if a.Via != "public" || len(a.Preferred) != 2 || a.Preferred[0].String() != "203.0.113.1" || a.Preferred[1].String() != "100.64.0.7" {
		t.Errorf("prober peer: %+v", a)
	}
	// A Config does not carry a preference: only the prober is given one.
	cfgPeers := NewPeers()
	cfgPeers.Set("cl-me", []*continuumv1.LinkPeer{lpA})
	if p := cfgPeers.List()[0]; len(p.Preferred) != 0 || p.Via != "public" {
		t.Errorf("%+v", p)
	}
}

func TestTheProbeFollowsTheTrafficAndTriesTheOthersWhenItDoesNotAnswer(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "203.0.113.2:") {
			return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
		}
		return summary(t, "owping_to.txt"), nil, nil
	}
	r := runnerFor(t, f, "10.0.0.1", "100.64.0.7", "203.0.113.2")
	r.Meter.Peers.set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.2")}, map[string][]string{"cl-a": {"203.0.113.2", "100.64.0.7"}})
	r.Round(context.Background())
	if len(f.calls) != 4 || !strings.Contains(strings.Join(f.calls[0], " "), "203.0.113.2:861") || !strings.Contains(strings.Join(f.calls[1], " "), "100.64.0.7:861") {
		t.Fatalf("the address the traffic uses is not tried first, then the next: %v", f.calls)
	}
}

func TestAProbePinnedToAKindUsesOnlyAddressesOfThatKind(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}
	lp := peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.2")
	lp.Via = "mesh"
	r := runnerFor(t, f, "10.0.0.1")
	r.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{lp})
	r.Round(context.Background())
	if len(f.calls) != 1 { // the peer did not answer, so the other direction is not tried either (see measurePeer)
		t.Fatalf("%d runs: %v", len(f.calls), f.calls)
	}
	for _, c := range f.calls {
		if j := strings.Join(c, " "); !strings.Contains(j, "100.64.0.7:861") {
			t.Errorf("%s", j)
		}
	}
}

func TestAProbePinnedToAKindThePeerHasNoneOfFailsWithAReasonAndDoesNotFallBack(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	lp := peer("cl-a", "A", "10.0.0.1", "203.0.113.2")
	lp.Via = "mesh"
	var traced []string
	r := runnerFor(t, f, "10.0.0.1")
	r.PathEvery = time.Hour
	r.Path = func(_ context.Context, dst netip.Addr) (PathResult, error) {
		traced = append(traced, dst.String())
		return PathResult{MTU: 1500, Reached: true, Route: "r"}, nil
	}
	r.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{lp})
	r.Round(context.Background())
	r.Round(context.Background())
	if len(f.calls) != 0 || len(traced) != 0 {
		t.Fatalf("another kind of address was used: %v %v", f.calls, traced)
	}
	got := render(r.Meter)
	for _, d := range []string{"forward", "reverse"} {
		k := `continuum_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp",reason="no_such_kind"}`
		if got[k] != "2" { // one per round, and the peer is not backed off from: the next round counts again
			t.Errorf("%s = %q", k, got[k])
		}
		if got[`continuum_link_probe_up{cluster="cl-me",peer="cl-a",direction="`+d+`",method="owamp"}`] != "0" {
			t.Errorf("the probe of %s reads as up", d)
		}
	}
	if got[`continuum_link_path_probe_failures_total{cluster="cl-me",peer="cl-a"}`] != "2" {
		t.Errorf("the route trace did not fail: %v", got)
	}
	// Once the peer has a mesh address, measuring resumes at once.
	lp2 := peer("cl-a", "A", "10.0.0.1", "100.64.0.9")
	lp2.Via = "mesh"
	r.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{lp2})
	r.Round(context.Background())
	if len(f.calls) != 2 || !strings.Contains(strings.Join(f.calls[0], " "), "100.64.0.9:861") {
		t.Errorf("%v", f.calls)
	}
}

func TestTheRouteTraceFollowsTheTrafficThenThePin(t *testing.T) {
	trace := func(lp *continuumv1.LinkPeer, preferred ...string) string {
		f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
		var dst string
		r := runnerFor(t, f, "10.0.0.1")
		r.PathEvery = time.Hour
		r.Path = func(_ context.Context, a netip.Addr) (PathResult, error) {
			dst = a.String()
			return PathResult{MTU: 1500, Reached: true, Route: "r"}, nil
		}
		r.Meter.Peers.set("cl-me", []*continuumv1.LinkPeer{lp}, map[string][]string{"cl-a": preferred})
		r.Round(context.Background())
		return dst
	}
	if got := trace(peer("cl-a", "A", "10.0.0.1", "203.0.113.2"), "203.0.113.2"); got != "203.0.113.2" {
		t.Errorf("traffic: %s", got)
	}
	pin := peer("cl-a", "A", "10.0.0.1", "203.0.113.2")
	pin.Via = "private"
	if got := trace(pin, "203.0.113.2"); got != "10.0.0.1" {
		t.Errorf("pinned: %s", got)
	}
	// Without either, the trace goes where the probe last got an answer, as it always did.
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "10.0.0.1", "203.0.113.2")
	var dst string
	r.PathEvery = time.Hour
	r.Path = func(_ context.Context, a netip.Addr) (PathResult, error) {
		dst = a.String()
		return PathResult{MTU: 1500, Reached: true, Route: "r"}, nil
	}
	r.Round(context.Background())
	if answered := r.good["cl-a"]; answered == "" || dst != answered {
		t.Errorf("as before: traced %s, the probe was answered by %s", dst, answered)
	}
}

func TestTheConnectTimingFollowsThePreferenceAndThePin(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return nil, errors.New("x")
	}
	lp := peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.2")
	c := connectFor(t, dial, lp)
	c.Meter.Peers.set("cl-me", []*continuumv1.LinkPeer{lp}, map[string][]string{"cl-a": {"203.0.113.2"}})
	c.Round(context.Background())
	if len(dialed) != 2 || dialed[0] != "203.0.113.2:8861" {
		t.Errorf("the address the traffic uses is not dialed first: %v", dialed)
	}

	dialed = nil
	pin := peer("cl-a", "A", "10.0.0.1", "100.64.0.7", "203.0.113.2")
	pin.Via = "public"
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{pin})
	c.Round(context.Background())
	if !slices.Equal(dialed, []string{"203.0.113.2:8861"}) {
		t.Errorf("a pin to public addresses dialed %v", dialed)
	}

	dialed = nil
	none := peer("cl-a", "A", "10.0.0.1", "203.0.113.2")
	none.Via = "mesh"
	c.Meter.Peers.Set("cl-me", []*continuumv1.LinkPeer{none})
	c.Round(context.Background())
	if len(dialed) != 0 {
		t.Errorf("dialed %v with no address of the kind", dialed)
	}
	got := render(c.Meter)
	if got[`continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="no_such_kind"}`] != "1" {
		t.Errorf("the failure is not counted: %v", got)
	}
}

func TestThroughputTestsFollowThePreferenceAndThePin(t *testing.T) {
	run := func(lp *continuumv1.LinkPeer, preferred ...string) (*Capacity, *fakeIperf) {
		f := &fakeIperf{capacity: 100e6}
		c, _ := capFor(t, f, nil)
		lp.Throughput = true
		c.Meter.Peers.set("cl-me", []*continuumv1.LinkPeer{lp}, map[string][]string{"cl-a": preferred})
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		settle(c)
		return c, f
	}
	_, f := run(peer("cl-a", "A", "10.0.0.1", "203.0.113.2"), "203.0.113.2")
	if f.count() == 0 || !strings.Contains(f.argsOf(0), "-c 203.0.113.2 ") {
		t.Fatalf("the first test did not go to the address the traffic uses: %v", f.calls)
	}

	pin := peer("cl-a", "A", "10.0.0.1", "203.0.113.2")
	pin.Via = "private"
	_, f = run(pin, "203.0.113.2")
	if f.count() == 0 {
		t.Fatal("no test ran")
	}
	for i := range f.count() {
		if !strings.Contains(f.argsOf(i), "-c 10.0.0.1 ") {
			t.Fatalf("a test left the pinned kind: %s", f.argsOf(i))
		}
	}

	none := peer("cl-a", "A", "10.0.0.1", "203.0.113.2")
	none.Via = "mesh"
	c, f := run(none)
	if f.count() != 0 {
		t.Fatalf("tests ran through another kind of address: %v", f.calls)
	}
	got := render(c.Meter)
	if got[`continuum_link_throughput_test_failures_total{cluster="cl-me",peer="cl-a",method="iperf3",reason="no_such_kind"}`] == "0" || !hasKey(got, `reason="no_such_kind"`) {
		t.Errorf("the failure is not counted: %v", got)
	}
}
