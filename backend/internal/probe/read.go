// Package probe reads a few hardware facts from the machine it runs on and carries them, signed, to
// the Continuum agent in the same cluster. It exists because the Kubernetes API cannot tell a VM
// from a bare-metal server; the machine itself can.
//
// The probe only reads. It never reads serial numbers, MAC addresses, UUIDs or processes, and never a
// disk's own identity (serial, WWN) - only its capacity and type (see Disk). It needs no capabilities:
// sysfs and /proc/cpuinfo are world-readable, and so - confirmed, not just documented - is the netlink
// link/address/route dump networkEvidence() reads to find overlay/tunnel interfaces and host subnets
// (see TunnelInterface and HostProbe.host_subnets).
package probe

import (
	"bufio"
	"io/fs"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	continuumv1 "continuum/gen/continuumv1"
)

// Version is reported with every observation so the server can tell old probes from new ones.
const Version = "1"

// Paths locate the host's sysfs and proc. In the DaemonSet the host's /sys is mounted read-only at
// /host/sys; /proc/cpuinfo is not namespaced, so the container's own /proc is used.
type Paths struct{ Sys, Proc string }

var DefaultPaths = Paths{Sys: "/sys", Proc: "/proc"}

const maxText = 96

// Clean makes firmware-provided text safe to store and show: printable characters only, one line,
// bounded. Firmware strings are untrusted input like any other.
func Clean(s string) string {
	var b strings.Builder
	space := false
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if n >= maxText {
			break
		}
		if r == 0 || unicode.IsControl(r) || !unicode.IsPrint(r) {
			r = ' '
		}
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			space = true
			b.WriteByte(' ')
		} else {
			space = false
			b.WriteRune(r)
		}
		n++
	}
	return strings.TrimSpace(b.String())
}

// placeholders are what board makers leave in DMI when they never filled it in.
var placeholders = map[string]bool{
	"to be filled by o.e.m.": true, "default string": true, "system manufacturer": true, "system product name": true,
	"not specified": true, "none": true, "unknown": true, "n/a": true, "o.e.m.": true, "oem": true, "type1productconfigid": true,
	"system version": true, "base board manufacturer": true, "base board product name": true, "default": true,
}

func firmware(s string) string {
	c := Clean(s)
	if placeholders[strings.ToLower(c)] {
		return ""
	}
	return c
}

func readText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	return strings.TrimRight(string(b), "\x00\n\r ")
}

// Read observes the machine. Fields the machine does not expose stay empty: absence is a fact too
// (a Raspberry Pi has no DMI; a VM has a hypervisor bit).
func Read(p Paths) *continuumv1.HostProbe {
	dmi := filepath.Join(p.Sys, "class", "dmi", "id")
	hyp := cpuHypervisorBit(filepath.Join(p.Proc, "cpuinfo"))
	cpuinfo := filepath.Join(p.Proc, "cpuinfo")
	ifaces := interfaces(filepath.Join(p.Sys, "class", "net"))
	tuns, hostSubnets := networkEvidence()
	h := &continuumv1.HostProbe{
		ProbeVersion:       Version,
		HypervisorBit:      hyp,
		HypervisorVendorId: hypervisorVendorID(hyp),
		HypervisorType:     Clean(readText(filepath.Join(p.Sys, "hypervisor", "type"))),
		SysVendor:          firmware(readText(filepath.Join(dmi, "sys_vendor"))),
		ProductName:        firmware(readText(filepath.Join(dmi, "product_name"))),
		BoardVendor:        firmware(readText(filepath.Join(dmi, "board_vendor"))),
		BoardName:          firmware(readText(filepath.Join(dmi, "board_name"))),
		BiosVendor:         firmware(readText(filepath.Join(dmi, "bios_vendor"))),
		DeviceTreeModel:    Clean(readText(filepath.Join(p.Sys, "firmware", "devicetree", "base", "model"))),
		Uplinks:            uplinkKinds(ifaces),
		Interfaces:         ifaces,
		HasBattery:         hasBattery(filepath.Join(p.Sys, "class", "power_supply")),
		CpuModel:           cpuModel(cpuinfo),
		CpuThreads:         cpuThreads(cpuinfo),
		Disks:              disks(filepath.Join(p.Sys, "block")),
		Tunnels:            tuns,
		HostSubnets:        hostSubnets,
	}
	if n, err := strconv.Atoi(strings.TrimSpace(readText(filepath.Join(dmi, "chassis_type")))); err == nil && n > 0 && n < 64 {
		h.ChassisType = int32(n)
	}
	cgroup := filepath.Join(p.Sys, "fs", "cgroup")
	h.CpuPressurePct = psiSomeAvg60(filepath.Join(cgroup, "cpu.pressure"))
	h.MemoryPressurePct = psiSomeAvg60(filepath.Join(cgroup, "memory.pressure"))
	h.IoPressurePct = psiSomeAvg60(filepath.Join(cgroup, "io.pressure"))
	h.OomKillCount = oomKillTotal(cgroup)
	return h
}

