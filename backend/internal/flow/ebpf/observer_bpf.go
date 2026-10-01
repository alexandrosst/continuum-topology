//go:build linux && (amd64 || arm64)

package ebpf

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"sync"

	continuumv1 "continuum/gen/continuumv1"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"
)

// Options tunes what Open loads.
type Options struct {
	// Live also loads the socket-snapshot program, so the bytes of connections that are still open are
	// counted in every window instead of when they close. It needs the collector to see every process
	// (hostPID), or it only sees its own.
	Live bool
	// Names also loads and attaches the cgroup_skb egress program that captures DNS query names and TLS
	// SNI hostnames (see flow.c's observe_egress and names.go's parsers). It needs CAP_NET_ADMIN on top
	// of Open's own BPF/PERFMON/SYS_RESOURCE, which is why it is its own opt-in rather than always-on:
	// unlike everything else this package counts, it is the one thing here that looks at packet payloads
	// at all, even though it is bounded to exactly two hostnames and nothing else. See Observer.NamesErr
	// for why it is not running when this was asked for and failed.
	Names bool
}

// Observer holds the loaded programs. It is cheap: three maps, one tracepoint and, optionally, one iterator.
type Observer struct {
	objs struct {
		OnState *ebpf.Program `ebpf:"on_state"`
		Flows   *ebpf.Map     `ebpf:"flows"`
		Socks   *ebpf.Map     `ebpf:"socks"`
		Lost    *ebpf.Map     `ebpf:"lost"`
	}
	lnk  link.Link
	snap *ebpf.Program
	iter *link.Iter

	// LiveErr says why live counting was asked for and is not running; nil when it is running or was not asked for.
	LiveErr error
	// NamesErr says why Options.Names was asked for and is not running; nil when it is running or was not asked for.
	NamesErr error

	namesProg *ebpf.Program
	namesMap  *ebpf.Map
	namesLnk  link.Link
	namesRd   *ringbuf.Reader

	namesMu      sync.Mutex
	pendingNames []observedName
}

// observedName is one decoded, already-parsed ring buffer record, waiting for the next Collect().
type observedName struct {
	kind        uint8
	local, peer string
	port        uint16
	name        string
}

// maxPendingNames bounds how many decoded names wait between two Collect() calls, the same "bounded, and
// anything past the bound is counted lost rather than grown without limit" treatment count_lost's own
// map gives every other counter here.
const maxPendingNames = 2000

const (
	nameKindDNSQuery       = 1
	nameKindTLSClientHello = 2
)

// Live reports whether open connections are counted while they are open.
func (o *Observer) Live() bool { return o.iter != nil }

// Open loads the program and attaches it. It fails, without side effects, on kernels that cannot run it
// (no BTF, too old, missing privileges); the caller then falls back to the conntrack table. If only the
// optional snapshot program is refused, Open still succeeds and LiveErr says why.
func Open(opts ...Options) (*Observer, error) {
	var opt Options
	if len(opts) > 0 {
		opt = opts[0]
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("cannot raise the locked-memory limit: %w", err)
	}
	spec, err := loadFlow()
	if err != nil {
		return nil, fmt.Errorf("cannot read the embedded program: %w", err)
	}
	delete(spec.Programs, "snapshot")
	delete(spec.Programs, "observe_egress")
	var o Observer
	if err := spec.LoadAndAssign(&o.objs, nil); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			return nil, fmt.Errorf("the kernel's verifier rejected the program: %w", err)
		}
		return nil, fmt.Errorf("cannot load the program: %w", err)
	}
	l, err := link.AttachTracing(link.TracingOptions{Program: o.objs.OnState})
	if err != nil {
		o.closeMaps()
		return nil, fmt.Errorf("cannot attach to inet_sock_set_state: %w", err)
	}
	o.lnk = l
	if opt.Live {
		o.LiveErr = o.openSnapshot()
	}
	if opt.Names {
		o.NamesErr = o.openNames()
	}
	return &o, nil
}

func (o *Observer) openSnapshot() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "observe_egress")
	var p struct {
		Snapshot *ebpf.Program `ebpf:"snapshot"`
	}
	err = spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"flows": o.objs.Flows, "socks": o.objs.Socks, "lost": o.objs.Lost}})
	if err != nil {
		return fmt.Errorf("the socket snapshot program could not be loaded: %w", err)
	}
	it, err := link.AttachIter(link.IterOptions{Program: p.Snapshot})
	if err != nil {
		p.Snapshot.Close()
		return fmt.Errorf("the socket snapshot program could not be attached: %w", err)
	}
	o.snap, o.iter = p.Snapshot, it
	return nil
}

