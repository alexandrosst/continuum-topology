// Package probe reads a few hardware facts from the machine it runs on and carries them, signed, to
// the Continuum agent in the same cluster. It exists because the Kubernetes API cannot tell a VM
// from a bare-metal server; the machine itself can.
//
// The probe only reads. It never reads serial numbers, MAC addresses, UUIDs or processes, and never a
// disk's own identity (serial, WWN) - only its capacity and type (see Disk). It needs no capabilities:
// sysfs and /proc/cpuinfo are world-readable.
package probe

import (
	"bufio"
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
	}
	if n, err := strconv.Atoi(strings.TrimSpace(readText(filepath.Join(dmi, "chassis_type")))); err == nil && n > 0 && n < 64 {
		h.ChassisType = int32(n)
	}
	return h
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
	return out
}