// maxCgroupWalkDepth bounds oomKillTotal's own tree walk below, purely as a self-protective limit
// against a pathologically deep cgroup hierarchy (there is no cgroup.max.depth guarantee this probe can
// rely on) - real cgroup v2 nesting on a Kubernetes node (root -> kubepods.slice -> a pod's own slice ->
// its container scope) never comes close to this.
const maxCgroupWalkDepth = 24

// oomKillTotal returns how many OOM kills cgroup v2's own per-cgroup accounting has recorded across
// every cgroup on this host, right now - a live, monotonically increasing total (the server diffs two
// readings a report-window apart into "this many new kills" the same way it already diffs every other
// cumulative counter this package is not itself responsible for diffing). Unlike the three PSI
// percentages right above, which read one fixed file at the root cgroup, this walks the whole cgroup
// tree (bounded by maxCgroupWalkDepth) summing the "oom_kill" field out of every memory.events file it
// finds: the root cgroup itself has no memory.events at all - confirmed empirically in this sandbox,
// and consistent with the kernel's own cgroup v2 model (the root cgroup has no memory.max of its own to
// ever trigger an OOM kill against; only a non-root cgroup with the memory controller attached gets one)
// - so there is no single root-level file to read the way psiSomeAvg60 reads one. A node-wide sum needs
// no cgroup-path resolution at all, unlike a specific pod's own attribution would (see the PSI commit
// this builds on for why that is a gap this package does not try to close): it never needs to know which
// cgroup belongs to which pod, only to add up every oom_kill field that exists on the host right now.
//
// This is the file-read half of this change's two options (the other being an eBPF fentry/kprobe hook
// on oom_kill_process giving an exact timestamp+pid, see flow.c's SNAT-exhaustion comment for the
// general "internal kernel function, not a stable ABI" tradeoff that route would carry) - chosen because
// it is zero new kernel code, bounded cost (one directory walk per probe read, not a new always-on
// hook), and already gives a real, correlatable node-level fact without guessing at kernel internals
// that could silently break on a future kernel refactor. The cost is precision: this cannot say which
// pod, or when, a kill happened within the probe's own polling interval - see HostProbe.oom_kill_count's
// own doc for what that gap does and does not block.
//
// Returns nil only when no cgroup v2 hierarchy is mounted at cgroupRoot at all (a cgroup v1 host, a
// kernel with cgroups disabled, or the empty-fixture case TestReadEmptyHostIsNotAnError covers) - 0 is a
// real, common reading ("every cgroup walked had 0 oom_kill"), never confused with "not read".
func oomKillTotal(cgroupRoot string) *uint64 {
	if !exists(cgroupRoot) {
		return nil
	}
	var total uint64
	_ = filepath.WalkDir(cgroupRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree (permissions, a cgroup that disappeared mid-walk) - skip it, not fatal
		}
		if d.IsDir() {
			if path != cgroupRoot {
				rel, relErr := filepath.Rel(cgroupRoot, path)
				if relErr == nil && strings.Count(rel, string(filepath.Separator))+1 > maxCgroupWalkDepth {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Name() != "memory.events" {
			return nil
		}
		total += oomKillFromEvents(path)
		return nil
	})
	return &total
}

// oomKillFromEvents reads one cgroup's own memory.events file for its "oom_kill" line (see
// Documentation/cgroup-v2.rst): a cgroup that has never had one still has the line, reading "oom_kill 0",
// which this correctly folds into the running total as 0 - only an unreadable or unrecognized file
// contributes nothing.
func oomKillFromEvents(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		field, ok := strings.CutPrefix(sc.Text(), "oom_kill ")
		if !ok {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(field), 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

// psiSomeAvg60 reads one cgroup v2 pressure-stall file's "some avg60" figure - see
// Documentation/accounting/psi.rst and HostProbe.cpu_pressure_pct's own doc for exactly what that
// number means and why it is read at the root cgroup rather than per-pod. The file has two lines,
// "some" and (for memory/io; cpu lacks it on a kernel older than 5.13) "full", each shaped like:
//
//	some avg10=0.00 avg60=0.00 avg300=0.00 total=0
//
// Returns nil - not a parsed 0 - for anything this probe cannot be sure of: no unified cgroup v2
// hierarchy mounted here at all (a cgroup v1 host, or a kernel too old to have PSI), a permission
// error, or a line this parser does not recognize. The "some" line is read, never "full": full only
// means every task was stalled at once, a stricter and rarer condition this field does not claim to
// measure, and cpu's own "full" line is missing entirely often enough (pre-5.13 kernels) that reading
// it here would make cpu_pressure_pct's own presence depend on kernel version in a way the other two
// fields' does not.
func psiSomeAvg60(path string) *float64 {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		rest, ok := strings.CutPrefix(line, "some ")
		if !ok {
			continue
		}
		for _, field := range strings.Fields(rest) {
			v, ok := strings.CutPrefix(field, "avg60=")
			if !ok {
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil
			}
			return &n
		}
		return nil
	}
	return nil
}

// cpuModel reads the CPU model name the kernel reports (e.g. "Intel(R) Xeon(R) Platinum ..."). It is
// the same for every logical CPU, so the first line decides. This is the real host's CPU, unlike
// NodeFacts' Kubernetes-visible millicore capacity, which a cgroup limit can shrink well below it.
func cpuModel(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "model name") {
			continue
		}
		_, after, ok := strings.Cut(line, ":")
		if !ok {
			return ""
		}
		return Clean(after)
	}
	return ""
}

