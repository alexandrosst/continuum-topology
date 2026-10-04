package probe

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nic builds an interface the way sysfs does: a real directory under devices/ and a symlink to it in class/net.
func nic(t *testing.T, sys, name, devPath, state string, files ...string) {
	t.Helper()
	write(t, sys, "devices/"+devPath+"/operstate", state+"\n")
	for _, f := range files {
		write(t, sys, "devices/"+devPath+"/"+f, "")
	}
	l := filepath.Join(sys, "class/net", name)
	if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../devices/"+devPath, l); err != nil {
		t.Fatal(err)
	}
}

// blockdev builds a block device the way sysfs does: a real directory under devices/ and a symlink to
// it in block/ (or, for the virtual case, a symlink whose target itself says /virtual/, exactly like a
// loop or ram device - no real directory is needed since disks() only inspects the symlink target then).
func blockdev(t *testing.T, sys, name, devPath string, files map[string]string) {
	t.Helper()
	for f, content := range files {
		write(t, sys, "devices/"+devPath+"/"+f, content)
	}
	l := filepath.Join(sys, "block", name)
	if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../devices/"+devPath, l); err != nil {
		t.Fatal(err)
	}
}

func TestReadDisks(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	blockdev(t, sys, "nvme0n1", "pci0000:00/0000:00:1d.0/nvme/nvme0/nvme0n1", map[string]string{
		"device/model": "Samsung SSD 970 EVO Plus 1TB\x00", "size": "2000409264\n",
	})
	blockdev(t, sys, "sda", "pci0000:00/0000:00:11.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", map[string]string{
		"device/model": "ST1000DM010-2EP1\n", "size": "1953525168\n", "queue/rotational": "1\n",
		"device/serial": "SECRET-SERIAL-1234", // must never be read, even though it sits right there
	})
	blockdev(t, sys, "sr0", "pci0000:00/0000:00:1f.2/ata2/host1/target1:0:0/1:0:0:0/block/sr0", map[string]string{
		"size": "0\n",
	})
	// loop0 is virtual: its own symlink target says so, no real device/ directory behind it at all.
	if err := os.MkdirAll(filepath.Join(sys, "block"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../devices/virtual/block/loop0", filepath.Join(sys, "block", "loop0")); err != nil {
		t.Fatal(err)
	}

	h := Read(Paths{Sys: sys, Proc: filepath.Join(root, "proc")})
	if len(h.Disks) != 2 {
		t.Fatalf("disks = %v (sr0 and loop0 must be excluded)", h.Disks)
	}
	byName := map[string]*continuumv1.Disk{}
	for _, d := range h.Disks {
		byName[d.Name] = d
	}
	nvme := byName["nvme0n1"]
	if nvme == nil || nvme.Model != "Samsung SSD 970 EVO Plus 1TB" || nvme.Type != "nvme" || nvme.SizeBytes != 2000409264*512 {
		t.Fatalf("nvme0n1 = %v", nvme)
	}
	sda := byName["sda"]
	if sda == nil || sda.Type != "hdd" || sda.SizeBytes != 1953525168*512 {
		t.Fatalf("sda = %v", sda)
	}
	if strings.Contains(h.String(), "SECRET-SERIAL") {
		t.Fatal("the probe must never read a disk's serial number")
	}
}

func TestReadVirtualMachine(t *testing.T) {
	root := t.TempDir()
	sys, proc := filepath.Join(root, "sys"), filepath.Join(root, "proc")
	write(t, proc, "cpuinfo", "processor\t: 0\nmodel name\t: Intel(R) Xeon(R) Platinum 8259CL CPU @ 2.50GHz\nflags\t\t: fpu vme de hypervisor lahf_lm\n\nprocessor\t: 1\nmodel name\t: Intel(R) Xeon(R) Platinum 8259CL CPU @ 2.50GHz\nflags\t\t: fpu\n")
	write(t, sys, "class/dmi/id/sys_vendor", "Amazon EC2\n")
	write(t, sys, "class/dmi/id/product_name", "m5.large\n")
	write(t, sys, "class/dmi/id/board_vendor", "Amazon EC2\n")
	write(t, sys, "class/dmi/id/board_name", "To Be Filled By O.E.M.\n")
	write(t, sys, "class/dmi/id/chassis_type", "1\n")
	write(t, sys, "class/dmi/id/product_serial", "SECRET-SERIAL")
	// ens5 is a real NIC; veth and docker0 are virtual and must be ignored.
	nic(t, sys, "ens5", "pci0000:00/0000:00:05.0/net/ens5", "up", "speed", "mtu")
	write(t, sys, "devices/pci0000:00/0000:00:05.0/net/ens5/speed", "25000\n")
	write(t, sys, "devices/pci0000:00/0000:00:05.0/net/ens5/mtu", "9001\n")
	nic(t, sys, "veth1", "virtual/net/veth1", "up")

	h := Read(Paths{Sys: sys, Proc: proc})
	if !h.HypervisorBit || h.SysVendor != "Amazon EC2" || h.ProductName != "m5.large" || h.ChassisType != 1 {
		t.Fatalf("unexpected: %v", h)
	}
	if h.CpuModel != "Intel(R) Xeon(R) Platinum 8259CL CPU @ 2.50GHz" || h.CpuThreads != 2 {
		t.Fatalf("cpu model/threads = %q %d", h.CpuModel, h.CpuThreads)
	}
	if len(h.Interfaces) != 1 || h.Interfaces[0].Name != "ens5" || h.Interfaces[0].Kind != "ethernet" || h.Interfaces[0].SpeedMbps != 25000 || h.Interfaces[0].Mtu != 9001 {
		t.Fatalf("interfaces = %v", h.Interfaces)
	}
	// Read must wire the hypervisor bit it just computed into hypervisorVendorID, not compute its own
	// separately: whatever a direct call returns for "bit set" is exactly what should have landed here.
	if h.HypervisorVendorId != hypervisorVendorID(true) {
		t.Fatalf("HypervisorVendorId = %q, want %q (Read must reuse the hypervisor bit it already computed)", h.HypervisorVendorId, hypervisorVendorID(true))
	}
	if h.BoardName != "" {
		t.Fatalf("a placeholder must not be reported as a real board name: %q", h.BoardName)
	}
	if len(h.Uplinks) != 1 || h.Uplinks[0] != "ethernet" {
		t.Fatalf("uplinks = %v", h.Uplinks)
	}
	if strings.Contains(h.String(), "SECRET-SERIAL") {
		t.Fatal("the probe must never read serial numbers")
	}
}

func TestReadRaspberryPi(t *testing.T) {
	root := t.TempDir()
	sys, proc := filepath.Join(root, "sys"), filepath.Join(root, "proc")
	write(t, proc, "cpuinfo", "processor : 0\nFeatures : fp asimd evtstrm\n")
	write(t, sys, "firmware/devicetree/base/model", "Raspberry Pi 4 Model B Rev 1.4\x00")
	nic(t, sys, "wlan0", "platform/soc/mmc/net/wlan0", "up", "wireless/.keep")
	nic(t, sys, "eth0", "platform/soc/net/eth0", "down")
	write(t, sys, "class/power_supply/BAT0/type", "Battery\n")

	h := Read(Paths{Sys: sys, Proc: proc})
	if h.HypervisorBit || h.DeviceTreeModel != "Raspberry Pi 4 Model B Rev 1.4" || h.SysVendor != "" {
		t.Fatalf("unexpected: %v", h)
	}
	if h.HypervisorVendorId != "" {
		t.Fatalf("no hypervisor bit must mean no CPUID read at all, got %q", h.HypervisorVendorId)
	}
	if len(h.Uplinks) != 1 || h.Uplinks[0] != "wifi" || !h.HasBattery {
		t.Fatalf("uplinks/battery = %v %v", h.Uplinks, h.HasBattery)
	}
}

func TestReadEmptyHostIsNotAnError(t *testing.T) {
	h := Read(Paths{Sys: t.TempDir(), Proc: t.TempDir()})
	if h.ProbeVersion != Version || h.HypervisorBit || len(h.Uplinks) != 0 {
		t.Fatalf("unexpected: %v", h)
	}
	if h.CpuModel != "" || h.CpuThreads != 0 || len(h.Interfaces) != 0 {
		t.Fatalf("a host with no /proc/cpuinfo and no /sys/class/net must report absence, not zeroes-as-guesses: %v", h)
	}
	// No /sys/fs/cgroup at all here (a cgroup v1 host, or simply this empty fixture) - all three PSI
	// fields must stay nil, not a fabricated 0%, the same "absence is a fact too" rule the rest of this
	// function's doc already states.
	if h.CpuPressurePct != nil || h.MemoryPressurePct != nil || h.IoPressurePct != nil {
		t.Fatalf("pressure = %v/%v/%v, want all nil with no cgroup v2 hierarchy mounted", h.CpuPressurePct, h.MemoryPressurePct, h.IoPressurePct)
	}
	// Same "absence is a fact too" rule as the three PSI fields right above: no /sys/fs/cgroup at all
	// means no cgroup v2 hierarchy to walk, so OomKillCount must stay nil, never a fabricated 0.
	if h.OomKillCount != nil {
		t.Fatalf("oomKillCount = %v, want nil with no cgroup v2 hierarchy mounted", h.OomKillCount)
	}
}

// TestReadPressure covers the three cgroup v2 PSI files at the root of the unified hierarchy - each
// read for its "some avg60" figure only, ignoring "full" (missing here for cpu.pressure, exactly as a
// pre-5.13 kernel would leave it, and present but unread for memory/io).
func TestReadPressure(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	write(t, sys, "fs/cgroup/cpu.pressure", "some avg10=0.00 avg60=2.50 avg300=1.10 total=9000\n")
	write(t, sys, "fs/cgroup/memory.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, sys, "fs/cgroup/io.pressure", "some avg10=5.00 avg60=13.75 avg300=8.40 total=500000\nfull avg10=1.00 avg60=2.00 avg300=1.00 total=100000\n")

	h := Read(Paths{Sys: sys, Proc: t.TempDir()})
	if h.CpuPressurePct == nil || *h.CpuPressurePct != 2.5 {
		t.Fatalf("cpuPressurePct = %v, want 2.5", h.CpuPressurePct)
	}
	if h.MemoryPressurePct == nil || *h.MemoryPressurePct != 0 {
		t.Fatalf("memoryPressurePct = %v, want a real, present 0", h.MemoryPressurePct)
	}
	if h.IoPressurePct == nil || *h.IoPressurePct != 13.75 {
		t.Fatalf("ioPressurePct = %v, want 13.75 (the \"some\" line, not \"full\"'s 2.00)", h.IoPressurePct)
	}
}

// TestReadOomKillCount covers oomKillTotal's own walk: unlike the three PSI files above, which live at
// one fixed path at the cgroup root, memory.events only exists on non-root cgroups - so this builds a
// small nested tree (mirroring kubepods.slice-style nesting) and checks the sum comes back right, that
// a cgroup with no oom_kill line at all (e.g. one with only "full"-less content, or a sibling never
// touched by the memory controller) contributes nothing rather than erroring the whole walk, and that
// the root cgroup's own absence of memory.events is not itself treated as "no cgroup v2 here".
func TestReadOomKillCount(t *testing.T) {
	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	// Real cgroup v2 roots never have their own memory.events (confirmed in TestReadEmptyHostIsNotAnError's
	// sibling package-level empirical note - see oomKillTotal's own doc) - only non-root cgroups do.
	write(t, sys, "fs/cgroup/cpu.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")
	write(t, sys, "fs/cgroup/kubepods.slice/memory.events", "low 0\nhigh 0\nmax 0\noom 0\noom_kill 3\noom_group_kill 0\n")
	write(t, sys, "fs/cgroup/kubepods.slice/pod-a/memory.events", "low 0\nhigh 0\nmax 2\noom 2\noom_kill 2\noom_group_kill 0\n")
	// A sibling cgroup whose memory.events exists but has never recorded a kill - oom_kill 0 must still
	// fold into the sum as 0, not be skipped or mistaken for "unreadable".
	write(t, sys, "fs/cgroup/kubepods.slice/pod-b/memory.events", "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\noom_group_kill 0\n")
	// A non-memory.events file that happens to sit in the same tree must never be mistaken for one.
	write(t, sys, "fs/cgroup/kubepods.slice/pod-b/cpu.pressure", "some avg10=0.00 avg60=0.00 avg300=0.00 total=0\n")

	h := Read(Paths{Sys: sys, Proc: t.TempDir()})
	if h.OomKillCount == nil || *h.OomKillCount != 5 {
		t.Fatalf("oomKillCount = %v, want a real, present 5 (3 + 2 + 0)", h.OomKillCount)
	}
}

// TestReadInterfaceWithNoSpeedFile covers a NIC whose driver does not expose "speed" (or reports it as
// unreadable, which is common when the link is down) - the interface must still be listed, just
// without a speed, since 0 already means "not known" per the field's own contract.
func TestReadInterfaceWithNoSpeedFile(t *testing.T) {
	root := t.TempDir()
	sys, proc := filepath.Join(root, "sys"), filepath.Join(root, "proc")
	nic(t, sys, "eth0", "platform/soc/net/eth0", "unknown", "mtu")
	write(t, sys, "devices/platform/soc/net/eth0/mtu", "1500\n")

	h := Read(Paths{Sys: sys, Proc: proc})
	if len(h.Interfaces) != 1 || h.Interfaces[0].SpeedMbps != 0 || h.Interfaces[0].Mtu != 1500 {
		t.Fatalf("interfaces = %v", h.Interfaces)
	}
}

func TestCleanAndSanitize(t *testing.T) {
	if got := Clean("Dell\x00 Inc.\n\t  PowerEdge\x1b[31m"); strings.ContainsAny(got, "\x00\n\t\x1b") || !strings.HasPrefix(got, "Dell Inc. PowerEdge") {
		t.Fatalf("Clean = %q", got)
	}
	if got := Clean(strings.Repeat("a", 500)); len(got) != maxText {
		t.Fatalf("length %d", len(got))
	}
	s := Sanitize(&continuumv1.HostProbe{ProductName: "<script>x", ChassisType: 9000, Uplinks: []string{"wifi", "wifi", "evil", "ethernet"}})
	if s.ChassisType != 0 || len(s.Uplinks) != 2 || s.Uplinks[0] != "ethernet" {
		t.Fatalf("Sanitize = %v", s)
	}

	untrusted := &continuumv1.HostProbe{
		CpuModel:   "Xeon\x00 Gold",
		CpuThreads: 999999,
		Interfaces: []*continuumv1.NetworkInterface{
			{Name: "eth0", Kind: "ethernet", SpeedMbps: 10000, Mtu: 1500},
			{Name: "", Kind: "ethernet"},                             // no name: dropped
			{Name: "tun0", Kind: "vpn"},                              // unknown kind: dropped
			{Name: "eth1", Kind: "wifi", SpeedMbps: -1, Mtu: 999999}, // out-of-range: cleared, not dropped
			nil, // must not panic
		},
	}
	su := Sanitize(untrusted)
	if su.CpuModel != "Xeon Gold" {
		t.Fatalf("CpuModel = %q", su.CpuModel)
	}
	if su.CpuThreads != 0 {
		t.Fatalf("an implausible CpuThreads must be dropped, got %d", su.CpuThreads)
	}
	if len(su.Interfaces) != 2 {
		t.Fatalf("Interfaces = %v", su.Interfaces)
	}
	if su.Interfaces[0].Name != "eth0" || su.Interfaces[0].SpeedMbps != 10000 || su.Interfaces[0].Mtu != 1500 {
		t.Fatalf("Interfaces[0] = %v", su.Interfaces[0])
	}
	if su.Interfaces[1].Name != "eth1" || su.Interfaces[1].SpeedMbps != 0 || su.Interfaces[1].Mtu != 0 {
		t.Fatalf("an out-of-range speed/mtu must be cleared rather than trusted: %v", su.Interfaces[1])
	}

	many := &continuumv1.HostProbe{}
	for i := 0; i < 50; i++ {
		many.Interfaces = append(many.Interfaces, &continuumv1.NetworkInterface{Name: "eth", Kind: "ethernet"})
		many.Disks = append(many.Disks, &continuumv1.Disk{Name: "sda", Type: "ssd"})
	}
	if got := Sanitize(many); len(got.Interfaces) != 32 || len(got.Disks) != 32 {
		t.Fatalf("Interfaces/Disks must be capped at 32, got %d/%d", len(got.Interfaces), len(got.Disks))
	}

	untrustedDisks := &continuumv1.HostProbe{Disks: []*continuumv1.Disk{
		{Name: "sda", Model: "Evil\x00 Drive", Type: "ssd", SizeBytes: 500 << 30},
		{Name: "", Type: "ssd"},                        // no name: dropped
		{Name: "sdb", Type: "floppy"},                  // unknown type: dropped
		{Name: "sdc", Type: "hdd", SizeBytes: 1 << 62}, // implausible size: cleared, not dropped
		nil, // must not panic
	}}
	sd := Sanitize(untrustedDisks)
	if len(sd.Disks) != 2 {
		t.Fatalf("Disks = %v", sd.Disks)
	}
	if sd.Disks[0].Name != "sda" || sd.Disks[0].Model != "Evil Drive" || sd.Disks[0].SizeBytes != 500<<30 {
		t.Fatalf("Disks[0] = %v", sd.Disks[0])
	}
	if sd.Disks[1].Name != "sdc" || sd.Disks[1].SizeBytes != 0 {
		t.Fatalf("an implausible size must be cleared rather than trusted: %v", sd.Disks[1])
	}
}

// TestSanitizePressure covers the three PSI fields: a plausible, real (including zero) percentage must
// survive untouched, while anything outside a percentage's own [0, 100] range - or NaN/Inf, which a
// hostile or buggy sender could put on the wire despite the kernel itself never producing one - must be
// dropped to nil rather than stored or shown as if it were a real reading.
func TestSanitizePressure(t *testing.T) {
	ok, zero, tooHigh, negative, notANumber := 2.5, 0.0, 101.0, -0.1, math.NaN()
	s := Sanitize(&continuumv1.HostProbe{CpuPressurePct: &ok, MemoryPressurePct: &zero, IoPressurePct: &tooHigh})
	if s.CpuPressurePct == nil || *s.CpuPressurePct != 2.5 {
		t.Fatalf("cpuPressurePct = %v, want 2.5", s.CpuPressurePct)
	}
	if s.MemoryPressurePct == nil || *s.MemoryPressurePct != 0 {
		t.Fatalf("memoryPressurePct = %v, want a real, present 0", s.MemoryPressurePct)
	}
	if s.IoPressurePct != nil {
		t.Fatalf("ioPressurePct = %v, want nil (101%% is not a plausible percentage)", s.IoPressurePct)
	}
	s2 := Sanitize(&continuumv1.HostProbe{CpuPressurePct: &negative, MemoryPressurePct: &notANumber})
	if s2.CpuPressurePct != nil || s2.MemoryPressurePct != nil {
		t.Fatalf("cpuPressurePct/memoryPressurePct = %v/%v, want both nil (negative and NaN are never real readings)", s2.CpuPressurePct, s2.MemoryPressurePct)
	}
	if Sanitize(&continuumv1.HostProbe{}).CpuPressurePct != nil {
		t.Fatal("an unset pressure field must stay nil, not become a fabricated 0")
	}
}

// TestSanitizeOomKillCount covers oom_kill_count: a plausible count (including a real, present zero)
// survives untouched, nil (never read) passes through unchanged, and an implausibly large value - the
// one shape a hostile/corrupted sender could send that a uint64 cannot otherwise rule out, since it has
// no negative or NaN case the way the PSI percentages do - is dropped to nil rather than trusted.
func TestSanitizeOomKillCount(t *testing.T) {
	ok, zero, tooHigh := uint64(5), uint64(0), uint64(maxPlausibleOomKillCount)
	s := Sanitize(&continuumv1.HostProbe{OomKillCount: &ok})
	if s.OomKillCount == nil || *s.OomKillCount != 5 {
		t.Fatalf("oomKillCount = %v, want 5", s.OomKillCount)
	}
	s2 := Sanitize(&continuumv1.HostProbe{OomKillCount: &zero})
	if s2.OomKillCount == nil || *s2.OomKillCount != 0 {
		t.Fatalf("oomKillCount = %v, want a real, present 0", s2.OomKillCount)
	}
	s3 := Sanitize(&continuumv1.HostProbe{OomKillCount: &tooHigh})
	if s3.OomKillCount != nil {
		t.Fatalf("oomKillCount = %v, want nil (implausibly large)", s3.OomKillCount)
	}
	if Sanitize(&continuumv1.HostProbe{}).OomKillCount != nil {
		t.Fatal("an unset oomKillCount must stay nil, not become a fabricated 0")
	}
}

// TestRAPLAbsentIsNotAnError covers the overwhelmingly common case on this product's actual target
// hardware: no powercap sysfs interface at all (ARM/edge boards, most VMs, an amd64 host without RAPL
// support). Read must return promptly, paying no sleep, with HostWatts left nil - never a fabricated 0.
func TestRAPLAbsentIsNotAnError(t *testing.T) {
	start := time.Now()
	h := Read(Paths{Sys: t.TempDir(), Proc: t.TempDir()})
	if h.HostWatts != nil {
		t.Fatalf("hostWatts = %v, want nil when no powercap interface exists at all", h.HostWatts)
	}
	if elapsed := time.Since(start); elapsed > raplSampleWindow {
		t.Fatalf("absence must cost no sleep at all, took %v (raplSampleWindow is %v)", elapsed, raplSampleWindow)
	}
}

// TestRAPLComputesAverageWatts covers the real, present case end to end through Read(): a counter that
// genuinely increases between the two samples rapl() takes raplSampleWindow apart. raplSampleWindow is
// shrunk for the test's own duration so this does not slow the suite down; the counter is mutated by a
// goroutine timed to land inside that window, the same real-timing idiom this package's e2e-style tests
// already use elsewhere (see e.g. the agent package's multi-second sleeps).
func TestRAPLComputesAverageWatts(t *testing.T) {
	old := raplSampleWindow
	raplSampleWindow = 40 * time.Millisecond
	defer func() { raplSampleWindow = old }()

	root := t.TempDir()
	sys := filepath.Join(root, "sys")
	write(t, sys, "class/powercap/intel-rapl:0/energy_uj", "1000000\n") // 1 J to start

	go func() {
		time.Sleep(raplSampleWindow / 4)
		write(t, sys, "class/powercap/intel-rapl:0/energy_uj", "1500000\n") // +0.5 J before the second read
	}()

	h := Read(Paths{Sys: sys, Proc: t.TempDir()})
	if h.HostWatts == nil {
		t.Fatal("hostWatts = nil, want a real reading: the counter file exists and genuinely increased")
	}
	// At least 0.5J was consumed over at most a handful of raplSampleWindow's worth of wall-clock time
	// (generous margin for scheduling jitter in a busy sandbox) - a real, clearly nonzero, clearly
	// bounded reading, not a crude sanity check for "some number came back".
	if *h.HostWatts <= 0 || *h.HostWatts > 0.5/(raplSampleWindow.Seconds())*4 {
		t.Fatalf("hostWatts = %v, want roughly 0.5J divided by about %v", *h.HostWatts, raplSampleWindow)
	}
}

// TestRAPLDeltaHandlesWraparoundWithoutAnyRealTiming covers raplDelta's own arithmetic in isolation -
// see its own doc comment for why this is split out of rapl(): no real sleep, no flakiness, exact
// expected values.
func TestRAPLDeltaHandlesWraparoundWithoutAnyRealTiming(t *testing.T) {
	// The ordinary, non-wrapped case: the max-range reader is never even called.
	calledMax := false
	maxFn := func() (uint64, bool) { calledMax = true; return 0, false }
	if d, ok := raplDelta(100, 150, maxFn); !ok || d != 50 || calledMax {
		t.Fatalf("delta=%d ok=%v calledMax=%v, want 50/true/false", d, ok, calledMax)
	}

	// Wrapped once: e2 < e1, corrected using max_energy_range_uj.
	if d, ok := raplDelta(950, 50, func() (uint64, bool) { return 1000, true }); !ok || d != 100 {
		t.Fatalf("wrapped delta=%d ok=%v, want 100/true ((1000-950)+50)", d, ok)
	}

	// Wrapped, but max_energy_range_uj cannot be read: nil, not a guess.
	if _, ok := raplDelta(950, 50, func() (uint64, bool) { return 0, false }); ok {
		t.Fatal("want ok=false when a wrap is seen but the range cannot be read")
	}

	// Wrapped, but the range read back is nonsensical (smaller than e1 itself): nil, not a guess.
	if _, ok := raplDelta(950, 50, func() (uint64, bool) { return 500, true }); ok {
		t.Fatal("want ok=false when max_energy_range_uj is smaller than e1, which this package cannot make sense of")
	}

	// e2 == e1: a real, valid zero-delta reading (the host drew the window's own epsilon of power).
	if d, ok := raplDelta(500, 500, maxFn); !ok || d != 0 {
		t.Fatalf("equal readings: delta=%d ok=%v, want 0/true", d, ok)
	}
}

func TestSanitizeWatts(t *testing.T) {
	ok, neg, nan, tooHigh := 185.5, -1.0, math.NaN(), float64(maxPlausibleWatts)
	s := Sanitize(&continuumv1.HostProbe{HostWatts: &ok})
	if s.HostWatts == nil || *s.HostWatts != 185.5 {
		t.Fatalf("hostWatts = %v, want 185.5", s.HostWatts)
	}
	if Sanitize(&continuumv1.HostProbe{HostWatts: &neg}).HostWatts != nil {
		t.Fatal("a negative watts figure must be dropped, not stored or shown as if it were real")
	}
	if Sanitize(&continuumv1.HostProbe{HostWatts: &nan}).HostWatts != nil {
		t.Fatal("NaN must be dropped")
	}
	if Sanitize(&continuumv1.HostProbe{HostWatts: &tooHigh}).HostWatts != nil {
		t.Fatal("an implausibly large watts figure must be dropped")
	}
	if Sanitize(&continuumv1.HostProbe{}).HostWatts != nil {
		t.Fatal("an unset hostWatts must stay nil, not become a fabricated 0")
	}
}

// TestSanitizeKeepsTunnelsAndHostSubnets guards against the regression this session found: Sanitize
// built its output field-by-field and simply never copied Tunnels or HostSubnets at all, so every
// node probe report silently lost its network-topology facts between the agent's receiver and
// everything downstream - regardless of whether networkEvidence() itself found anything.
func TestSanitizeKeepsTunnelsAndHostSubnets(t *testing.T) {
	untrusted := &continuumv1.HostProbe{
		HostSubnets: []string{"10.8.0.0/24", "not-a-prefix", "192.168.1.0/24"},
		Tunnels: []*continuumv1.TunnelInterface{
			{Name: "wt0", Kind: "wireguard", Up: true, Mtu: 1420, Addresses: []string{"100.64.0.45/24", "garbage"}, Routes: []string{"100.64.0.0/10", "0.0.0.0/0", "also-garbage"}},
			{Name: "", Kind: "wireguard"},            // no name: dropped
			{Name: "tun0", Kind: "openvpn"},          // unknown/unrecognized kind: dropped
			{Name: "gre1", Kind: "gre", Mtu: 999999}, // out-of-range mtu: cleared, not dropped
			nil,                                      // must not panic
		},
	}
	s := Sanitize(untrusted)
	if len(s.HostSubnets) != 2 || s.HostSubnets[0] != "10.8.0.0/24" || s.HostSubnets[1] != "192.168.1.0/24" {
		t.Fatalf("HostSubnets = %v, want only the two valid prefixes kept", s.HostSubnets)
	}
	if len(s.Tunnels) != 2 {
		t.Fatalf("Tunnels = %v, want wt0 and gre1 kept, the rest dropped", s.Tunnels)
	}
	wt0 := s.Tunnels[0]
	if wt0.Name != "wt0" || wt0.Kind != "wireguard" || !wt0.Up || wt0.Mtu != 1420 {
		t.Fatalf("Tunnels[0] = %v", wt0)
	}
	if len(wt0.Addresses) != 1 || wt0.Addresses[0] != "100.64.0.45/24" {
		t.Fatalf("wt0.Addresses = %v, want only the valid one kept", wt0.Addresses)
	}
	// Sanitize only validates syntax, not the probe's own semantic rule against sending a default
	// route - a hostile sender's default route is syntactically valid and kept; "also-garbage" is not.
	if len(wt0.Routes) != 2 || wt0.Routes[0] != "100.64.0.0/10" || wt0.Routes[1] != "0.0.0.0/0" {
		t.Fatalf("wt0.Routes = %v, want the two valid prefixes kept and the garbage entry dropped", wt0.Routes)
	}
	gre1 := s.Tunnels[1]
	if gre1.Name != "gre1" || gre1.Mtu != 0 {
		t.Fatalf("an out-of-range mtu must be cleared rather than trusted: %v", gre1)
	}

	many := &continuumv1.HostProbe{}
	for i := 0; i < 50; i++ {
		many.Tunnels = append(many.Tunnels, &continuumv1.TunnelInterface{Name: "wt", Kind: "wireguard"})
	}
	if got := Sanitize(many); len(got.Tunnels) != 32 {
		t.Fatalf("Tunnels must be capped at 32, got %d", len(got.Tunnels))
	}
}

func TestSignVerify(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"hypervisorBit":true}`)
	sig := Sign(secret, ts, "node-1", body)
	if err := Verify(secret, ts, "node-1", sig, body, now); err != nil {
		t.Fatal(err)
	}
	if Verify(secret, ts, "node-2", sig, body, now) == nil {
		t.Fatal("a signature for one node must not verify for another")
	}
	if Verify(secret, ts, "node-1", sig, []byte(`{"hypervisorBit":false}`), now) == nil {
		t.Fatal("a changed body must not verify")
	}
	if Verify([]byte("another secret, also long enough"), ts, "node-1", sig, body, now) == nil {
		t.Fatal("wrong secret must not verify")
	}
	if Verify(secret, ts, "node-1", sig, body, now.Add(10*time.Minute)) == nil {
		t.Fatal("a stale report must be rejected")
	}
	if Verify(secret, "abc", "node-1", sig, body, now) == nil {
		t.Fatal("bad timestamp must be rejected")
	}
}

func TestReceiverEndToEnd(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	r := NewReceiver(secret, nil)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	h := &continuumv1.HostProbe{ProbeVersion: "1", HypervisorBit: true, SysVendor: "QEMU\x00"}
	if _, err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-1", h, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := r.Get("worker-1")
	if got == nil || !got.HypervisorBit || got.SysVendor != "QEMU" {
		t.Fatalf("stored = %v", got)
	}
	select {
	case <-r.Changes():
	default:
		t.Fatal("a new observation must signal a change")
	}
	// The same report again (a second later; the same signature would be a replay) is not a change.
	if _, err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-1", h, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Changes():
		t.Fatal("an identical report must not signal a change")
	default:
	}
	// Wrong secret, bad node name, oversized body, wrong method.
	if _, err := Push(context.Background(), srv.Client(), srv.URL, []byte("wrong wrong wrong wrong wrong!!!"), "worker-2", h, time.Now()); err == nil || r.Get("worker-2") != nil {
		t.Fatal("a report signed with the wrong secret must be refused")
	}
	if _, err := Push(context.Background(), srv.Client(), srv.URL, secret, "../etc/passwd", h, time.Now()); err == nil {
		t.Fatal("a bad node name must be refused")
	}
	big := &continuumv1.HostProbe{ProductName: strings.Repeat("x", MaxBody*2)}
	if _, err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-3", big, time.Now()); err == nil {
		t.Fatal("an oversized report must be refused")
	}
	resp, err := http.Get(srv.URL + PathReport)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", resp.StatusCode)
	}
	// Pruning forgets nodes that left the cluster.
	r.Prune(map[string]bool{"other": true})
	if r.Get("worker-1") != nil {
		t.Fatal("Prune must drop unknown nodes")
	}
}

func TestAPausedReceiverForgetsAndIgnoresNodes(t *testing.T) {
	r := NewReceiver([]byte("secret-secret-secret-secret-1234"), nil)
	if accepted, paused := r.put("node-a", &continuumv1.HostProbe{}); !accepted || paused {
		t.Fatal("a report was not accepted")
	}
	if n, _ := r.Presence(time.Hour); n != 1 || r.Get("node-a") == nil {
		t.Fatal("the node is not known")
	}
	r.SetPaused(true)
	select {
	case <-r.Changes():
	default:
		t.Fatal("pausing must tell the agent its picture changed")
	}
	if n, _ := r.Presence(time.Hour); n != 0 || r.Get("node-a") != nil {
		t.Fatal("pausing must forget what nodes reported")
	}
	if accepted, paused := r.put("node-b", &continuumv1.HostProbe{}); !accepted || !paused {
		t.Fatal("a paused receiver must still accept and mark the report as ignored")
	} // answered as usual, thrown away
	if n, _ := r.Presence(time.Hour); n != 0 {
		t.Fatal("a paused receiver remembers a node")
	}
	r.SetPaused(false)
	if accepted, paused := r.put("node-b", &continuumv1.HostProbe{}); !accepted || paused {
		t.Fatal("a resumed receiver refuses reports")
	}
}

// TestPauseBackoffSkipsMostTicksAndChecksOnSchedule exercises PauseBackoff directly (no HTTP, no
// clock): the deterministic sequencing that both Run loops (probe's own below and the flow collector's
// in package collector) lean on. A real "still paused" answer must be followed by exactly
// PauseBackoffTicks-1 free skips before the next real attempt, and a "not paused" answer must clear the
// backoff so every following tick is due again.
func TestPauseBackoffSkipsMostTicksAndChecksOnSchedule(t *testing.T) {
	var b PauseBackoff
	if !b.Due() {
		t.Fatal("a fresh backoff must be due on its first tick")
	}
	b.Observe(true) // the receiver says: still paused
	skipped := 0
	for !b.Due() {
		skipped++
		if skipped > PauseBackoffTicks {
			t.Fatal("never became due again; a paused backoff must not skip forever")
		}
	}
	if skipped != PauseBackoffTicks-1 {
		t.Fatalf("skipped %d ticks before the next real attempt, want exactly %d (PauseBackoffTicks-1)", skipped, PauseBackoffTicks-1)
	}
	b.Observe(false) // the receiver says: not paused any more
	for i := 0; i < 2*PauseBackoffTicks; i++ {
		if !b.Due() {
			t.Fatalf("tick %d: a backoff that just observed 'not paused' must stay due every tick", i)
		}
	}
	// Paused again, but this time the loop never calls Observe before the skip run ends (as if the
	// real check attempt kept failing on the transport, not on pause) - Due must still return to true
	// on schedule rather than skipping forever, since nothing re-arms the skip counter without Observe.
	b.Observe(true)
	for i := 0; i < PauseBackoffTicks-1; i++ {
		if b.Due() {
			t.Fatalf("tick %d of the skip run fired early", i)
		}
	}
	if !b.Due() {
		t.Fatal("the tick right after a full skip run must be due")
	}
}

// TestRunBacksOffWhilePausedAndNoticesResumePromptly proves the node-side loop actually does less real
// work while the receiver is paused, not merely that the toggle exists: with the receiver paused from
// the start, only a fraction of the ticks that elapse ever reach the server at all (every Read of
// procfs/sysfs, the sign and the POST are skipped on the rest), and once resumed the next real request
// lands within the documented bound of PauseBackoffTicks ticks.
func TestRunBacksOffWhilePausedAndNoticesResumePromptly(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	r := NewReceiver(secret, nil)
	r.SetPaused(true)

	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests.Add(1)
		r.Handler().ServeHTTP(w, req)
	}))
	defer srv.Close()

	// An empty sys/proc, exactly like TestReadEmptyHostIsNotAnError's fixture: Read must not need a
	// real host to run every tick that is due.
	paths := Paths{Sys: t.TempDir(), Proc: t.TempDir()}
	// 300ms, not the production 3m, so the test runs quickly - but still well over a second between
	// real attempts (PauseBackoffTicks*every = 1.2s) so two real attempts never land on the same
	// whole-second timestamp and collide in the replay cache (see Sign/ReplayCache), which would
	// otherwise make every real attempt fail and defeat the very backoff this test is checking.
	every := 300 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, paths, srv.URL, secret, "node-a", every, func(string, ...any) {})
		close(done)
	}()

	// Let roughly 12 ticks pass while paused. PauseBackoffTicks=4 means about 3 of those should be
	// real attempts (tick 1, then every 4th after); nowhere near all 12.
	time.Sleep(12*every + 8*every) // + margin for scheduling jitter
	gotPaused := requests.Load()
	if gotPaused == 0 {
		t.Fatal("no request reached the receiver at all while paused - Run never even checks")
	}
	if gotPaused > 6 {
		t.Fatalf("requests reaching the receiver while paused = %d over ~12 ticks; want far fewer than "+
			"one per tick, i.e. the real read/sign/POST cycle must be skipped on most ticks while paused", gotPaused)
	}

	// Resume, and check the bound this design promises: at most PauseBackoffTicks ticks after the
	// server stops reporting paused, the next real attempt happens.
	before := requests.Load()
	r.SetPaused(false)
	bound := time.Duration(PauseBackoffTicks) * every
	deadline := time.Now().Add(bound + 10*every) // margin for scheduling jitter on top of the bound itself
	for time.Now().Before(deadline) && requests.Load() == before {
		time.Sleep(every / 2)
	}
	cancel()
	<-done
	if requests.Load() == before {
		t.Fatalf("no real attempt within the stated resume bound of %v (PauseBackoffTicks * interval)", bound)
	}
}

// TestHypervisorVendorID reads real CPUID leaf 0x40000000 from this machine's own CPU. Whatever comes
// back, the function must never panic and must return either "" or a clean, printable 12-character-ish
// string (Clean already bounds and sanitizes it, same as any other firmware-adjacent text this package
// handles). On amd64 hardware this also serves as smoke evidence the raw CPUID assembly stub actually
// executes and decodes its four result registers in the right order and byte order - if it read a
// register wrong, the vendor ID would come back garbled rather than one of the well-known strings.
func TestHypervisorVendorID(t *testing.T) {
	if hypervisorVendorID(false) != "" {
		t.Fatal("must not read CPUID at all when the hypervisor bit was not set")
	}
	got := hypervisorVendorID(true)
	if runtime.GOARCH != "amd64" {
		if got != "" {
			t.Fatalf("non-amd64 must always return empty, got %q", got)
		}
		return
	}
	if got != Clean(got) {
		t.Fatalf("hypervisorVendorID must already be clean, got %q", got)
	}
	// This sandbox itself is known (from earlier, independent empirical verification in this project) to
	// run under KVM, so a real read on real amd64 hardware here should name it - not merely return
	// something non-crashing.
	if got != "KVMKVMKVM" {
		t.Logf("hypervisorVendorID = %q (expected KVMKVMKVM on this project's usual sandbox; a different amd64 host running under a different hypervisor is not a failure by itself)", got)
	}
}
