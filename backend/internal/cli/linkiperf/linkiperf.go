// Package linkiperf is `ikhnos link-iperf-server`: the iperf3 server of the throughput tests, run in the responder pod when the
// Ikhnos server issues the credentials. It fetches this cluster's server key and users from the agent (a signed request with a
// secret of its own, answered sealed), writes them to a private in-memory directory, and runs iperf3 -s in authenticated mode
// with them. With no credentials it runs nothing. See link.IperfServerRunner.
package linkiperf

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ikhnos/internal/cli"
	"ikhnos/internal/link"
)

// Main runs the supervisor until it is signalled.
func Main(args []string) int {
	fs := cli.NewFlagSet("ikhnos link-iperf-server", os.Stderr)
	agentURL := fs.String("agent", cli.Env("IKHNOS_LINK_AGENT_URL", ""), "URL of the agent's link address, e.g. http://ikhnos-agent-link-metrics.ikhnos-system.svc:9104")
	secretFile := fs.String("secret-file", cli.Env("IKHNOS_LINK_IPERF_SECRET_FILE", ""), "file holding the secret shared with the agent for the iperf3-server credentials request (a secret of its own: not the prober's, not the flow secret)")
	dir := fs.String("dir", cli.Env("IKHNOS_LINK_IPERF_DIR", "/iperf-auth"), "private in-memory directory for the key and the users file")
	bin := fs.String("iperf3", cli.Env("IKHNOS_LINK_IPERF3", "/usr/local/bin/iperf3"), "the iperf3 program")
	port := fs.Int("port", cli.Atoi(cli.Env("IKHNOS_LINK_CAPACITY_PORT", "8862")), "the port the iperf3 server listens on")
	idle := fs.Int("idle-timeout", cli.Atoi(cli.Env("IKHNOS_LINK_CAPACITY_IDLE_TIMEOUT", "60")), "seconds after which a stuck test is dropped")
	limit := fs.String("bitrate-limit", cli.Env("IKHNOS_LINK_CAPACITY_BITRATE_LIMIT", ""), "iperf3 --server-bitrate-limit (empty: none)")
	every := fs.Duration("interval", time.Minute, "how often the agent is asked for the credentials")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *agentURL == "" || *secretFile == "" {
		return cli.Fatal(log, fmt.Errorf("--agent and --secret-file are required"))
	}
	if !strings.HasPrefix(*agentURL, "http://") && !strings.HasPrefix(*agentURL, "https://") {
		return cli.Fatal(log, fmt.Errorf("--agent must be an http(s) URL, got %q", *agentURL))
	}
	secret, err := os.ReadFile(*secretFile)
	secret = []byte(strings.TrimSpace(string(secret)))
	if err != nil || len(secret) < 16 {
		return cli.Fatal(log, fmt.Errorf("--secret-file must hold a secret of at least 16 characters: %v", err))
	}
	if _, err := os.Stat(*bin); err != nil {
		return cli.Fatal(log, fmt.Errorf("--iperf3: %w", err))
	}
	if *port < 1024 || *port > 65535 {
		return cli.Fatal(log, fmt.Errorf("--port must be between 1024 and 65535"))
	}
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		return cli.Fatal(log, fmt.Errorf("--dir: %w", err))
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	r := link.NewIperfServerRunner(*agentURL, secret, &http.Client{Timeout: 10 * time.Second})
	r.Dir, r.Bin, r.Port, r.IdleTimeout, r.BitrateLimit, r.Every, r.Log = *dir, *bin, *port, *idle, *limit, *every, log
	log.Info("link iperf3 server supervisor starting", "version", cli.Version, "agent", *agentURL, "port", *port)
	r.Run(ctx)
	return 0
}
