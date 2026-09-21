// Command continuum is the one binary (and one container image) behind every part of Continuum that runs in a cluster.
// Its first argument picks the role:
//
//	continuum agent   the in-cluster agent (Deployment)
//	continuum probe   the optional node probe (DaemonSet)
//	continuum flow    the optional node flow collector (DaemonSet, needs root and BPF)
//	continuum version
//
// Each role takes exactly the flags and environment variables the separate binaries (cmd/agent, cmd/probe, cmd/flow, now
// thin wrappers over the same code) always took. The server is a different program with its own image (cmd/server).
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"continuum/internal/cli"
	agentcli "continuum/internal/cli/agent"
	flowcli "continuum/internal/cli/flow"
	probecli "continuum/internal/cli/probe"
)

var version = "0.1.0-dev"

func main() {
	cli.Version = version
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run dispatches on the role and returns the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "agent":
		return agentcli.Main(args[1:])
	case "probe":
		return probecli.Main(args[1:])
	case "flow":
		return flowcli.Main(args[1:])
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "continuum %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	case "help", "--help", "-h", "-help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "continuum: unknown role %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: continuum <role> [flags]   (continuum <role> --help lists a role's flags)")
	fmt.Fprintln(w)
	for _, r := range cli.Roles {
		fmt.Fprintf(w, "  %-8s %s\n", r.Name, r.Summary)
	}
	fmt.Fprintf(w, "  %-8s %s\n", "version", "print the version")
}
