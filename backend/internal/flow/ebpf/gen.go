package ebpf

// The compiled programs (flow_x86_bpfel.o, flow_arm64_bpfel.o) are committed, so building the collector
// needs no clang. Regenerate after editing flow.c:
//
//	go generate ./internal/flow/ebpf
//
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -cc clang -cflags "-O2 -g -Wall -Werror" -target amd64,arm64 -type flow_key -type flow_val flow flow.c -- -Iheaders
