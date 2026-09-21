// Command probe is the optional node probe: a tiny read-only program that runs on every node
// (as a DaemonSet) and tells the Continuum agent what kind of machine it is. Run it with --print
// to see exactly what it reads; there is nothing else.
//
// It is a thin wrapper: the same code is `continuum probe` in the single Continuum binary (cmd/continuum), which is what
// the container image and the Helm chart run. This binary keeps the old name working for development and tests.
package main

import (
	"os"

	"continuum/internal/cli"
	probecli "continuum/internal/cli/probe"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(probecli.Main(os.Args[1:]))
}
