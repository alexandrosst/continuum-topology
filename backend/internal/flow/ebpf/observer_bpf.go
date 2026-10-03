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
		// SnatExhaustion is loaded eagerly here, in the main collection, even though the two programs
		// that increment it (on_hash_connect_fexit/on_hash_connect_kretprobe) are not - see
		// openSnatExhaustion. That way Collect() can always read it, whether or not either attach
		// path worked on this kernel; a node where neither attached simply reads 0 forever, the same
		// as a node that was never asked to look.
		SnatExhaustion *ebpf.Map `ebpf:"snat_exhaustion"`
		// CpuFreqChangeCount/ThermalTripCount are loaded eagerly here, in the main collection, exactly
		// like SnatExhaustion above and for the same reason: the programs that increment them
		// (on_cpu_freq_change/on_thermal_zone_trip) are attached separately (see openCpuFreqChange/
		// openThermalTrip), so Collect() can always read these, whether or not either tracepoint attach
		// worked on this kernel - a node where neither attached simply reads 0 forever.
		CpuFreqChangeCount *ebpf.Map `ebpf:"cpu_freq_change_count"`
		ThermalTripCount   *ebpf.Map `ebpf:"thermal_trip_count"`
	}
	lnk  link.Link
	snap *ebpf.Program
	iter *link.Iter
	// snatProg/snatLnk are whichever of the fexit/kretprobe attach paths in openSnatExhaustion
	// succeeded (at most one of them); nil when neither did, in which case SnatExhaustionErr says why.
	snatProg *ebpf.Program
	snatLnk  link.Link
	// cpuFreqProg/cpuFreqLnk and thermalProg/thermalLnk are on_cpu_freq_change/on_thermal_zone_trip's
	// own program/link, attached independently of each other and of snatProg/snatLnk above (see
	// openCpuFreqChange/openThermalTrip) - nil when that particular tracepoint attach failed, in which
	// case CpuFreqChangeErr/ThermalTripErr says why.
	cpuFreqProg *ebpf.Program
	cpuFreqLnk  link.Link
	thermalProg *ebpf.Program
	thermalLnk  link.Link

	// LiveErr says why live counting was asked for and is not running; nil when it is running or was not asked for.
	LiveErr error
	// NamesErr says why Options.Names was asked for and is not running; nil when it is running or was not asked for.
	NamesErr error
	// DNSLatencyErr says why the DNS-response-latency half of Options.Names specifically is not running
	// (observe_ingress could not attach), independently of NamesErr: the query/SNI-capture half this
	// shares Options.Names with can be running perfectly well (NamesErr == nil) while this is set, since
	// ingress and egress are two separate attach points that can fail independently. nil when it is
	// running or Options.Names was not asked for at all.
	DNSLatencyErr error
	// SnatExhaustionErr says why the SNAT/ephemeral-port-exhaustion counter (see flow.c's own
	// snat_exhaustion doc comment) is not being collected; nil when either attach path worked. Unlike
	// LiveErr/NamesErr this is never something the caller asked for and might not have gotten - both
	// attach attempts always happen - so it is purely informational (worth logging, never worth
	// falling back over).
	SnatExhaustionErr error
	// CpuFreqChangeErr/ThermalTripErr say why power:cpu_frequency/thermal:thermal_zone_trip (see flow.c's
	// own doc comment on cpu_freq_change_count/thermal_trip_count) are not being collected; nil when
	// that tracepoint attached. Like SnatExhaustionErr, never something the caller asked for - both
	// attach attempts always happen, unconditionally, since neither reads a packet payload and neither
	// needs a privilege this package does not already require for on_state's own attach - so these are
	// purely informational. The two are independent: a VM guest can have a working power:cpu_frequency
	// tracepoint and no thermal zones at all (ThermalTripErr set, CpuFreqChangeErr nil), and a fixed-
	// frequency board can have thermal zones but no cpufreq driver (the reverse).
	CpuFreqChangeErr error
	ThermalTripErr   error

	namesProg *ebpf.Program
	namesMap  *ebpf.Map
	namesLnk  link.Link
	namesRd   *ringbuf.Reader
	// ingressProg/ingressLnk are observe_ingress's own program/link (see openNames) - the DNS-latency
	// half of the Names opt-in, a separate attach type (ingress, not egress) from namesProg/namesLnk
	// above but sharing the same ring buffer (namesMap) and torn down alongside it in Close.
	ingressProg   *ebpf.Program
	ingressLnk    link.Link
	dnsPendingMap *ebpf.Map

	namesMu      sync.Mutex
	pendingNames []observedName
}

