// Package linkprober is `continuum link-prober`: the active half of the cluster-link measurements. It asks the agent which
// clusters this one is paired with, measures the loss and delay of the path to each in both directions with OWAMP, and
// serves the result as Prometheus text for the cluster's collector. It runs in a pod of its own so that the agent image
// carries no measurement tools and a misbehaving probe cannot take the agent down.
package linkprober

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

	"continuum/internal/cli"
	"continuum/internal/link"
)

// Main runs the prober until it is signalled.
func Main(args []string) int {
	fs := cli.NewFlagSet("continuum link-prober", os.Stderr)
	agentURL := fs.String("agent", cli.Env("CONTINUUM_LINK_AGENT_URL", ""), "URL of the agent's link address, e.g. http://continuum-agent-link-metrics.continuum-system.svc:9104")
	secretFile := fs.String("secret-file", cli.Env("CONTINUUM_FLOW_SECRET_FILE", ""), "file holding the secret shared with the agent")
	listen := fs.String("listen", cli.Env("CONTINUUM_LINK_PROBE_LISTEN", ":9105"), "address to serve the probe metrics on")
	owping := fs.String("owping", cli.Env("CONTINUUM_LINK_OWPING", "/usr/local/bin/owping"), "the owping program")
	port := fs.Int("responder-port", cli.Atoi(cli.Env("CONTINUUM_LINK_RESPONDER_PORT", "8861")), "the control port every cluster's link responder listens on")
	packets := fs.Int("packets", cli.Atoi(cli.Env("CONTINUUM_LINK_PACKETS", "100")), "test packets per run and direction")
	spacing := fs.Duration("spacing", durEnv("CONTINUUM_LINK_SPACING", 100*time.Millisecond), "average time between test packets")
	every := fs.Duration("interval", durEnv("CONTINUUM_LINK_INTERVAL", time.Minute), "how often each peer is measured")
	user := fs.String("auth-user", cli.Env("CONTINUUM_LINK_AUTH_USER", ""), "OWAMP user for authenticated mode (needs --auth-key-file)")
	keyFile := fs.String("auth-key-file", cli.Env("CONTINUUM_LINK_AUTH_KEY_FILE", ""), "file holding the OWAMP pass-phrase file for authenticated mode")
	dscp := fs.Int("dscp", cli.Atoi(cli.Env("CONTINUUM_LINK_DSCP", "0")), "DSCP value to mark test packets with (0: unmarked)")
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
	peers := link.NewPeers()
	meter := link.NewMeter(peers)
	meter.OnlyProbes()
	r := &link.Runner{Meter: meter, Log: log, Bin: *owping, Port: *port, Packets: *packets, Spacing: *spacing, Every: *every, User: *user, KeyFile: *keyFile, DSCP: *dscp}
	r.Defaults()
	if err := r.Validate(); err != nil {
		return cli.Fatal(log, err)
	}
	if _, err := os.Stat(*owping); err != nil {
		return cli.Fatal(log, fmt.Errorf("--owping: %w", err))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	stopServe, err := link.Serve(*listen, meter, log)
	if err != nil {
		return cli.Fatal(log, fmt.Errorf("--listen: %w", err))
	}
	defer stopServe()

	log.Info("link prober starting", "version", cli.Version, "agent", *agentURL, "packets", *packets, "interval", *every)
	go follow(ctx, log, &http.Client{Timeout: 10 * time.Second}, *agentURL, secret, peers, meter)
	r.Run(ctx)
	return 0
}

// follow keeps the peer list current: the agent hears about changes from the server at least once a minute, so asking
// once a minute is as fresh as the answer can be.
func follow(ctx context.Context, log *slog.Logger, hc *http.Client, url string, secret []byte, peers *link.Peers, meter *link.Meter) {
	asked := false
	for {
		d, err := link.FetchPeers(ctx, hc, url, secret, time.Now())
		switch {
		case err != nil:
			log.Warn("could not fetch the link peers from the agent", "err", err)
		default:
			for _, why := range peers.SetFromDoc(d) {
				log.Warn("link peer list", "note", why)
			}
			meter.Forget()
			if !asked {
				log.Info("link peers", "cluster", d.ClusterID, "peers", len(d.Peers))
				asked = true
			}
		}
		t := time.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func durEnv(k string, d time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if p, err := time.ParseDuration(v); err == nil {
			return p
		}
	}
	return d
}
