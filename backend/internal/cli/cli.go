// Package cli is what the three Continuum roles share: the in-cluster agent (cli/agent), the node probe (cli/probe) and the
// node flow collector (cli/flow). They ship as ONE binary, `continuum`, that picks its role from its first argument
// (`continuum agent ...`, `continuum probe ...`, `continuum flow ...`), so a cluster pulls a single image. cmd/agent,
// cmd/probe and cmd/flow are thin wrappers over the same Main functions, for development and for anything that still runs
// the old binary names. Each role lives in its own package so a wrapper links only what its role needs (the probe does not
// carry the Kubernetes client, the agent does not carry the eBPF loader).
//
// Each role takes the same flags and environment variables it always had. Main returns the process exit code instead of
// calling os.Exit, so it can be tested and deferred cleanups run.
package cli

import (
	"flag"
	"io"
	"log/slog"
	"os"
	"strconv"
)

// Version is what the roles report about themselves. The binary's main sets it (from a linker flag) before calling a role.
var Version = "0.1.0-dev"

// Roles names the subcommands `continuum` accepts, in the order help lists them.
var Roles = []struct{ Name, Summary string }{
	{"agent", "the in-cluster agent: discovers the cluster and reports to the Continuum server (read-only)"},
	{"probe", "the optional node probe: tells the agent what kind of machine a node is (read-only)"},
	{"flow", "the optional node flow collector: counts connections between workloads (needs root and BPF)"},
}

func Env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func Atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func Fatal(log *slog.Logger, err error) int {
	log.Error("fatal", "err", err)
	return 1
}

// NewFlagSet gives each role its own flag set, so the roles can be run one after another in tests and nothing leaks into
// the process-wide flag.CommandLine. `-h` and unknown flags exit with code 2, as the flag package always has.
func NewFlagSet(name string, out io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(out)
	return fs
}

// Parse returns (exitCode, done): done means the caller must return exitCode now (help was shown, or the flags were bad).
func Parse(fs *flag.FlagSet, args []string) (int, bool) {
	switch err := fs.Parse(args); err {
	case nil:
		return 0, false
	case flag.ErrHelp:
		return 0, true
	default:
		return 2, true
	}
}
