// Command probe is the optional node probe: a tiny read-only program that runs on every node
// (as a DaemonSet) and tells the Ikhnos agent what kind of machine it is. Run it with --print
// to see exactly what it reads; there is nothing else.
//
// It is a thin wrapper: the same code is `ikhnos probe` in the single Ikhnos binary (cmd/ikhnos), which is what
// the container image and the Helm chart run. This binary keeps the old name working for development and tests.
package main

import (
	"os"

	"ikhnos/internal/cli"
	probecli "ikhnos/internal/cli/probe"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(probecli.Main(os.Args[1:]))
}
