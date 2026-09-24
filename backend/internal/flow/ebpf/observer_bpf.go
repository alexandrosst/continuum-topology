//go:build linux && (amd64 || arm64)

package ebpf

import (
	"errors"
	"fmt"
	"io"
	"net/netip"

	continuumv1 "continuum/gen/continuumv1"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/rlimit"
)

// Options tunes what Open loads.
type Options struct {
	// Live also loads the socket-snapshot program, so the bytes of connections that are still open are
	// counted in every window instead of when they close. It needs the collector to see every process
	// (hostPID), or it only sees its own.
	Live bool
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
}

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
	return &o, nil
}

func (o *Observer) openSnapshot() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
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
			// Every CPU that ever handled this socket's traffic put_iface'd the same route, so any
			// non-empty reading is as good as another; take the first rather than requiring them to agree,
			// since a route change mid-life would otherwise blank it out for no good reason.
			if iface == "" {
				if s := ifaceName(v.Ifname); s != "" {
					iface = s
				}
			}
		}
		if sum.Connections == 0 && sum.BytesOut == 0 && sum.BytesIn == 0 {
			continue
		}
		out = append(out, &continuumv1.RawFlow{
			Client:      key.Role == 1,
			LocalIp:     addr(key.Local),
			PeerIp:      addr(key.Peer),
			Port:        uint32(key.Port),
			Protocol:    "tcp",
			Connections: sum.Connections,
			BytesOut:    sum.BytesOut,
			BytesIn:     sum.BytesIn,
			Iface:       iface,
		})
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
