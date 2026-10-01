package ebpf

// The compiled programs (flow_x86_bpfel.o, flow_arm64_bpfel.o) are committed, so building the collector
// needs no clang. Regenerate after editing flow.c:
//
//	go generate ./internal/flow/ebpf
//
// -type name_event is required even though nothing in this package constructs one directly (observer_bpf.go
// decodes ring buffer records into it) - without this flag bpf2go silently drops the Go binding for it
// entirely (confirmed empirically: this flag was missing from this exact comment for a while, and running
// the command as literally written here regenerates flow_{x86,arm64}_bpfel.go with struct name_event gone,
// not merely out of date - flow.c's own "unused_name_event" global keeps clang from stripping the type's
// BTF, but that only makes the type visible to bpf2go, it does not by itself ask bpf2go to generate a Go
// struct for it; only an explicit -type flag does that).
//
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags "-O2 -g -Wall -Werror" -target amd64,arm64 -type flow_key -type flow_val -type name_event flow flow.c -- -Iheaders
