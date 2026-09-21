// Package conntrack observes traffic by reading the kernel's connection-tracking table. It is the
// fallback for nodes where the eBPF observer cannot run (old kernels, no BTF, a locked-down runtime),
// and the way UDP is seen even where eBPF runs: UDP has no connections, so the table's entries (one per
// address pair and port, kept alive while packets flow) are the honest unit.
//
// It sees connections that pass through the node's netfilter, which is what Kubernetes networking uses
// for Services, so pod-to-pod and pod-to-outside traffic is visible; it does not see a pod talking to
// another pod on a private bridge that bypasses netfilter. Bytes are known only if the kernel counts
// them (sysctl net.netfilter.nf_conntrack_acct=1); the collector says so in every report rather than
// reporting zeros as if they were measurements.
package conntrack

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
)

// DefaultTable is where the kernel exposes the table. It is the host's table only when the collector
// runs in the host's network namespace.
const DefaultTable = "/proc/net/nf_conntrack"

// Entry is one tracked TCP connection, in the direction it was opened.
type Entry struct {
	Proto        string // tcp | udp
	State        string
	Src, Dst     string
	Sport, Dport uint32
	ReplySrc     string
	ReplySport   uint32
	OutBytes     uint64 // sent by whoever opened the connection
	InBytes      uint64
	HasBytes     bool
}

// tuple identifies a connection between polls. The reply tuple is included because two connections
// can share the original tuple when source ports are reused through NAT.
type tuple struct {
	proto              string
	src, dst, replySrc string
	sport, dport       uint32
}

// ParseLine parses one line of /proc/net/nf_conntrack; ok is false for anything that is not a TCP entry.
//
//	ipv4 2 tcp 6 431999 ESTABLISHED src=A dst=B sport=1 dport=2 packets=3 bytes=4 src=B dst=A sport=2 dport=1 packets=5 bytes=6 [ASSURED] mark=0 use=2
func ParseLine(line string) (Entry, bool) {
	f := strings.Fields(line)
	if len(f) < 8 || (f[0] != "ipv4" && f[0] != "ipv6") || (f[2] != "tcp" && f[2] != "udp") {
		return Entry{}, false
	}
	e := Entry{Proto: f[2]}
	rest := f[5:]
	if e.Proto == "tcp" {
		// TCP entries carry a state column before the tuples; UDP entries do not.
		if strings.Contains(f[5], "=") {
			return Entry{}, false
		}
		e.State = f[5]
		rest = f[6:]
	} else if !strings.Contains(f[5], "=") {
		return Entry{}, false
	}
	part := 0 // 0 while reading the original direction, 1 once the reply direction begins
	for _, kv := range rest {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "src":
			if e.Src == "" {
				e.Src = v
			} else if part == 0 {
				part, e.ReplySrc = 1, v
			}
		case "dst":
			if part == 0 && e.Dst == "" {
				e.Dst = v
			}
		case "sport":
			n, _ := strconv.ParseUint(v, 10, 32)
			if part == 0 {
				e.Sport = uint32(n)
			} else {
				e.ReplySport = uint32(n)
			}
		case "dport":
			n, _ := strconv.ParseUint(v, 10, 32)
			if part == 0 {
				e.Dport = uint32(n)
			}
		case "bytes":
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				continue
			}
			e.HasBytes = true
			if part == 0 {
				e.OutBytes = n
			} else {
				e.InBytes = n
			}
		}
	}
	if e.Src == "" || e.Dst == "" || e.ReplySrc == "" || e.Dport == 0 {
		return Entry{}, false
	}
	return e, true
}

// counted says whether a state means the handshake completed, so the entry is a real connection and not
// an attempt that never got an answer.
func counted(e Entry) bool {
	if e.Proto == "udp" {
		return true
	}
	switch e.State {
	case "ESTABLISHED", "FIN_WAIT", "CLOSE_WAIT", "LAST_ACK", "TIME_WAIT", "CLOSE":
		return true
	}
	return false
}