// cgroupV2Root finds the cgroup2 unified hierarchy's mount point by reading /proc/mounts, rather than
// assuming the conventional /sys/fs/cgroup - a chart can mount the host's cgroup filesystem at whatever
// path it likes, and a hybrid v1+v2 host can have cgroup2 mounted somewhere other than the usual default.
func cgroupV2Root() (string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return "", fmt.Errorf("cannot read /proc/mounts to find the cgroup2 filesystem: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 3 && fields[2] == "cgroup2" {
			return fields[1], nil
		}
	}
	return "", errors.New("no cgroup2 filesystem is mounted - the node-capture program needs the unified cgroup hierarchy")
}

// openNames loads and attaches the cgroup_skb egress program (see flow.c's observe_egress), and starts
// the background reader that decodes what it captures. Sharing this Observer's own "lost" map (the way
// openSnapshot already shares flows/socks/lost) means a full ring buffer counts toward the same lost
// total Collect() already reports, rather than a second, separate figure nothing reads.
func (o *Observer) openNames() error {
	cg, err := cgroupV2Root()
	if err != nil {
		return err
	}
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "snapshot")
	delete(spec.Maps, "flows")
	delete(spec.Maps, "socks")
	var p struct {
		ObserveEgress *ebpf.Program `ebpf:"observe_egress"`
		Names         *ebpf.Map     `ebpf:"names"`
	}
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"lost": o.objs.Lost}}); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			return fmt.Errorf("the kernel's verifier rejected the name-capture program: %w", err)
		}
		return fmt.Errorf("the name-capture program could not be loaded: %w", err)
	}
	l, err := link.AttachCgroup(link.CgroupOptions{Path: cg, Attach: ebpf.AttachCGroupInetEgress, Program: p.ObserveEgress})
	if err != nil {
		p.ObserveEgress.Close()
		p.Names.Close()
		return fmt.Errorf("the name-capture program could not be attached to the root cgroup (%s): %w", cg, err)
	}
	rd, err := ringbuf.NewReader(p.Names)
	if err != nil {
		l.Close()
		p.ObserveEgress.Close()
		p.Names.Close()
		return fmt.Errorf("the name-capture ring buffer could not be opened: %w", err)
	}
	o.namesProg, o.namesMap, o.namesLnk, o.namesRd = p.ObserveEgress, p.Names, l, rd
	go o.drainNames()
	return nil
}

// drainNames blocks on the ring buffer for as long as it is open, decoding each record (flow.c's raw
// bytes, parsed by names.go) into the small pending list Collect() drains. It returns, quietly, once the
// reader is closed (Close() does that) - ringbuf.Reader.Read's documented way of saying "stop".
func (o *Observer) drainNames() {
	for {
		rec, err := o.namesRd.Read()
		if err != nil {
			return
		}
		o.handleNameRecord(rec.RawSample)
	}
}

// handleNameRecord parses and stores one ring-buffer record. Every packet captured on this node's egress
// that merely looks like a DNS query or a TLS ClientHello reaches the two parsers below - untrusted input
// from any workload on the node, not just well-formed traffic - so a parser bug here must cost this one
// record, never the whole collector process (which would also take conntrack-based observation down with
// it on this node). Both parsers are already defensively written and unit-tested (see names_test.go), but
// the recover() is deliberate, cheap insurance against the next bug, not a substitute for fixing one.
func (o *Observer) handleNameRecord(raw []byte) {
	defer func() {
		if r := recover(); r != nil {
			// Nothing to log to here without a logger reference; dropping the record silently is exactly
			// as safe as the many other malformed-input cases both parsers already reject with ok=false -
			// this only differs in how the rejection was discovered.
			_ = r
		}
	}()
	const eventLen = 16 + 16 + 2 + 2 + 1 + 3 + 4 // saddr, daddr, sport, dport, kind, pad, len - the header
	// before flow.c's fixed-size data[NAME_CAP] array; see flowNameEvent's generated layout.
	if len(raw) < eventLen {
		return
	}
	var saddr, daddr [16]byte
	copy(saddr[:], raw[0:16])
	copy(daddr[:], raw[16:32])
	dport := binary.LittleEndian.Uint16(raw[34:36])
	kind := raw[36]
	length := binary.LittleEndian.Uint32(raw[40:44])
	data := raw[eventLen:]
	if int(length) > len(data) {
		length = uint32(len(data))
	}
	payload := data[:length]

	var name string
	var ok bool
	switch kind {
	case nameKindDNSQuery:
		name, ok = ParseDNSQueryName(payload)
	case nameKindTLSClientHello:
		name, ok = ParseTLSClientHelloSNI(payload)
	}
	if !ok {
		return
	}
	o.namesMu.Lock()
	if len(o.pendingNames) < maxPendingNames {
		o.pendingNames = append(o.pendingNames, observedName{kind: kind, local: addr(saddr), peer: addr(daddr), port: dport, name: name})
	}
	o.namesMu.Unlock()
}

