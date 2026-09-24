package interpret

import (
	"regexp"
	"strings"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/model"
)

// vmSignatures map what virtual machines write into firmware to the platform's name. They are
// matched, lower-cased, against sys_vendor, product_name, board_vendor and bios_vendor.
var vmSignatures = []struct{ match, name string }{
	{"amazon ec2", "Amazon EC2"},
	{"google compute engine", "Google Compute Engine"},
	{"microsoft corporation virtual machine", "Hyper-V"},
	{"virtual machine", "Hyper-V"},
	{"vmware", "VMware"},
	{"virtualbox", "VirtualBox"},
	{"innotek", "VirtualBox"},
	{"openstack", "OpenStack (KVM)"},
	{"digitalocean", "DigitalOcean Droplet"},
	{"alibaba cloud", "Alibaba Cloud ECS"},
	{"oraclecloud", "Oracle Cloud"},
	{"parallels", "Parallels"},
	{"apple virtualization", "Apple Virtualization"},
	{"bochs", "Bochs"},
	{"xen", "Xen"},
	{"qemu", "KVM/QEMU"},
	{"kvm", "KVM/QEMU"},
	{"firecracker", "Firecracker"},
	{"cloud hypervisor", "Cloud Hypervisor"},
}

// cpuidHypervisorNames maps CPUID leaf 0x40000000's vendor ID (see HostProbe.hypervisor_vendor_id) to a
// hypervisor name - the exact strings the Linux kernel and systemd-detect-virt themselves hardcode
// (arch/x86/kernel/cpu/{kvm,vmware}.c and hyperv/hv_common.c; already cleaned of trailing NULs/padding by
// probe.Clean the same way any other firmware-adjacent string here is). Checked before the DMI table
// below: it is read from the CPU directly rather than through sysfs, so it cannot be blank or stripped
// on a minimal or hardened cloud image the way sys_vendor/product_name sometimes are. A hypervisor absent
// from this table (or one that hides the leaf on purpose) still falls through to the DMI match.
var cpuidHypervisorNames = map[string]string{
	"KVMKVMKVM":    "KVM/QEMU",
	"TCGTCGTCGTCG": "QEMU (TCG)",
	"VMwareVMware": "VMware",
	"Microsoft Hv": "Hyper-V",
	"XenVMMXenVMM": "Xen",
	"VBoxVBoxVBox": "VirtualBox",
	"prl hyperv":   "Parallels",
	"bhyve bhyve":  "bhyve",
	"ACRNACRNACRN": "ACRN",
}

func vmPlatform(h *continuumv1.HostProbe) string {
	if name, ok := cpuidHypervisorNames[h.HypervisorVendorId]; ok {
		return name
	}
	hay := strings.ToLower(strings.Join([]string{h.SysVendor, h.ProductName, h.BoardVendor, h.BiosVendor}, " | "))
	// "Microsoft Corporation" with "Virtual Machine" is Hyper-V; a Surface tablet is not.
	for _, s := range vmSignatures {
		if strings.Contains(hay, s.match) {
			return s.name
		}
	}
	if strings.EqualFold(h.HypervisorType, "xen") {
		return "Xen"
	}
	return ""
}

// Single-board computers announce themselves in the device tree.
var boardWords = regexp.MustCompile(`(?i)raspberry pi|jetson|orange ?pi|rock ?pi|radxa|beaglebone|banana ?pi|odroid|khadas|libre computer|nano ?pi|pine64|rockchip|allwinner`)
var revSuffix = regexp.MustCompile(`(?i)\s+rev(ision)?\s+[0-9.]+$`)

// Chassis types that mean "portable or tiny": notebook, laptop, handheld, tablet, mini PC, stick PC.
var portableChassis = map[int32]bool{8: true, 9: true, 10: true, 11: true, 14: true, 30: true, 31: true, 32: true, 35: true, 36: true}

func machineModel(h *continuumv1.HostProbe) string {
	vendor, product := firstNonEmpty(h.SysVendor, h.BoardVendor), firstNonEmpty(h.ProductName, h.BoardName)
	switch {
	case product == "":
		return vendor
	case vendor == "" || strings.HasPrefix(strings.ToLower(product), strings.ToLower(vendor)):
		return product
	}
	return vendor + " " + product
}

func uplinkOf(h *continuumv1.HostProbe) (string, *model.Evidence) {
	if len(h.Uplinks) == 0 {
		return "", nil
	}
	set := map[string]bool{}
	for _, u := range h.Uplinks {
		set[u] = true
	}
	best := ""
	for _, u := range []string{"ethernet", "wifi", "cellular"} {
		if set[u] {
			best = u
			break
		}
	}
	e := ev("node probe: physical interfaces up: "+strings.Join(h.Uplinks, ", "), "high", "")
	return best, &e
}

