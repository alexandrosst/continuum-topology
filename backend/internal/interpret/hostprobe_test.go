package interpret

import (
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

type P = continuumv1.HostProbe

func TestNodeKindFromProbe(t *testing.T) {
	cases := []struct {
		name         string
		n            *N
		kind, hw     string
		virt, uplink string
		conf         string
	}{
		{"hypervisor bit on a QEMU guest", node("a", func(n *N) {
			n.Probe = &P{HypervisorBit: true, SysVendor: "QEMU", ProductName: "Standard PC (Q35 + ICH9, 2009)"}
		}), "vm", "", "KVM/QEMU", "", "high"},
		{"hypervisor bit, unknown platform", node("a", func(n *N) { n.Probe = &P{HypervisorBit: true} }), "vm", "", "unidentified hypervisor", "", "high"},
		{"EC2 instance", node("a", func(n *N) { n.Probe = &P{HypervisorBit: true, SysVendor: "Amazon EC2", ProductName: "m5.large"} }), "vm", "", "Amazon EC2", "", "high"},
		{"EC2 metal", node("a", func(n *N) {
			n.Probe = &P{SysVendor: "Amazon EC2", ProductName: "m5.metal", Uplinks: []string{"ethernet"}}
		}), "bare-metal", "Amazon EC2 m5.metal", "", "ethernet", "high"},
		{"Dell server", node("a", func(n *N) {
			n.Probe = &P{SysVendor: "Dell Inc.", ProductName: "PowerEdge R640", ChassisType: 23, Uplinks: []string{"ethernet"}}
		}), "bare-metal", "Dell Inc. PowerEdge R640", "", "ethernet", "high"},
		{"firmware names KVM, CPU hides the bit", node("a", func(n *N) { n.Probe = &P{SysVendor: "QEMU"} }), "vm", "", "KVM/QEMU", "", "medium"},
		{"VMware", node("a", func(n *N) {
			n.Probe = &P{HypervisorBit: true, SysVendor: "VMware, Inc.", ProductName: "VMware Virtual Platform"}
		}), "vm", "", "VMware", "", "high"},
		{"CPUID vendor ID alone, no DMI at all (minimal/hardened cloud image)", node("a", func(n *N) {
			n.Probe = &P{HypervisorBit: true, HypervisorVendorId: "KVMKVMKVM"}
		}), "vm", "", "KVM/QEMU", "", "high"},
		{"CPUID vendor ID disagrees with DMI: the CPU wins", node("a", func(n *N) {
			n.Probe = &P{HypervisorBit: true, HypervisorVendorId: "VMwareVMware", SysVendor: "QEMU"}
		}), "vm", "", "VMware", "", "high"},
		{"unrecognized CPUID vendor ID falls back to DMI", node("a", func(n *N) {
			n.Probe = &P{HypervisorBit: true, HypervisorVendorId: "SomeNewHV12", SysVendor: "QEMU"}
		}), "vm", "", "KVM/QEMU", "", "high"},
		{"Xen via sysfs", node("a", func(n *N) { n.Probe = &P{HypervisorBit: true, HypervisorType: "xen"} }), "vm", "", "Xen", "", "high"},
		{"Raspberry Pi over Wi-Fi", node("a", func(n *N) {
			n.Architecture, n.MemoryCapacityBytes = "arm64", 4<<30
			n.Probe = &P{DeviceTreeModel: "Raspberry Pi 4 Model B Rev 1.4", Uplinks: []string{"wifi"}}
		}), "edge-device", "Raspberry Pi 4 Model B", "", "wifi", "high"},
		{"Jetson", node("a", func(n *N) {
			n.Architecture, n.MemoryCapacityBytes = "arm64", 8<<30
			n.Probe = &P{DeviceTreeModel: "NVIDIA Jetson Orin Nano Developer Kit", Uplinks: []string{"ethernet", "wifi"}}
		}), "edge-device", "NVIDIA Jetson Orin Nano Developer Kit", "", "ethernet", "high"},
		{"big ARM server with a device tree stays bare metal", node("a", func(n *N) {
			n.Architecture, n.MemoryCapacityBytes = "arm64", 256<<30
			n.Probe = &P{DeviceTreeModel: "Ampere Altra Dev Kit"}
		}), "bare-metal", "Ampere Altra Dev Kit", "", "", "high"},
		{"ARM cloud VM identified by firmware", node("a", func(n *N) {
			n.Architecture = "arm64"
			n.Probe = &P{SysVendor: "Amazon EC2", ProductName: "c7g.large"}
		}), "vm", "", "Amazon EC2", "", "high"},
		{"laptop acting as a node", node("a", func(n *N) {
			n.Probe = &P{SysVendor: "LENOVO", ProductName: "ThinkPad T14", ChassisType: 10, HasBattery: true, Uplinks: []string{"wifi"}}
		}), "edge-device", "LENOVO ThinkPad T14", "", "wifi", "medium"},
		{"placeholder firmware, still bare metal", node("a", func(n *N) { n.Probe = &P{} }), "bare-metal", "", "", "", "high"},
	}
	for _, c := range cases {
		r := detectNodeKind(c.n)
		if r.kind != c.kind || r.hardware != c.hw || r.virt != c.virt || r.connectivity != c.uplink || r.kindEv.Confidence != c.conf || !r.probed {
			t.Errorf("%s: got kind=%q hw=%q virt=%q uplink=%q conf=%q probed=%v (%s)", c.name, r.kind, r.hardware, r.virt, r.connectivity, r.kindEv.Confidence, r.probed, r.kindEv.Signal)
		}
	}
}

func TestProbeOverridesWeakerSignalsButIsIgnoredWhenAbsent(t *testing.T) {
	// The cloud providerID says "vm", the probe says the CPU has no hypervisor: the machine wins.
	n := node("a", func(n *N) {
		n.ProviderId = "hrobot://1234"
		n.Probe = &P{HypervisorBit: true, SysVendor: "QEMU"}
	})
	if r := detectNodeKind(n); r.kind != "vm" {
		t.Errorf("probe should decide: %+v", r)
	}
	// An ARM machine the probe could not identify falls back to the API heuristics.
	arm := node("b", func(n *N) {
		n.Architecture, n.MemoryCapacityBytes, n.KernelVersion = "arm64", 4<<30, "5.15.0-1066-raspi"
		n.Probe = &P{ProbeVersion: "1"}
	})
	if r := detectNodeKind(arm); r.kind != "edge-device" || r.probed {
		t.Errorf("a probe with nothing decisive must not block the heuristics: %+v", r)
	}
	// No probe at all: unchanged behaviour, low confidence, and the note tells the user what would settle it.
	if r := detectNodeKind(node("c", nil)); r.kind != "vm" || r.kindEv.Confidence != "low" || r.probed {
		t.Errorf("no probe: %+v", r)
	}
}

func TestInterpretCarriesProbeFacts(t *testing.T) {
	st := k3sFixture()
	first := sortedNodes(st)[0]
	first.Probe = &P{HypervisorBit: true, SysVendor: "QEMU", ProductName: "Standard PC"}
	second := sortedNodes(st)[1]
	second.Probe = &P{SysVendor: "Supermicro", ProductName: "SYS-1029", Uplinks: []string{"ethernet"}, HasBattery: false}
	out := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge", State: st, Now: time.Now()})
	seen := 0
	for _, n := range out.Nodes {
		switch n.Name {
		case first.Name:
			seen++
			if !n.Probed || n.Kind != "vm" || n.Virtualization != "KVM/QEMU" || n.Evidence["virtualization"].Signal == "" || n.Connectivity != "" {
				t.Errorf("vm node = %+v", n)
			}
		case second.Name:
			seen++
			if !n.Probed || n.Kind != "bare-metal" || n.HardwareModel != "Supermicro SYS-1029" || n.Connectivity != "ethernet" || n.Evidence["connectivity"].Signal == "" || n.Virtualization != "" {
				t.Errorf("metal node = %+v", n)
			}
		default:
			if n.Probed {
				t.Errorf("node %s has no probe and must not claim one", n.Name)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("expected both probed nodes in the output, saw %d", seen)
	}
	// Deterministic: same facts, same result.
	again := Interpret(Input{OrgID: "org", AgentID: "ag-1", ClusterID: "cl-x", Name: "edge", State: st, Now: time.Now()})
	for i := range out.Nodes {
		if out.Nodes[i].ID != again.Nodes[i].ID || out.Nodes[i].Kind != again.Nodes[i].Kind {
			t.Fatal("interpretation must be deterministic")
		}
	}
}

func TestKindFromProbeNamesTheStrongestVirtSignal(t *testing.T) {
	// kindFromProbe must name the strongest signal it actually has, in priority order: the CPUID vendor
	// ID (read from the CPU, so it survives a minimal image with blank DMI) beats DMI, which beats a bare
	// hypervisor bit with nothing else to name.
	cases := []struct {
		name string
		p    *P
		want string // substring expected in r.virtEv.Signal
	}{
		{"CPUID vendor ID known: named directly, no DMI needed", &P{HypervisorBit: true, HypervisorVendorId: "KVMKVMKVM"}, "CPUID vendor ID KVMKVMKVM"},
		{"CPUID vendor ID known and DMI also present: CPUID still wins", &P{HypervisorBit: true, HypervisorVendorId: "KVMKVMKVM", SysVendor: "QEMU"}, "CPUID vendor ID KVMKVMKVM"},
		{"no CPUID vendor ID, DMI names one: falls back to DMI", &P{HypervisorBit: true, SysVendor: "QEMU"}, "DMI QEMU"},
		{"neither CPUID nor DMI: just the bare bit", &P{HypervisorBit: true}, "node probe: hypervisor bit"},
	}
	for _, c := range cases {
		p := c.p
		n := node("a", func(n *N) { n.Probe = p })
		r := detectNodeKind(n)
		if r.virtEv == nil {
			t.Errorf("%s: virtEv is nil", c.name)
			continue
		}
		if !strings.Contains(r.virtEv.Signal, c.want) {
			t.Errorf("%s: virtEv.Signal = %q, want it to contain %q", c.name, r.virtEv.Signal, c.want)
		}
	}
}

func TestVMPlatformPrefersCPUIDOverDMI(t *testing.T) {
	cases := []struct {
		name string
		p    *P
		want string
	}{
		{"CPUID only", &P{HypervisorVendorId: "XenVMMXenVMM"}, "Xen"},
		{"CPUID and DMI disagree: CPUID wins", &P{HypervisorVendorId: "VBoxVBoxVBox", SysVendor: "QEMU"}, "VirtualBox"},
		{"unknown CPUID vendor ID: falls through to DMI", &P{HypervisorVendorId: "totally-unknown", ProductName: "VMware Virtual Platform"}, "VMware"},
		{"no CPUID vendor ID: DMI as before", &P{SysVendor: "innotek GmbH"}, "VirtualBox"},
		{"neither: empty", &P{}, ""},
	}
	for _, c := range cases {
		if got := vmPlatform(c.p); got != c.want {
			t.Errorf("%s: vmPlatform = %q, want %q", c.name, got, c.want)
		}
	}
}
