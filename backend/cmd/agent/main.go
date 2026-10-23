// Command agent runs inside a cluster (or beside one, for development) and reports what it discovers.
//
// It is a thin wrapper: the same code is `ikhnos agent` in the single Ikhnos binary (cmd/ikhnos), which is what
// the container image and the Helm chart run. This binary keeps the old name working for development and tests.
package main

import (
	"os"

	"ikhnos/internal/cli"
	agentcli "ikhnos/internal/cli/agent"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(agentcli.Main(os.Args[1:]))
}