// cpuThreads counts the logical CPUs (hardware threads) the kernel sees: one "processor" line per
// thread in /proc/cpuinfo. Like cpuModel, this reflects the real host regardless of any cgroup quota.
func cpuThreads(path string) int32 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var n int32
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "processor") {
			n++
		}
	}
	return n
}

// cpuHypervisorBit reports whether the first CPU's flags contain "hypervisor" (x86 CPUID leaf 1, bit 31).
func cpuHypervisorBit(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "flags") {
			continue
		}
		_, after, ok := strings.Cut(line, ":")
		if !ok {
			return false
		}
		for _, fl := range strings.Fields(after) {
			if fl == "hypervisor" {
				return true
			}
		}
		return false // flags are the same on every CPU; the first line decides
	}
	return false
}

// interfaces lists the physical network interfaces that are up, with whatever speed and MTU sysfs
// reports for each. Virtual interfaces (veth, bridges, tunnels, VLANs) live under
// /sys/devices/virtual and are skipped. Never carries an address: this package never reads MACs.
func interfaces(dir string) []*continuumv1.NetworkInterface {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*continuumv1.NetworkInterface
	for _, e := range ents {
		name := e.Name()
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || strings.Contains(target, "/virtual/") || name == "lo" {
			continue
		}
		state := readText(filepath.Join(dir, name, "operstate"))
		if state != "up" && state != "unknown" {
			continue
		}
		var kind string
		switch {
		case exists(filepath.Join(dir, name, "wireless")) || exists(filepath.Join(dir, name, "phy80211")):
			kind = "wifi"
		case strings.HasPrefix(name, "wwan") || strings.Contains(readText(filepath.Join(dir, name, "uevent")), "DEVTYPE=wwan"):
			kind = "cellular"
		default:
			kind = "ethernet"
		}
		iface := &continuumv1.NetworkInterface{Name: Clean(name), Kind: kind}
		// speed_mbps: absent, unreadable, or -1 (no link) all mean "not known"; 0 says that, not "no link".
		if n, err := strconv.Atoi(strings.TrimSpace(readText(filepath.Join(dir, name, "speed")))); err == nil && n > 0 {
			iface.SpeedMbps = int32(n)
		}
		if n, err := strconv.Atoi(strings.TrimSpace(readText(filepath.Join(dir, name, "mtu")))); err == nil && n > 0 {
			iface.Mtu = int32(n)
		}
		out = append(out, iface)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// uplinkKinds derives the legacy kind-only uplinks list from the richer interfaces list, kept for
// consumers that only ever cared about which kinds of link a node has.
func uplinkKinds(ifaces []*continuumv1.NetworkInterface) []string {
	seen := map[string]bool{}
	for _, i := range ifaces {
		seen[i.Kind] = true
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// disks lists the physical block devices found under /sys/block, with whatever capacity and type
// sysfs reports for each. Virtual block devices (loop, ram, zram, device-mapper/LVM) live under
// /sys/devices/virtual and are skipped, the same way virtual network interfaces are; optical drives
// (sr*) are skipped too, since they are not storage capacity in any useful sense here. Never reads a
// disk's serial, WWN or any other per-disk identifier - see the package doc.
func disks(dir string) []*continuumv1.Disk {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []*continuumv1.Disk
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, "sr") {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, name))
		if err != nil || strings.Contains(target, "/virtual/") {
			continue
		}
		d := &continuumv1.Disk{Name: Clean(name)}
		d.Model = firmware(readText(filepath.Join(dir, name, "device", "model")))
		if n, err := strconv.ParseInt(strings.TrimSpace(readText(filepath.Join(dir, name, "size"))), 10, 64); err == nil && n > 0 {
			d.SizeBytes = n * 512 // sysfs always reports size in 512-byte sectors, regardless of the real sector size
		}
		switch {
		case strings.HasPrefix(name, "nvme"):
			d.Type = "nvme"
		case readText(filepath.Join(dir, name, "queue", "rotational")) == "0":
			d.Type = "ssd"
		case readText(filepath.Join(dir, name, "queue", "rotational")) == "1":
			d.Type = "hdd"
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func hasBattery(dir string) bool {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range ents {
		if strings.EqualFold(readText(filepath.Join(dir, e.Name(), "type")), "Battery") {
			return true
		}
	}
	return false
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// maxPlausibleOomKillCount bounds oom_kill_count the same way sanitizePressurePct bounds a percentage:
// a real host restarts long before a legitimate cumulative OOM-kill count could ever reach this, so
// anything at or beyond it is far more likely a hostile or corrupted sender than a real reading.
const maxPlausibleOomKillCount = 10_000_000

// sanitizeOomKillCount keeps a sent oom_kill_count only when it is at least plausible - see
// maxPlausibleOomKillCount. nil (never read, or a cgroup v1 host) passes through unchanged: unlike the
// percentages below, there is no "negative" or "NaN" case for a uint64 to guard against, only an
// implausibly large one.
func sanitizeOomKillCount(v *uint64) *uint64 {
	if v == nil || *v >= maxPlausibleOomKillCount {
		return nil
	}
	n := *v
	return &n
}

// sanitizePressurePct keeps a sent cpu_pressure_pct/memory_pressure_pct/io_pressure_pct only when it is
// a plausible percentage - PSI's own avg10/avg60/avg300 are each bounded to [0, 100] by the kernel, so
// anything outside that (or NaN/Inf, which a hostile or buggy sender could still put on the wire despite
// the kernel itself never producing one) is dropped rather than stored or shown as if it were real.
func sanitizePressurePct(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 || *v > 100 {
		return nil
	}
	n := *v
	return &n
}

// Sanitize bounds and cleans an observation received from the network, so a hostile or buggy sender
// cannot store anything but short printable strings and known values.
func Sanitize(h *continuumv1.HostProbe) *continuumv1.HostProbe {
	if h == nil {
		return nil
	}
	out := &continuumv1.HostProbe{
		ProbeVersion: Clean(h.ProbeVersion), HypervisorBit: h.HypervisorBit, HypervisorVendorId: Clean(h.HypervisorVendorId), HypervisorType: Clean(h.HypervisorType),
		SysVendor: Clean(h.SysVendor), ProductName: Clean(h.ProductName), BoardVendor: Clean(h.BoardVendor), BoardName: Clean(h.BoardName),
		BiosVendor: Clean(h.BiosVendor), DeviceTreeModel: Clean(h.DeviceTreeModel), HasBattery: h.HasBattery,
		CpuModel: Clean(h.CpuModel),
	}
	if h.ChassisType > 0 && h.ChassisType < 64 {
		out.ChassisType = h.ChassisType
	}
	if h.CpuThreads > 0 && h.CpuThreads <= 4096 {
		out.CpuThreads = h.CpuThreads
	}
	out.CpuPressurePct = sanitizePressurePct(h.CpuPressurePct)
	out.MemoryPressurePct = sanitizePressurePct(h.MemoryPressurePct)
	out.IoPressurePct = sanitizePressurePct(h.IoPressurePct)
	out.OomKillCount = sanitizeOomKillCount(h.OomKillCount)
	seen := map[string]bool{}
	for _, u := range h.Uplinks {
		if (u == "ethernet" || u == "wifi" || u == "cellular") && !seen[u] {
			seen[u] = true
			out.Uplinks = append(out.Uplinks, u)
		}
	}
	sort.Strings(out.Uplinks)
	// Interfaces: bound the count, drop anything with no name or an unrecognized kind, and cap
	// speed/MTU to plausible ranges. A hostile or buggy sender gets none of this taken on faith.
	for _, iface := range h.Interfaces {
		if iface == nil || len(out.Interfaces) >= 32 {
			continue
		}
		name := Clean(iface.Name)
		if name == "" || (iface.Kind != "ethernet" && iface.Kind != "wifi" && iface.Kind != "cellular") {
			continue
		}
		ni := &continuumv1.NetworkInterface{Name: name, Kind: iface.Kind}
		if iface.SpeedMbps > 0 && iface.SpeedMbps <= 1_000_000 {
			ni.SpeedMbps = iface.SpeedMbps
		}
		if iface.Mtu > 0 && iface.Mtu <= 65536 {
			ni.Mtu = iface.Mtu
		}
		out.Interfaces = append(out.Interfaces, ni)
	}
	// Disks: bound the count, drop anything with no name or an unrecognized type, and cap size to a
	// plausible range. Model is cosmetic text like any other firmware string - Clean is enough for it.
	for _, d := range h.Disks {
		if d == nil || len(out.Disks) >= 32 {
			continue
		}
		name := Clean(d.Name)
		if name == "" || (d.Type != "" && d.Type != "hdd" && d.Type != "ssd" && d.Type != "nvme") {
			continue
		}
		nd := &continuumv1.Disk{Name: name, Model: Clean(d.Model), Type: d.Type}
		if d.SizeBytes > 0 && d.SizeBytes <= 1<<60 { // 1 EiB: generous headroom, not a real disk size
			nd.SizeBytes = d.SizeBytes
		}
		out.Disks = append(out.Disks, nd)
	}
	// HostSubnets: bound the count and keep only syntactically valid prefixes - same discipline as
	// everything else here.
	for _, s := range h.HostSubnets {
		if len(out.HostSubnets) >= 32 {
			break
		}
		if _, err := netip.ParsePrefix(s); err == nil {
			out.HostSubnets = append(out.HostSubnets, s)
		}
	}
	// Tunnels: bound the count, drop anything with no name or a kind this probe does not itself
	// recognize (overlayKinds, the same allow-list buildTunnels uses), and bound/validate addresses
	// and routes the same way - the exact same "nothing taken on faith" treatment Interfaces/Disks
	// get above. This was missing entirely until now: Tunnels and HostSubnets reached this function
	// on the wire but were never copied into out, so they were silently dropped on every report,
	// regardless of whether the probe's own netlink read found anything.
	for _, t := range h.Tunnels {
		if t == nil || len(out.Tunnels) >= 32 {
			continue
		}
		name := Clean(t.Name)
		if name == "" || !overlayKinds[t.Kind] {
			continue
		}
		nt := &continuumv1.TunnelInterface{Name: name, Kind: t.Kind, Up: t.Up}
		if t.Mtu > 0 && t.Mtu <= 65536 {
			nt.Mtu = t.Mtu
		}
		for _, a := range t.Addresses {
			if len(nt.Addresses) >= 32 {
				break
			}
			if _, err := netip.ParsePrefix(a); err == nil {
				nt.Addresses = append(nt.Addresses, a)
			}
		}
		for _, rt := range t.Routes {
			if len(nt.Routes) >= maxTunnelRoutes {
				break
			}
			if _, err := netip.ParsePrefix(rt); err == nil {
				nt.Routes = append(nt.Routes, rt)
			}
		}
		out.Tunnels = append(out.Tunnels, nt)
	}
	return out
}
