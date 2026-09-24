//go:build !amd64

package probe

// hypervisorVendorID is amd64-only. ARM has no equivalent of x86's CPUID leaf 0x40000000 (its own
// hypervisor discovery goes through PSCI/SMCCC calls instead, a different mechanism this probe does not
// use), and detectNodeKind already has independent, well-tested ARM signals: the device tree and, where
// present, DMI. Always "" here, same as any other fact this machine does not expose.
func hypervisorVendorID(hypervisorBit bool) string { return "" }