// observedName is one decoded, already-parsed ring buffer record, waiting for the next Collect().
type observedName struct {
	kind        uint8
	local, peer string
	port        uint16
	name        string
	// dnsRttUs is set only for kind == nameKindDNSLatency, in which case name is always empty and this
	// carries the computed round-trip in microseconds instead - flow.c's name_event repurposes its own
	// len field for this one kind (see its doc comment), so nothing here comes from parsing payload.
	dnsRttUs uint32
}

// maxPendingNames bounds how many decoded names wait between two Collect() calls, the same "bounded, and
// anything past the bound is counted lost rather than grown without limit" treatment count_lost's own
// map gives every other counter here.
const maxPendingNames = 2000

const (
	nameKindDNSQuery       = 1
	nameKindTLSClientHello = 2
	nameKindDNSLatency     = 3
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
	// Attempted unconditionally, unlike Live/Names just below: neither attach path here needs any
	// privilege beyond what this function already required for on_state's own tp_btf attach, and
	// neither looks at a packet payload, so there is nothing to opt into.
	o.SnatExhaustionErr = o.openSnatExhaustion()
	o.CpuFreqChangeErr = o.openCpuFreqChange()
	o.ThermalTripErr = o.openThermalTrip()
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

// openSnatExhaustion attaches the SNAT/ephemeral-port-exhaustion counter (flow.c's snat_exhaustion map
// and its two candidate programs) - see that map's own doc comment for what it counts and why
// inet_hash_connect, the function both attach paths target, is an inherently fragile hook to have
// chosen: an internal kernel function, not a stable tracepoint or syscall ABI, free to change shape or
// disappear on a future kernel refactor. The fexit path is tried first (cheap, BTF-validated); if the
// running kernel's BTF does not describe inet_hash_connect the way flow.c's fexit program declares it,
// that load is rejected outright and the kretprobe fallback - which only needs the symbol to exist and
// be kprobe-able, not any particular signature - is tried instead. If both fail, the error says so and
// the caller is left with SnatExhaustionErr set; this never blocks Open() itself, since the signal is a
// bonus, not core function.
func (o *Observer) openSnatExhaustion() error {
	fexitErr := o.openSnatExhaustionFexit()
	if fexitErr == nil {
		return nil
	}
	if kretErr := o.openSnatExhaustionKretprobe(); kretErr == nil {
		return nil
	} else {
		return fmt.Errorf("fexit/inet_hash_connect: %v; kretprobe/inet_hash_connect fallback: %w", fexitErr, kretErr)
	}
}

func (o *Observer) openSnatExhaustionFexit() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "snapshot")
	delete(spec.Programs, "observe_egress")
	delete(spec.Programs, "observe_ingress")
	delete(spec.Programs, "on_hash_connect_kretprobe")
	var p struct {
		Prog *ebpf.Program `ebpf:"on_hash_connect_fexit"`
	}
	// snat_exhaustion is replaced with the map already loaded as part of o.objs (see its own doc
	// comment there) so this program increments the one copy Collect() actually reads, the same
	// MapReplacements treatment openSnapshot above gives flows/socks/lost.
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"snat_exhaustion": o.objs.SnatExhaustion}}); err != nil {
		return fmt.Errorf("could not load: %w", err)
	}
	l, err := link.AttachTracing(link.TracingOptions{Program: p.Prog})
	if err != nil {
		p.Prog.Close()
		return fmt.Errorf("could not attach: %w", err)
	}
	o.snatProg, o.snatLnk = p.Prog, l
	return nil
}

func (o *Observer) openSnatExhaustionKretprobe() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "snapshot")
	delete(spec.Programs, "observe_egress")
	delete(spec.Programs, "observe_ingress")
	delete(spec.Programs, "on_hash_connect_fexit")
	var p struct {
		Prog *ebpf.Program `ebpf:"on_hash_connect_kretprobe"`
	}
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"snat_exhaustion": o.objs.SnatExhaustion}}); err != nil {
		return fmt.Errorf("could not load: %w", err)
	}
	l, err := link.Kretprobe("inet_hash_connect", p.Prog, nil)
	if err != nil {
		p.Prog.Close()
		return fmt.Errorf("could not attach: %w", err)
	}
	o.snatProg, o.snatLnk = p.Prog, l
	return nil
}

