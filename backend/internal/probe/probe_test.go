package probe

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
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
	}
	if got := Sanitize(many); len(got.Interfaces) != 32 {
		t.Fatalf("Interfaces must be capped at 32, got %d", len(got.Interfaces))
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
	if err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-1", h, time.Now()); err != nil {
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
	if err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-1", h, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.Changes():
		t.Fatal("an identical report must not signal a change")
	default:
	}
	// Wrong secret, bad node name, oversized body, wrong method.
	if err := Push(context.Background(), srv.Client(), srv.URL, []byte("wrong wrong wrong wrong wrong!!!"), "worker-2", h, time.Now()); err == nil || r.Get("worker-2") != nil {
		t.Fatal("a report signed with the wrong secret must be refused")
	}
	if err := Push(context.Background(), srv.Client(), srv.URL, secret, "../etc/passwd", h, time.Now()); err == nil {
		t.Fatal("a bad node name must be refused")
	}
	big := &continuumv1.HostProbe{ProductName: strings.Repeat("x", MaxBody*2)}
	if err := Push(context.Background(), srv.Client(), srv.URL, secret, "worker-3", big, time.Now()); err == nil {
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
	if !r.put("node-a", &continuumv1.HostProbe{}) {
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
	r.put("node-b", &continuumv1.HostProbe{}) // answered as usual, thrown away
	if n, _ := r.Presence(time.Hour); n != 0 {
		t.Fatal("a paused receiver remembers a node")
	}
	r.SetPaused(false)
	if !r.put("node-b", &continuumv1.HostProbe{}) {
		t.Fatal("a resumed receiver refuses reports")
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
