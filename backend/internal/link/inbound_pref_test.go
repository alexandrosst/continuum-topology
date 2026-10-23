package link

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
)

func inbound(ip string, connections, bytes uint64) *ikhnosv1.RawFlow {
	return &ikhnosv1.RawFlow{Client: false, PeerIp: ip, Connections: connections, BytesIn: bytes, SegsOut: 10}
}

// B reaches A from a NAT address that A cannot dial (B's egress, or the way the server's stream comes in): it is the heaviest
// address by far, and must not be the one A's prober is sent to when A has dialed another address of B itself.
func TestAnAddressOnlyTheInboundTrafficUsesDoesNotOutrankOneThisClusterDialed(t *testing.T) {
	m, ck := clockedMeter(peer("cl-a", "A", "198.51.100.9", "100.64.0.7"))
	m.Observe([]*ikhnosv1.RawFlow{inbound("198.51.100.9", 50, 8<<30), flow("100.64.0.7", 1, 2<<10)})
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"100.64.0.7", "198.51.100.9"}) {
		t.Fatalf("the dialed address must lead: %v", got)
	}
	// Later and heavier inbound traffic changes nothing; the dialed address keeps its place while its evidence is fresh.
	ck.add(5 * time.Minute)
	m.Observe([]*ikhnosv1.RawFlow{inbound("198.51.100.9", 500, 80<<30)})
	if got := preferredOf(m, "cl-a"); got[0] != "100.64.0.7" {
		t.Fatalf("%v", got)
	}
}

// With no dialed address at all, inbound traffic is still a hint (a cluster that only receives from the peer), and its weight
// is a tenth of the same traffic dialed from here.
func TestInboundTrafficStillRanksWhenNothingWasDialedAndCountsForLessThanDialedTraffic(t *testing.T) {
	m, _ := clockedMeter(peer("cl-a", "A", "10.0.0.1", "10.0.0.2"))
	m.Observe([]*ikhnosv1.RawFlow{inbound("10.0.0.2", 1, 10<<20)})
	if got := preferredOf(m, "cl-a"); !slices.Equal(got, []string{"10.0.0.2"}) {
		t.Fatalf("inbound-only evidence was ignored: %v", got)
	}
	m.Observe([]*ikhnosv1.RawFlow{inbound("10.0.0.1", 1, 10<<20)})
	m.Observe([]*ikhnosv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 1, BytesOut: 10 << 20, SegsOut: 1}})
	m.mu.Lock()
	in, out := m.per["cl-a"].used[netip.MustParseAddr("10.0.0.2")].weight, m.per["cl-a"].used[netip.MustParseAddr("10.0.0.1")].weight
	m.mu.Unlock()
	// 10.0.0.1 got the same bytes inbound and dialed: inbound counted a tenth of the dialed row.
	if !(in < out) || out < 5*in {
		t.Errorf("weights: inbound-only %v, inbound plus dialed %v", in, out)
	}
}
