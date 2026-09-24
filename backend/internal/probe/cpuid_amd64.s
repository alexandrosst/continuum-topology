// The CPUID instruction has always been executable in unprivileged usermode on x86 - no syscall, no
// capability, nothing sysfs needs to expose - which is what lets this probe read the hypervisor's own
// vendor ID without any elevated access. See cpuid_amd64.go.

#include "textflag.h"

// func cpuidLeaf(eax uint32) (a, b, c, d uint32)
TEXT ·cpuidLeaf(SB), NOSPLIT, $0-24
	MOVL eax+0(FP), AX
	XORL CX, CX
	CPUID
	MOVL AX, a+8(FP)
	MOVL BX, b+12(FP)
	MOVL CX, c+16(FP)
	MOVL DX, d+20(FP)
	RET