// Reader turns successive readings of the table into the counts since the previous reading.
type Reader struct {
	path   string
	protos map[string]bool
	prev   map[tuple][2]uint64
	// BytesKnown is true once an entry carried byte counters.
	bytesKnown bool
}

// Open reads the table at path (the kernel's, when empty). protocols limits what is reported: "tcp",
// "udp" or both; none means both.
func Open(path string, protocols ...string) (*Reader, error) {
	if path == "" {
		path = DefaultTable
	}
	protos := map[string]bool{}
	for _, p := range protocols {
		protos[p] = true
	}
	if len(protos) == 0 {
		protos["tcp"], protos["udp"] = true, true
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the connection-tracking table: %w", err)
	}
	f.Close()
	r := &Reader{path: path, protos: protos}
	// The first reading only establishes what already exists: connections open when the collector starts
	// have been counted by nobody, and reporting their lifetime total as one window would overstate it.
	if _, _, err := r.Collect(); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) Method() string   { return "conntrack" }
func (r *Reader) BytesKnown() bool { return r.bytesKnown }
func (r *Reader) Close() error     { return nil }

// Collect reads the table and returns what is new since the previous reading. Each connection is
// offered from both ends (as the caller's outbound connection and as the callee's inbound one), and the
// agent, which knows which addresses are its pods, keeps the one that applies.
func (r *Reader) Collect() ([]*continuumv1.RawFlow, uint64, error) {
	f, err := os.Open(r.path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	flows, next, known, err := diff(f, r.protos, r.prev, r.prev == nil)
	if err != nil {
		return nil, 0, err
	}
	r.prev = next
	if known {
		r.bytesKnown = true
	}
	return flows, 0, nil
}

func diff(in io.Reader, protos map[string]bool, prev map[tuple][2]uint64, baseline bool) ([]*continuumv1.RawFlow, map[tuple][2]uint64, bool, error) {
	type ck struct {
		proto    string
		src, dst string
		port     uint32
		client   bool
	}
	sums := map[ck]*continuumv1.RawFlow{}
	next := map[tuple][2]uint64{}
	known := false

	add := func(k ck, local, peer string, port uint32, conns, out, inb uint64) {
		if conns == 0 && out == 0 && inb == 0 {
			return
		}
		s, ok := sums[k]
		if !ok {
			s = &continuumv1.RawFlow{Client: k.client, LocalIp: local, PeerIp: peer, Port: port, Protocol: k.proto}
			sums[k] = s
		}
		s.Connections += conns
		s.BytesOut += out
		s.BytesIn += inb
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		e, ok := ParseLine(sc.Text())
		if !ok || !protos[e.Proto] || !counted(e) {
			continue
		}
		if e.HasBytes {
			known = true
		}
		t := tuple{e.Proto, e.Src, e.Dst, e.ReplySrc, e.Sport, e.Dport}
		last, seen := prev[t]
		next[t] = [2]uint64{e.OutBytes, e.InBytes}
		if baseline {
			continue
		}
		var conns, dOut, dIn uint64
		if !seen {
			conns = 1
			dOut, dIn = e.OutBytes, e.InBytes
		} else {
			if e.OutBytes >= last[0] {
				dOut = e.OutBytes - last[0]
			}
			if e.InBytes >= last[1] {
				dIn = e.InBytes - last[1]
			}
		}
		// As the caller: the address that dialed, what it dialed (a Service address, before NAT), the port.
		add(ck{e.Proto, e.Src, e.Dst, e.Dport, true}, e.Src, e.Dst, e.Dport, conns, dOut, dIn)
		// As the callee: the machine that finally answered (after NAT) and who connected to it.
		add(ck{e.Proto, e.ReplySrc, e.Src, e.ReplySport, false}, e.ReplySrc, e.Src, e.ReplySport, conns, dOut, dIn)
	}
	if err := sc.Err(); err != nil {
		return nil, nil, false, err
	}
	out := make([]*continuumv1.RawFlow, 0, len(sums))
	for _, s := range sums {
		out = append(out, s)
	}
	return out, next, known, nil
}
