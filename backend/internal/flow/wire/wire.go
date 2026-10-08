// Package wire is what the agent and the server agree on about a Flow: which ones are acceptable, how long its
// free-text fields may be, how two reports of the same edge merge, and which edges go first when a table is full.
// Both sides call the same functions (the agent before it sends, the server on arrival), so a rule changed here
// cannot be changed on one side only. It depends on nothing but the generated types, so the server can import it
// without the Kubernetes client the rest of internal/flow brings in.
package wire

import (
	"cmp"
	"container/heap"
	"errors"
	"iter"
	"net/netip"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/facts"
)

const (
	// MaxRef bounds an endpoint's ref (a workload key or a node name) and MaxName a pod name, a TLS server name and a
	// DNS name: Kubernetes and DNS cap them at 253 themselves, so anything longer is invented.
	MaxRef  = 512
	MaxName = 253
	// MaxIface bounds an interface name (the kernel's is 15).
	MaxIface = 64
	// MaxDNSNames bounds how many distinct domain names one edge remembers: "recently asked about" is the point, not
	// a full log.
	MaxDNSNames = 8
)

var (
	errNoEnds   = errors.New("a flow needs a source and a destination")
	errPort     = errors.New("the port is not 1-65535")
	errProtocol = errors.New("the protocol is not tcp or udp")
	errMethod   = errors.New("the method is not ebpf or conntrack")
	errNoise    = errors.New("the noise class is not dns or system")
	errPodName  = errors.New("a pod name is longer than Kubernetes allows")
	errEndpoint = errors.New("an endpoint is malformed")
	errOutside  = errors.New("neither end is a workload or a node")
)

// CheckKey reports why f cannot identify an edge: both ends, the port and the protocol. It is what a stored edge
// must pass again when it is loaded.
func CheckKey(f *continuumv1.Flow) error {
	switch {
	case f == nil || f.Src == nil || f.Dst == nil:
		return errNoEnds
	case f.Port == 0 || f.Port > 65535:
		return errPort
	case f.Protocol != "tcp" && f.Protocol != "udp":
		return errProtocol
	}
	for _, e := range []*continuumv1.FlowEndpoint{f.Src, f.Dst} {
		switch e.Kind {
		case continuumv1.FlowEndpoint_WORKLOAD, continuumv1.FlowEndpoint_NODE:
			// A NODE endpoint is a node-level process, or a hostNetwork pod that could not be pinned to a pod: its ref
			// is the node's name, bounded like a workload key.
			if e.Ref == "" || len(e.Ref) > MaxRef {
				return errEndpoint
			}
		case continuumv1.FlowEndpoint_EXTERNAL:
			if a, err := netip.ParseAddr(e.Ip); err != nil || a.String() != e.Ip || a.Is4In6() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() {
				return errEndpoint
			}
		default:
			return errEndpoint
		}
	}
	if f.Src.Kind == continuumv1.FlowEndpoint_EXTERNAL && f.Dst.Kind == continuumv1.FlowEndpoint_EXTERNAL {
		return errOutside
	}
	return nil
}

// Check reports why f is not a flow the server takes, or nil. Text that is merely too long is not a reason: Clip
// cuts it.
func Check(f *continuumv1.Flow) error {
	if err := CheckKey(f); err != nil {
		return err
	}
	switch {
	case f.Method != "ebpf" && f.Method != "conntrack":
		return errMethod
	case f.Noise != "" && f.Noise != "dns" && f.Noise != "system":
		return errNoise
	case len(f.SrcPod) > MaxName || len(f.DstPod) > MaxName:
		return errPodName
	}
	return nil
}

// Clip cuts the free-text fields of f to what the server keeps.
func Clip(f *continuumv1.Flow) {
	f.SniHost = facts.Cut(f.SniHost, MaxName)
	f.Iface = facts.Cut(f.Iface, MaxIface)
	if len(f.DnsQueryNames) > MaxDNSNames {
		f.DnsQueryNames = f.DnsQueryNames[:MaxDNSNames]
	}
	for i, n := range f.DnsQueryNames {
		f.DnsQueryNames[i] = facts.Cut(n, MaxName)
	}
}

