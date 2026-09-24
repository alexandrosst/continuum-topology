//go:build amd64

package probe

// cpuidLeaf executes the CPUID instruction directly (cpuid_amd64.s) and returns its four result
// registers for the given eax input, ecx left at 0 (no probe here reads a sub-leaf).
func cpuidLeaf(eax uint32) (a, b, c, d uint32)

// hypervisorVendorID reads CPUID leaf 0x40000000, the 12-character vendor ID string every mainstream
// hypervisor (KVM, VMware, Hyper-V, Xen, VirtualBox, and others) is expected to answer the moment leaf
// 1's hypervisor-present bit is set - the same signal systemd-detect-virt and virt-what use to name a
// hypervisor. It is read directly from the CPU rather than through any file, so unlike sys_vendor,
// product_name and bios_vendor it cannot be blank or stripped on a minimal or hardened cloud image.
// Only called when hypervisorBit is already true (cpuHypervisorBit, from /proc/cpuinfo): reading this
// leaf on real hardware, where a hypervisor never claimed it, is meaningless and CPU-model-dependent.
// Returns "" if the hypervisor set the bit but left the leaf empty - unusual, and interpret.go's DMI
// matching is the fallback for exactly that case.
func hypervisorVendorID(hypervisorBit bool) string {
	if !hypervisorBit {
		return ""
	}
	_, b, c, d := cpuidLeaf(0x40000000)
	buf := make([]byte, 12)
	put := func(dst []byte, v uint32) {
		dst[0], dst[1], dst[2], dst[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	}
	put(buf[0:4], b)
	put(buf[4:8], c)
	put(buf[8:12], d)
	return Clean(string(buf))
}
