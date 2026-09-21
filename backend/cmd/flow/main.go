// Command flow is the optional node flow collector. It counts TCP connections and, where the kernel
// allows, the bytes they carried, and reports the counts to the Continuum agent, which turns addresses
// into workloads. It never reads packets or payloads and keeps nothing but counters.
//
// Method: eBPF where the kernel supports it, the kernel's connection-tracking table otherwise. Run it
// with --print 30s to see exactly what a window contains; that sends nothing.
//
// It is a thin wrapper: the same code is `continuum flow` in the single Continuum binary (cmd/continuum), which is what
// the container image and the Helm chart run. This binary keeps the old name working for development and tests.
package main

import (
	"os"

	"continuum/internal/cli"
	flowcli "continuum/internal/cli/flow"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(flowcli.Main(os.Args[1:]))
}