// takeNames returns and clears everything decoded since the last call.
func (o *Observer) takeNames() []observedName {
	o.namesMu.Lock()
	defer o.namesMu.Unlock()
	if len(o.pendingNames) == 0 {
		return nil
	}
	names := o.pendingNames
	o.pendingNames = nil
	return names
}

func (o *Observer) Method() string   { return "ebpf" }
func (o *Observer) BytesKnown() bool { return true }

func (o *Observer) closeMaps() {
	o.objs.OnState.Close()
	o.objs.Flows.Close()
	o.objs.Socks.Close()
	o.objs.Lost.Close()
}

func (o *Observer) Close() error {
	if o.iter != nil {
		o.iter.Close()
		o.snap.Close()
	}
	if o.lnk != nil {
		o.lnk.Close()
	}
	if o.namesRd != nil {
		o.namesRd.Close() // unblocks drainNames' Read() loop
		o.namesLnk.Close()
		o.namesProg.Close()
		o.namesMap.Close()
	}
	o.closeMaps()
	return nil
}

func addr(b [16]uint8) string { return netip.AddrFrom16(b).Unmap().String() }

// ifaceName reads a NUL-terminated interface name out of the fixed-size buffer flow.c wrote (IFNAMSIZ, so
// it is never longer than 15 visible characters). Kernel interface names are restricted to printable
// ASCII, but this is still firmware-adjacent, untrusted-shaped input from a raw kernel struct read, so it
// is bounded and NUL-trimmed the same cautious way the node probe treats DMI strings.
func ifaceName(b [16]int8) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	raw := make([]byte, n)
	for i := 0; i < n; i++ {
		raw[i] = byte(b[i])
	}
	return string(raw)
}