// openCpuFreqChange attaches on_cpu_freq_change to the kernel's power:cpu_frequency tracepoint (flow.c's
// cpu_freq_change_count map) - see that map's own doc comment for what it counts and, importantly, what
// it does NOT mean (a cpufreq scaling event is not itself throttling evidence). Unlike
// openSnatExhaustion's fexit/kretprobe pair, there is only one attach path here: a plain tracepoint
// needs no BTF match at load time (ctx is never dereferenced), so the only way this fails is the
// tracepoint itself not existing in this kernel's tracefs (link.Tracepoint returns that at attach time,
// never at load) - an old kernel, or a board whose CPU has no cpufreq driver compiled in.
func (o *Observer) openCpuFreqChange() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "snapshot")
	delete(spec.Programs, "observe_egress")
	delete(spec.Programs, "observe_ingress")
	delete(spec.Programs, "on_hash_connect_fexit")
	delete(spec.Programs, "on_hash_connect_kretprobe")
	delete(spec.Programs, "on_thermal_zone_trip")
	var p struct {
		Prog *ebpf.Program `ebpf:"on_cpu_freq_change"`
	}
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"cpu_freq_change_count": o.objs.CpuFreqChangeCount}}); err != nil {
		return fmt.Errorf("could not load: %w", err)
	}
	l, err := link.Tracepoint("power", "cpu_frequency", p.Prog, nil)
	if err != nil {
		p.Prog.Close()
		return fmt.Errorf("could not attach to power:cpu_frequency: %w", err)
	}
	o.cpuFreqProg, o.cpuFreqLnk = p.Prog, l
	return nil
}

// openThermalTrip attaches on_thermal_zone_trip to the kernel's thermal:thermal_zone_trip tracepoint
// (flow.c's thermal_trip_count map) - see that map's own doc comment for why, unlike
// cpu_freq_change_count's counter above, a nonzero reading here IS the decisive thermal-throttling
// signal. Same single-attach-path treatment as openCpuFreqChange above, for the same reason (no BTF
// match needed at load time) - the only failure here is the tracepoint not existing (a VM guest, a
// board with no thermal zones registered, or a kernel too old to have this tracepoint at all).
func (o *Observer) openThermalTrip() error {
	spec, err := loadFlow()
	if err != nil {
		return err
	}
	delete(spec.Programs, "on_state")
	delete(spec.Programs, "snapshot")
	delete(spec.Programs, "observe_egress")
	delete(spec.Programs, "observe_ingress")
	delete(spec.Programs, "on_hash_connect_fexit")
	delete(spec.Programs, "on_hash_connect_kretprobe")
	delete(spec.Programs, "on_cpu_freq_change")
	var p struct {
		Prog *ebpf.Program `ebpf:"on_thermal_zone_trip"`
	}
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"thermal_trip_count": o.objs.ThermalTripCount}}); err != nil {
		return fmt.Errorf("could not load: %w", err)
	}
	l, err := link.Tracepoint("thermal", "thermal_zone_trip", p.Prog, nil)
	if err != nil {
		p.Prog.Close()
		return fmt.Errorf("could not attach to thermal:thermal_zone_trip: %w", err)
	}
	o.thermalProg, o.thermalLnk = p.Prog, l
	return nil
}

// SnatExhaustion reads the running total of connect() attempts that failed with EADDRNOTAVAIL since
// this program was loaded (see flow.c's snat_exhaustion doc comment for the full story) - it is a
// lifetime count the map itself never resets, not a per-window delta like Collect()'s own counters, so
// unlike those this is read, never read-and-cleared. 0 on a node where neither attach path in
// openSnatExhaustion worked (SnatExhaustionErr != nil) is indistinguishable from a real, healthy 0 - the
// same ambiguity Options.Live/Options.Names leave callers to resolve via their own error field, which is
// exactly why this one exists too.
func (o *Observer) SnatExhaustion() uint64 {
	if o.objs.SnatExhaustion == nil {
		return 0
	}
	var k uint32
	var per []uint64
	if err := o.objs.SnatExhaustion.Lookup(&k, &per); err != nil {
		return 0
	}
	var total uint64
	for _, v := range per {
		total += v
	}
	return total
}