// MergeGauges folds the readings of src into dst: everything about an edge that is a reading, not a running total.
// The latest sample replaces the last (a route changes, RTT drifts), and a missing one (empty, zero, nil) never
// replaces a real one. Counters and DNS names are not touched; each side keeps them where it keeps them (the agent
// on the Flow, the server on the FlowEdge, adding in its own width, without wrapping).
func MergeGauges(dst, src *continuumv1.Flow) {
	if src.Method == "ebpf" {
		dst.Method = "ebpf" // a flow seen by eBPF at all is described by it
	}
	dst.Noise = src.Noise
	dst.BytesKnown = dst.BytesKnown || src.BytesKnown
	if src.Iface != "" {
		dst.Iface = src.Iface
	}
	if src.SniHost != "" {
		dst.SniHost = src.SniHost // one peer essentially always carries one hostname
	}
	if src.RttUs != 0 {
		dst.RttUs = src.RttUs
	}
	if src.JitterUs != 0 {
		dst.JitterUs = src.JitterUs
	}
	if src.HandshakeUs != 0 {
		dst.HandshakeUs = src.HandshakeUs
	}
	if src.Cwnd != 0 {
		dst.Cwnd = src.Cwnd
	}
	if src.PacingBps != 0 {
		dst.PacingBps = src.PacingBps
	}
	if src.DnsRttUs != 0 {
		dst.DnsRttUs = src.DnsRttUs
	}
	if src.MssBytes != 0 {
		dst.MssBytes = src.MssBytes
	}
	// These four are optional on the wire because 0 is a real sample (a zero window, a drained queue): presence is
	// read off the pointer, and a non-nil 0 replaces what dst held.
	if src.RcvWndBytes != nil {
		dst.RcvWndBytes = src.RcvWndBytes
	}
	if src.SndWndBytes != nil {
		dst.SndWndBytes = src.SndWndBytes
	}
	if src.WmemQueuedBytes != nil {
		dst.WmemQueuedBytes = src.WmemQueuedBytes
	}
	if src.SndbufBytes != nil {
		dst.SndbufBytes = src.SndbufBytes
	}
	if src.TlsHandshake != continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_UNKNOWN {
		dst.TlsHandshake = src.TlsHandshake
	}
}

// MergeDNSNames folds add's distinct, non-empty names into cur, newest first, capped at MaxDNSNames. Used within
// one window by the agent and across windows by the server, the list-vs-gauge reasoning being the same.
func MergeDNSNames(cur, add []string) []string {
	for _, n := range add {
		if n == "" {
			continue
		}
		found := false
		for _, c := range cur {
			if c == n {
				found = true
				break
			}
		}
		if !found {
			cur = append([]string{n}, cur...)
		}
	}
	if len(cur) > MaxDNSNames {
		cur = cur[:MaxDNSNames]
	}
	return cur
}

// Oldest returns the n keys of seq with the smallest age, in no particular order, in one pass and without sorting
// the whole table: it keeps a bounded max-heap of the n oldest seen so far, so each further candidate either stays
// out at once (newer than everything kept: one comparison against the root) or replaces the newest kept entry.
// That costs O(len * log n) where evicting a few entries from a table of thousands is the normal case.
func Oldest[K comparable, T cmp.Ordered](n int, seq iter.Seq2[K, T]) []K {
	if n <= 0 {
		return nil
	}
	h := make(agedHeap[K, T], 0, n)
	for k, t := range seq {
		switch {
		case len(h) < n:
			heap.Push(&h, aged[K, T]{k, t})
		case t < h[0].t:
			heap.Pop(&h)
			heap.Push(&h, aged[K, T]{k, t})
		}
	}
	out := make([]K, len(h))
	for i, a := range h {
		out[i] = a.k
	}
	return out
}

type aged[K comparable, T cmp.Ordered] struct {
	k K
	t T
}

// agedHeap's root is the entry with the LATEST age among those held, which is the one a genuinely older candidate
// evicts.
type agedHeap[K comparable, T cmp.Ordered] []aged[K, T]

func (h agedHeap[K, T]) Len() int           { return len(h) }
func (h agedHeap[K, T]) Less(i, j int) bool { return h[i].t > h[j].t }
func (h agedHeap[K, T]) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *agedHeap[K, T]) Push(x any)        { *h = append(*h, x.(aged[K, T])) }
func (h *agedHeap[K, T]) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}