// Collect returns everything counted since the previous call, and empties the counters. A connection is
// counted when it is established. Its bytes are counted when it closes, and also once per call while it
// is open when live counting is running.
func (o *Observer) Collect() ([]*continuumv1.RawFlow, uint64, error) {
	if o.iter != nil {
		if err := o.snapshot(); err != nil {
			return nil, 0, err
		}
	}
	var keys []flowFlowKey
	var (
		k    flowFlowKey
		vals []flowFlowVal
	)
	it := o.objs.Flows.Iterate()
	for it.Next(&k, &vals) {
		keys = append(keys, k)
	}
	if err := it.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading counters: %w", err)
	}

	var out []*continuumv1.RawFlow
	for _, key := range keys {
		vals = vals[:0]
		// LookupAndDelete is atomic where the kernel supports it, so nothing counted between the read and
		// the delete is lost; older kernels get the plain pair, which can lose a few increments.
		err := o.objs.Flows.LookupAndDelete(&key, &vals)
		if errors.Is(err, ebpf.ErrNotSupported) {
			if err = o.objs.Flows.Lookup(&key, &vals); err == nil {
				_ = o.objs.Flows.Delete(&key)
			}
		}
		if err != nil {
			continue
		}
		var sum flowFlowVal
		var iface string
		for _, v := range vals {
			sum.Connections += v.Connections
			sum.BytesOut += v.BytesOut
			sum.BytesIn += v.BytesIn
			sum.Retransmits += v.Retransmits
			sum.SegsOut += v.SegsOut
			sum.BufferDrops += v.BufferDrops
			sum.FailedAttempts += v.FailedAttempts
			sum.FailedRefused += v.FailedRefused
			sum.FailedTimeout += v.FailedTimeout
			sum.FailedReset += v.FailedReset
			sum.FailedUnreachable += v.FailedUnreachable
			// A gauge, not a sum: whichever CPU last sampled it wins, same as the interface below. A zero
			// value on one CPU's slice must never overwrite a real sample from another CPU's, since 0 here
			// means "no sample yet", not "no delay".
			if v.RttUs != 0 {
				sum.RttUs = v.RttUs
			}
			if v.JitterUs != 0 {
				sum.JitterUs = v.JitterUs
			}
			// handshake_us is set exactly once, by whichever CPU happened to handle this socket's
			// ESTABLISHED transition - the same single-writer gauge treatment as RttUs above, just set
			// only the one time rather than resampled throughout the connection's life.
			if v.HandshakeUs != 0 {
				sum.HandshakeUs = v.HandshakeUs
			}
			// cwnd/pacing_bps are gauges too, sampled at the exact same moments as RttUs/JitterUs - same
			// "0 means no sample" single-writer treatment.
			if v.Cwnd != 0 {
				sum.Cwnd = v.Cwnd
			}
			if v.PacingBps != 0 {
				sum.PacingBps = v.PacingBps
			}
			// Every CPU that ever handled this socket's traffic put_iface'd the same route, so any
			// non-empty reading is as good as another; take the first rather than requiring them to agree,
			// since a route change mid-life would otherwise blank it out for no good reason.
			if iface == "" {
				if s := ifaceName(v.Ifname); s != "" {
					iface = s
				}
			}
		}
		if sum.Connections == 0 && sum.BytesOut == 0 && sum.BytesIn == 0 && sum.FailedAttempts == 0 {
			continue
		}
		out = append(out, &continuumv1.RawFlow{
			Client:            key.Role == 1,
			LocalIp:           addr(key.Local),
			PeerIp:            addr(key.Peer),
			Port:              uint32(key.Port),
			Protocol:          "tcp",
			Connections:       sum.Connections,
			BytesOut:          sum.BytesOut,
			BytesIn:           sum.BytesIn,
			Iface:             iface,
			Retransmits:       sum.Retransmits,
			RttUs:             sum.RttUs,
			JitterUs:          sum.JitterUs,
			SegsOut:           sum.SegsOut,
			HandshakeUs:       sum.HandshakeUs,
			Cwnd:              sum.Cwnd,
			PacingBps:         sum.PacingBps,
			BufferDrops:       sum.BufferDrops,
			FailedAttempts:    sum.FailedAttempts,
			FailedRefused:     sum.FailedRefused,
			FailedTimeout:     sum.FailedTimeout,
			FailedReset:       sum.FailedReset,
			FailedUnreachable: sum.FailedUnreachable,
		})
	}

	// DNS query names and TLS SNI hostnames arrive on a wholly separate path (a ring buffer, not the
	// flows map) and were never TCP-state-tracked in the first place for DNS - each becomes its own
	// RawFlow row, with no counts of its own, purely so the same attribution and merge-by-attributed-key
	// logic already applied to every other row (see resolve.go, aggregate.go) applies to these too.
	for _, n := range o.takeNames() {
		rf := &continuumv1.RawFlow{Client: true, LocalIp: n.local, PeerIp: n.peer, Port: uint32(n.port)}
		switch n.kind {
		case nameKindDNSQuery:
			rf.Protocol = "udp"
			rf.DnsQueryName = n.name
		case nameKindTLSClientHello:
			rf.Protocol = "tcp"
			rf.SniHost = n.name
		default:
			continue
		}
		out = append(out, rf)
	}

	var lost uint64
	var lk uint32
	var per []uint64
	if err := o.objs.Lost.Lookup(&lk, &per); err == nil {
		for _, v := range per {
			lost += v
		}
		zero := make([]uint64, len(per))
		_ = o.objs.Lost.Update(&lk, zero, ebpf.UpdateAny)
	}
	return out, lost, nil
}

// snapshot walks the open sockets once, which adds the growth of each tracked connection to the counters.
func (o *Observer) snapshot() error {
	f, err := o.iter.Open()
	if err != nil {
		return fmt.Errorf("walking open sockets: %w", err)
	}
	defer f.Close()
	if _, err := io.Copy(io.Discard, f); err != nil {
		return fmt.Errorf("walking open sockets: %w", err)
	}
	return nil
}