// CpuFreqChangeCount reads the running total of power:cpu_frequency tracepoint firings since this
// program was loaded (see flow.c's cpu_freq_change_count doc comment) - a lifetime count, read never
// read-and-cleared, exactly like SnatExhaustion above. 0 when openCpuFreqChange's attach did not work
// (CpuFreqChangeErr != nil) is indistinguishable from a real 0 (a fixed-frequency board that never
// scales at all) - this counter's only real job is corroborating ThermalTripCount below, not standing
// on its own as a health signal.
func (o *Observer) CpuFreqChangeCount() uint64 {
	if o.objs.CpuFreqChangeCount == nil {
		return 0
	}
	var k uint32
	var per []uint64
	if err := o.objs.CpuFreqChangeCount.Lookup(&k, &per); err != nil {
		return 0
	}
	var total uint64
	for _, v := range per {
		total += v
	}
	return total
}

// ThermalTripCount reads the running total of thermal:thermal_zone_trip tracepoint firings since this
// program was loaded (see flow.c's thermal_trip_count doc comment) - the decisive thermal-throttling
// signal, unlike CpuFreqChangeCount above. A lifetime count, read never read-and-cleared, exactly like
// SnatExhaustion. 0 when openThermalTrip's attach did not work (ThermalTripErr != nil) is
// indistinguishable from a real, healthy 0 - the same ambiguity SnatExhaustion already leaves callers
// to resolve via ThermalTripErr.
func (o *Observer) ThermalTripCount() uint64 {
	if o.objs.ThermalTripCount == nil {
		return 0
	}
	var k uint32
	var per []uint64
	if err := o.objs.ThermalTripCount.Lookup(&k, &per); err != nil {
		return 0
	}
	var total uint64
	for _, v := range per {
		total += v
	}
	return total
}

