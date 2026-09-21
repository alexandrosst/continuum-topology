package probe

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"continuum/internal/cli"
	"continuum/internal/probe"

	"google.golang.org/protobuf/encoding/protojson"
)

// Main is the optional node probe: a tiny read-only program that runs on every node (as a DaemonSet) and tells the
// Continuum agent what kind of machine it is. Run it with --print to see exactly what it reads; there is nothing else.
func Main(args []string) int {
	fs := cli.NewFlagSet("continuum probe", os.Stderr)
	agentURL := fs.String("agent", cli.Env("CONTINUUM_PROBE_URL", ""), "URL of the agent's probe receiver, e.g. http://continuum-agent-probe.continuum-system.svc:8081")
	secretFile := fs.String("secret-file", cli.Env("CONTINUUM_PROBE_SECRET_FILE", ""), "file holding the shared secret")
	node := fs.String("node", cli.Env("NODE_NAME", ""), "this node's Kubernetes name (from the downward API)")
	sys := fs.String("sys", cli.Env("CONTINUUM_PROBE_SYS", probe.DefaultPaths.Sys), "the host's sysfs")
	proc := fs.String("proc", cli.Env("CONTINUUM_PROBE_PROC", probe.DefaultPaths.Proc), "the host's procfs (only cpuinfo is read)")
	every := fs.Duration("interval", 3*time.Minute, "how often to report")
	print := fs.Bool("print", false, "print what would be reported and exit; sends nothing")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	paths := probe.Paths{Sys: *sys, Proc: *proc}

	if *print {
		b, _ := protojson.MarshalOptions{Multiline: true, EmitUnpopulated: true}.Marshal(probe.Read(paths))
		fmt.Println(string(b))
		return 0
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *agentURL == "" || *secretFile == "" || *node == "" {
		log.Error("--agent, --secret-file and --node are required")
		return 1
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil || len(strings.TrimSpace(string(secret))) < 16 {
		log.Error("the secret file is missing or too short", "err", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("continuum node probe starting", "version", cli.Version, "node", *node, "agent", *agentURL)
	probe.Run(ctx, paths, strings.TrimRight(*agentURL, "/"), []byte(strings.TrimSpace(string(secret))), *node, *every, func(msg string, kv ...any) { log.Warn(msg, kv...) })
	return 0
}
