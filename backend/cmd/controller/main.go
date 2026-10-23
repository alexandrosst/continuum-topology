// Command controller keeps an Ikhnos agent installed in the cluster it runs in, from an IkhnosAgent resource. See internal/controller.
// It is its own binary and image: it carries the Helm library, which the roles of the `ikhnos` binary (that run on every node) do not.
package main

import (
	"os"

	"ikhnos/internal/cli"
	"ikhnos/internal/cli/agentcontroller"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(agentcontroller.Main(os.Args[1:]))
}