// ThermalThrottle satisfies collector.ThermalThrottleSource - see CpuFreqChangeCount/ThermalTripCount
// above for what each return value means on its own, most importantly why only the second one is ever
// treated as throttling evidence.
func (o *Observer) ThermalThrottle() (cpuFreqChangeCount, thermalTripCount uint64) {
	return o.CpuFreqChangeCount(), o.ThermalTripCount()
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
//
// Also loads and attaches observe_ingress (see flow.c) - a second, independent attach, on the ingress
// side of the same root cgroup, that matches DNS responses against the pending queries observe_egress
// records and reports a latency sample through this same ring buffer. It shares "dns_pending" and "lost"
// with the main on_state programs the way openSnapshot shares flows/socks/lost, but is otherwise
// entirely optional: if it fails to attach, Names still runs (query/SNI capture keeps working) with only
// the latency half missing - callers are told this through the returned error wrapping, not a silent
// partial success, but nothing here is fatal to the rest of Open().
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
	// "flows" is deliberately NOT deleted here, unlike "socks": observe_egress's mesh-bypass check (see
	// flow.c's note_mesh_bypass) writes into it directly, on the same flow_key a successful connection to
	// the same peer would get from on_state - so, unlike socks, it must be the very same map instance
	// on_state and Collect() already use, not a second, independent one of its own. That is what the
	// "flows" entry in MapReplacements just below does, mirroring openSnapshot's own reuse of
	// o.objs.Flows/Socks/Lost above.
	delete(spec.Maps, "socks")
	var p struct {
		ObserveEgress  *ebpf.Program `ebpf:"observe_egress"`
		ObserveIngress *ebpf.Program `ebpf:"observe_ingress"`
		Names          *ebpf.Map     `ebpf:"names"`
		DnsPending     *ebpf.Map     `ebpf:"dns_pending"`
	}
	if err := spec.LoadAndAssign(&p, &ebpf.CollectionOptions{MapReplacements: map[string]*ebpf.Map{"flows": o.objs.Flows, "lost": o.objs.Lost}}); err != nil {
		var ve *ebpf.VerifierError
		if errors.As(err, &ve) {
			return fmt.Errorf("the kernel's verifier rejected the name-capture program: %w", err)
		}
		return fmt.Errorf("the name-capture program could not be loaded: %w", err)
	}
	l, err := link.AttachCgroup(link.CgroupOptions{Path: cg, Attach: ebpf.AttachCGroupInetEgress, Program: p.ObserveEgress})
	if err != nil {
		p.ObserveEgress.Close()
		p.ObserveIngress.Close()
		p.Names.Close()
		p.DnsPending.Close()
		return fmt.Errorf("the name-capture program could not be attached to the root cgroup (%s): %w", cg, err)
	}
	rd, err := ringbuf.NewReader(p.Names)
	if err != nil {
		l.Close()
		p.ObserveEgress.Close()
		p.ObserveIngress.Close()
		p.Names.Close()
		p.DnsPending.Close()
		return fmt.Errorf("the name-capture ring buffer could not be opened: %w", err)
	}
	o.namesProg, o.namesMap, o.namesLnk, o.namesRd = p.ObserveEgress, p.Names, l, rd
	go o.drainNames()
	// The DNS-latency half: attached separately (ingress is a different attach type from egress), and
	// deliberately not fatal to the egress half above if it fails - query/SNI capture is the more
	// established, more load-bearing half of Names, and should keep working even if this one can't
	// attach for some reason on a given kernel.
	il, err := link.AttachCgroup(link.CgroupOptions{Path: cg, Attach: ebpf.AttachCGroupInetIngress, Program: p.ObserveIngress})
	if err != nil {
		p.ObserveIngress.Close()
		p.DnsPending.Close()
		// Deliberately not returned as this function's own error: query/SNI capture (attached just
		// above) is still running, so NamesErr must stay nil for that success - this failure belongs on
		// its own field instead, see DNSLatencyErr's doc comment.
		o.DNSLatencyErr = fmt.Errorf("the ingress program could not be attached to the root cgroup (%s): %w", cg, err)
		return nil
	}
	o.ingressProg, o.ingressLnk, o.dnsPendingMap = p.ObserveIngress, il, p.DnsPending
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
	rawLength := binary.LittleEndian.Uint32(raw[40:44])

	if kind == nameKindDNSLatency {
		// This kind repurposes the wire field as the computed round-trip in microseconds, not a byte
		// count - see flow.c's NAME_KIND_DNS_LATENCY comment. Read it before the byte-count clamping
		// below, which would otherwise silently cap any real latency past NAME_CAP (1500) microseconds -
		// a case common enough (most real DNS lookups take several milliseconds) that it would corrupt
		// nearly every sample, not just an edge case. There is no payload to parse for this kind at all.
		o.namesMu.Lock()
		if len(o.pendingNames) < maxPendingNames {
			o.pendingNames = append(o.pendingNames, observedName{kind: kind, local: addr(saddr), peer: addr(daddr), port: dport, dnsRttUs: rawLength})
		}
		o.namesMu.Unlock()
		return
	}

	length := rawLength
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
	o.objs.SnatExhaustion.Close()
	o.objs.CpuFreqChangeCount.Close()
	o.objs.ThermalTripCount.Close()
}

