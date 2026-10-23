// Command ikhnos is the one binary (and one container image) behind every part of Ikhnos that runs in a cluster.
// Its first argument picks the role:
//
//	ikhnos agent   the in-cluster agent (Deployment)
//	ikhnos probe   the optional node probe (DaemonSet)
//	ikhnos flow    the optional node flow collector (DaemonSet, needs root and BPF)
//	ikhnos link-prober   the optional cluster-link prober (Deployment; OWAMP is in the link image)
//	ikhnos link-iperf-server   the iperf3 server of the throughput tests when Ikhnos issues the credentials (runs in the responder pod)
//	ikhnos cert-renew   the regional operator's sidecar that keeps its TLS Secrets from expiring
//	ikhnos purge-identity   deletes the agent's identity Secret (the agent chart's post-delete hook, when asked for)
//	ikhnos version
//
// Each role takes exactly the flags and environment variables the separate binaries (cmd/agent, cmd/probe, cmd/flow, now
// thin wrappers over the same code) always took. The server is a different program with its own image (cmd/server).
package main

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"ikhnos/internal/cli"
	agentcli "ikhnos/internal/cli/agent"
	renewcli "ikhnos/internal/cli/certrenew"
	flowcli "ikhnos/internal/cli/flow"
	linkiperfcli "ikhnos/internal/cli/linkiperf"
	linkprobercli "ikhnos/internal/cli/linkprober"
	probecli "ikhnos/internal/cli/probe"
	purgecli "ikhnos/internal/cli/purge"
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
	case "link-prober":
		return linkprobercli.Main(args[1:])
	case "link-iperf-server":
		return linkiperfcli.Main(args[1:])
	case "cert-renew":
		return renewcli.Main(args[1:])
	case "purge-identity":
		return purgecli.Main(args[1:])
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "ikhnos %s (%s/%s, %s)\n", version, runtime.GOOS, runtime.GOARCH, runtime.Version())
		return 0
	case "help", "--help", "-h", "-help":
		usage(stdout)
		return 0
	}
	fmt.Fprintf(stderr, "ikhnos: unknown role %q\n\n", args[0])
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: ikhnos <role> [flags]   (ikhnos <role> --help lists a role's flags)")
	fmt.Fprintln(w)
	for _, r := range cli.Roles {
		fmt.Fprintf(w, "  %-18s %s\n", r.Name, r.Summary)
	}
	fmt.Fprintf(w, "  %-18s %s\n", "version", "print the version")
}
