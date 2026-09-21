// Package probe reads a few hardware facts from the machine it runs on and carries them, signed, to
// the Continuum agent in the same cluster. It exists because the Kubernetes API cannot tell a VM
// from a bare-metal server; the machine itself can.
//
// The probe only reads. It never reads serial numbers, MAC addresses, UUIDs, disks or processes,
// and it needs no capabilities: sysfs and /proc/cpuinfo are world-readable.
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
	h := &continuumv1.HostProbe{
		ProbeVersion:    Version,
		HypervisorBit:   cpuHypervisorBit(filepath.Join(p.Proc, "cpuinfo")),
		HypervisorType:  Clean(readText(filepath.Join(p.Sys, "hypervisor", "type"))),
		SysVendor:       firmware(readText(filepath.Join(dmi, "sys_vendor"))),
		ProductName:     firmware(readText(filepath.Join(dmi, "product_name"))),
		BoardVendor:     firmware(readText(filepath.Join(dmi, "board_vendor"))),
		BoardName:       firmware(readText(filepath.Join(dmi, "board_name"))),
		BiosVendor:      firmware(readText(filepath.Join(dmi, "bios_vendor"))),
		DeviceTreeModel: Clean(readText(filepath.Join(p.Sys, "firmware", "devicetree", "base", "model"))),
		Uplinks:         uplinks(filepath.Join(p.Sys, "class", "net")),
		HasBattery:      hasBattery(filepath.Join(p.Sys, "class", "power_supply")),
	}
	if n, err := strconv.Atoi(strings.TrimSpace(readText(filepath.Join(dmi, "chassis_type")))); err == nil && n > 0 && n < 64 {
		h.ChassisType = int32(n)
	}
	return h
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

// uplinks lists the kinds of physical network interface that are up. Virtual interfaces (veth,
// bridges, tunnels, VLANs) live under /sys/devices/virtual and are skipped.
func uplinks(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
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
		switch {
		case exists(filepath.Join(dir, name, "wireless")) || exists(filepath.Join(dir, name, "phy80211")):
			seen["wifi"] = true
		case strings.HasPrefix(name, "wwan") || strings.Contains(readText(filepath.Join(dir, name, "uevent")), "DEVTYPE=wwan"):
			seen["cellular"] = true
		default:
			seen["ethernet"] = true
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
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
		ProbeVersion: Clean(h.ProbeVersion), HypervisorBit: h.HypervisorBit, HypervisorType: Clean(h.HypervisorType),
		SysVendor: Clean(h.SysVendor), ProductName: Clean(h.ProductName), BoardVendor: Clean(h.BoardVendor), BoardName: Clean(h.BoardName),
		BiosVendor: Clean(h.BiosVendor), DeviceTreeModel: Clean(h.DeviceTreeModel), HasBattery: h.HasBattery,
	}
	if h.ChassisType > 0 && h.ChassisType < 64 {
		out.ChassisType = h.ChassisType
	}
	seen := map[string]bool{}
	for _, u := range h.Uplinks {
		if (u == "ethernet" || u == "wifi" || u == "cellular") && !seen[u] {
			seen[u] = true
			out.Uplinks = append(out.Uplinks, u)
		}
	}
	sort.Strings(out.Uplinks)
	return out
}