func (o *Observer) Close() error {
	if o.iter != nil {
		o.iter.Close()
		o.snap.Close()
	}
	if o.lnk != nil {
		o.lnk.Close()
	}
	if o.snatLnk != nil {
		o.snatLnk.Close()
		o.snatProg.Close()
	}
	if o.cpuFreqLnk != nil {
		o.cpuFreqLnk.Close()
		o.cpuFreqProg.Close()
	}
	if o.thermalLnk != nil {
		o.thermalLnk.Close()
		o.thermalProg.Close()
	}
	if o.namesRd != nil {
		o.namesRd.Close() // unblocks drainNames' Read() loop
		o.namesLnk.Close()
		o.namesProg.Close()
		o.namesMap.Close()
	}
	if o.ingressLnk != nil {
		o.ingressLnk.Close()
		o.ingressProg.Close()
		o.dnsPendingMap.Close()
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

// tlsHandshakeOutcome maps flow.c's own TLS_HANDSHAKE_* constants (flow_val.tls_handshake - see its doc
// comment there) onto the wire-stable continuumv1.TlsHandshakeOutcome enum. The two do not share numeric
// values on purpose: flow.c's constants are this program's own internal detail, free to renumber without
// touching the proto wire format, while the proto enum's numbering is load-bearing (persisted, sent
// between versions) the moment it ships - so this mapping is written out explicitly rather than cast.
func tlsHandshakeOutcome(raw uint8) continuumv1.TlsHandshakeOutcome {
	switch raw {
	case 1: // TLS_HANDSHAKE_OK
		return continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK
	case 2: // TLS_HANDSHAKE_FAILED
		return continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED
	default: // TLS_HANDSHAKE_UNKNOWN (0), or anything this Go build does not yet know about
		return continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_UNKNOWN
	}
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
			sum.RtoRetransmits += v.RtoRetransmits
			sum.SegsOut += v.SegsOut
			sum.BufferDrops += v.BufferDrops
			sum.MeshBypassSyns += v.MeshBypassSyns
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
			// mss_bytes is a gauge too, sampled at the exact same moments as RttUs/Cwnd above - same
			// "0 means no sample" single-writer treatment.
			if v.MssBytes != 0 {
				sum.MssBytes = v.MssBytes
			}
			// RcvWnd/SndWnd/WmemQueued/Sndbuf are gauges too, sampled at the exact same moments as
			// RttUs/Cwnd/MssBytes above - same "0 means no sample" single-writer treatment. See
			// flow.c's own flow_val.rcv_wnd/wmem_queued doc comments for what each actually means.
			if v.RcvWnd != 0 {
				sum.RcvWnd = v.RcvWnd
			}
			if v.SndWnd != 0 {
				sum.SndWnd = v.SndWnd
			}
			if v.WmemQueued != 0 {
				sum.WmemQueued = v.WmemQueued
			}
			if v.Sndbuf != 0 {
				sum.Sndbuf = v.Sndbuf
			}
			// tls_handshake is a gauge too, set at most once per connection (see flow.c's own doc comment on
			// flow_val.tls_handshake) - same "0 means no sample" single-writer treatment, just over the C
			// side's own TLS_HANDSHAKE_* constants rather than RttUs/Cwnd's raw numbers.
			if v.TlsHandshake != 0 {
				sum.TlsHandshake = v.TlsHandshake
			}
			// cgroup_id is a gauge too, set at most once per connection (see flow.c's own doc comment on
			// flow_val.cgroup_id) - same "0 means no sample" treatment, and only ever non-zero on a
			// ROLE_CLIENT row in the first place.
			if v.CgroupId != 0 {
				sum.CgroupId = v.CgroupId
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
		// mesh_bypass_syns is checked here too: note_mesh_bypass (flow.c) can create this very entry on a
		// SYN whose connection has not yet reached ESTABLISHED or failed by the time this window closes
		// (a plausible race, not a rare one - a bypassing SYN to a dead or unreachable peer can sit
		// pending for the whole handshake timeout) - without this, such a window would delete the entry
		// (LookupAndDelete, below) without ever reporting what it held, losing the one signal this
		// collection cycle exists to catch.
		if sum.Connections == 0 && sum.BytesOut == 0 && sum.BytesIn == 0 && sum.FailedAttempts == 0 && sum.MeshBypassSyns == 0 {
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
			RtoRetransmits:    sum.RtoRetransmits,
			RttUs:             sum.RttUs,
			JitterUs:          sum.JitterUs,
			SegsOut:           sum.SegsOut,
			HandshakeUs:       sum.HandshakeUs,
			Cwnd:              sum.Cwnd,
			PacingBps:         sum.PacingBps,
			BufferDrops:       sum.BufferDrops,
			MeshBypassSyns:    sum.MeshBypassSyns,
			MssBytes:          sum.MssBytes,
			RcvWndBytes:       sum.RcvWnd,
			SndWndBytes:       sum.SndWnd,
			WmemQueuedBytes:   sum.WmemQueued,
			SndbufBytes:       sum.Sndbuf,
			TlsHandshake:      tlsHandshakeOutcome(sum.TlsHandshake),
			CgroupId:          sum.CgroupId,
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
		case nameKindDNSLatency:
			rf.Protocol = "udp"
			rf.DnsRttUs = n.dnsRttUs
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