// kindFromProbe decides vm / bare-metal / edge-device from what the machine reports about itself.
// It returns ok=false when the probe saw nothing decisive (for example an ARM machine with neither
// firmware tables nor a device tree), and the API-based heuristics then decide.
func kindFromProbe(n *continuumv1.NodeFacts, apiHardware string, apiHwEv *model.Evidence) (nodeKindResult, bool) {
	h := n.Probe
	arm := strings.HasPrefix(n.Architecture, "arm")
	platform := vmPlatform(h)
	metal := strings.Contains(strings.ToLower(h.SysVendor), "amazon ec2") && strings.Contains(strings.ToLower(h.ProductName), ".metal")
	fw := machineModel(h)
	r := nodeKindResult{hardware: apiHardware, hwEv: apiHwEv, probed: true, battery: h.HasBattery}
	virtEv := func(signal, conf, detail string) *model.Evidence { e := ev(signal, conf, detail); return &e }

	switch {
	case metal:
		r.kind, r.kindEv = "bare-metal", ev("node probe: DMI "+h.SysVendor+" "+h.ProductName, "high", "an AWS .metal instance runs without a hypervisor")
	case h.HypervisorBit:
		r.kind = "vm"
		r.virt = firstNonEmpty(platform, "unidentified hypervisor")
		r.kindEv = ev("node probe: CPU reports a hypervisor", "high", "the CPUID hypervisor bit is set")
		// Name the strongest signal actually available, in the order vmPlatform trusts them: the CPUID
		// vendor ID (read from the CPU, so it survives a minimal image that leaves DMI blank), then DMI,
		// then just the bare hypervisor bit when neither named anything.
		signal := "hypervisor bit"
		if _, ok := cpuidHypervisorNames[h.HypervisorVendorId]; ok {
			signal = "CPUID vendor ID " + h.HypervisorVendorId
		} else if fw != "" {
			signal = "DMI " + fw
		}
		r.virtEv = virtEv("node probe: "+signal, "high", "")
	case platform != "":
		r.kind, r.virt = "vm", platform
		conf, detail := "high", "the firmware identifies a virtual machine"
		if !arm { // an x86 CPU that hides the hypervisor bit while the firmware names one is unusual
			conf, detail = "medium", "firmware names a hypervisor but the CPU hides its hypervisor bit"
		}
		r.kindEv = ev("node probe: DMI "+fw, conf, detail)
		r.virtEv = virtEv("node probe: DMI "+fw, conf, "")
	case !arm:
		r.kind = "bare-metal"
		detail := "no hypervisor bit and firmware does not name a hypervisor"
		r.kindEv = ev("node probe: no hypervisor bit on the CPU", "high", detail)
	case h.DeviceTreeModel != "":
		r.kind = "bare-metal"
		r.kindEv = ev("node probe: device tree "+h.DeviceTreeModel, "high", "a board described by a device tree, no hypervisor named")
	case h.SysVendor != "" || h.ProductName != "":
		r.kind = "bare-metal"
		r.kindEv = ev("node probe: DMI "+fw, "medium", "ARM has no hypervisor bit; firmware does not name a hypervisor")
	default:
		return nodeKindResult{}, false
	}

	if r.kind == "vm" {
		return r, true
	}

	// Bare metal: name the hardware, decide whether it is an edge device, and record how it connects.
	switch {
	case h.DeviceTreeModel != "":
		model := revSuffix.ReplaceAllString(h.DeviceTreeModel, "")
		r.hardware = model
		e := ev("node probe: device-tree model", "high", h.DeviceTreeModel)
		r.hwEv = &e
		conf := "medium"
		if boardWords.MatchString(h.DeviceTreeModel) {
			conf = "high"
		}
		if memGB(n.MemoryCapacityBytes) <= 32 {
			r.kind = "edge-device"
			r.kindEv = ev("node probe: device tree "+h.DeviceTreeModel, conf, "single-board computer")
		}
	case fw != "":
		r.hardware = fw
		e := ev("node probe: DMI sys_vendor and product_name", "high", "")
		r.hwEv = &e
	}
	if r.kind == "bare-metal" && (portableChassis[h.ChassisType] || h.HasBattery) {
		why := "battery present"
		if portableChassis[h.ChassisType] {
			why = "portable or mini-PC chassis"
		}
		r.kind = "edge-device"
		r.kindEv = ev("node probe: "+why, "medium", "a laptop or mini PC acting as a node; confirm")
	}
	r.connectivity, r.connEv = uplinkOf(h)
	return r, true
}
