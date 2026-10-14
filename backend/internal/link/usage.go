package link

import (
	"math"
	"net/netip"
	"sort"
	"time"
)

// The addresses real traffic with a peer is seen using, so the active measurements can follow it. This is kept in memory only
// and never reaches a series: the cardinality test and the doc statement that no address is labelled depend on that.
const (
	// maxUsedAddresses bounds the table of one peer, whatever the peer's list holds.
	maxUsedAddresses = 16
	// usedExpiry is how long an address that is no longer seen stays in the table.
	usedExpiry = time.Hour
	// usedHalfLife makes an address that was busy a while ago count for less than one that is busy now.
	usedHalfLife = 10 * time.Minute
)

// addrUse is how much traffic was seen with one address of a peer. weight decays with time (see usedHalfLife), so the order
// follows what the traffic does now rather than what it did in the past.
type addrUse struct {
	last   time.Time
	weight float64
	at     time.Time // when weight was last brought up to date
}

// now is the weight at t.
func (u *addrUse) now(t time.Time) float64 {
	if dt := t.Sub(u.at); dt > 0 {
		return u.weight * math.Exp2(-float64(dt)/float64(usedHalfLife))
	}
	return u.weight
}

// usedAddrs is the small table of one peer.
type usedAddrs map[netip.Addr]*addrUse

// add records w worth of traffic with a at t. A full table drops its weakest address to make room.
func (u *usedAddrs) add(a netip.Addr, w float64, t time.Time) {
	if *u == nil {
		*u = usedAddrs{}
	}
	e := (*u)[a]
	if e == nil {
		if len(*u) >= maxUsedAddresses {
			u.dropWeakest(t)
		}
		e = &addrUse{}
		(*u)[a] = e
	}
	e.weight, e.at, e.last = e.now(t)+w, t, t
}

func (u usedAddrs) dropWeakest(t time.Time) {
	var weakest netip.Addr
	low := math.Inf(1)
	for a, e := range u {
		if w := e.now(t); w < low {
			weakest, low = a, w
		}
	}
	delete(u, weakest)
}

// prune forgets the addresses not seen for usedExpiry, and those that are not among keep.
func (u usedAddrs) prune(t time.Time, keep map[netip.Addr]bool) {
	for a, e := range u {
		if t.Sub(e.last) > usedExpiry || (keep != nil && !keep[a]) {
			delete(u, a)
		}
	}
}

// ordered are the addresses in use, the busiest first and, among equals, the most recently seen.
func (u usedAddrs) ordered(t time.Time) []netip.Addr {
	out := make([]netip.Addr, 0, len(u))
	for a, e := range u {
		if t.Sub(e.last) <= usedExpiry {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		wi, wj := u[out[i]].now(t), u[out[j]].now(t)
		if wi != wj {
			return wi > wj
		}
		if !u[out[i]].last.Equal(u[out[j]].last) {
			return u[out[i]].last.After(u[out[j]].last)
		}
		return out[i].Less(out[j])
	})
	return out
}

// rowWeight is what one flow row counts for: its connections (at least one, as the row shows the address was in use) and its
// bytes in kibibytes when they were counted, so a long transfer outweighs a connection that carried nothing.
func rowWeight(connections, bytes uint64) float64 {
	return float64(max(connections, 1)) + float64(bytes)/1024
}
