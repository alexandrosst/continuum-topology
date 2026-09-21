// Command agent runs inside a cluster (or beside one, for development) and reports what it discovers.
//
// It is a thin wrapper: the same code is `continuum agent` in the single Continuum binary (cmd/continuum), which is what
// the container image and the Helm chart run. This binary keeps the old name working for development and tests.
package main

import (
	"os"

	"continuum/internal/cli"
	agentcli "continuum/internal/cli/agent"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(agentcli.Main(os.Args[1:]))
}
