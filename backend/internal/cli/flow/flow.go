package flow

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"continuum/internal/cli"
	"continuum/internal/flow/collector"
	"continuum/internal/flow/conntrack"
	"continuum/internal/flow/ebpf"

	"google.golang.org/protobuf/encoding/protojson"
)

// Main is the optional node flow collector. It counts TCP connections and, where the kernel allows, the bytes they
// carried, and reports the counts to the Continuum agent, which turns addresses into workloads. It never reads packets or
// payloads and keeps nothing but counters.
//
// Method: eBPF where the kernel supports it, the kernel's connection-tracking table otherwise. Run it with --print 30s to
// see exactly what a window contains; that sends nothing. Loading the eBPF program needs root with CAP_BPF and CAP_PERFMON;
// nothing at startup of the other roles touches the kernel, so importing this here costs `agent` and `probe` no privileges.
func Main(args []string) int {
	fs := cli.NewFlagSet("continuum flow", os.Stderr)
	agentURL := fs.String("agent", cli.Env("CONTINUUM_FLOW_URL", ""), "URL of the agent's flow receiver, e.g. http://continuum-agent-probe.continuum-system.svc:8081")
	secretFile := fs.String("secret-file", cli.Env("CONTINUUM_FLOW_SECRET_FILE", ""), "file holding the flow secret")
	node := fs.String("node", cli.Env("NODE_NAME", ""), "this node's Kubernetes name (from the downward API)")
	method := fs.String("method", cli.Env("CONTINUUM_FLOW_METHOD", "auto"), "auto | ebpf | conntrack")
	udp := fs.String("udp", cli.Env("CONTINUUM_FLOW_UDP", "auto"), "also observe UDP through the connection-tracking table: auto (when the eBPF method is in use and the table is readable) | on | off")
	table := fs.String("conntrack-table", cli.Env("CONTINUUM_FLOW_CONNTRACK", conntrack.DefaultTable), "the connection-tracking table")
	live := fs.Bool("live", cli.Env("CONTINUUM_FLOW_LIVE", "") == "true", "count the bytes of connections that are still open (eBPF only; needs the host's process namespace, or it sees only its own)")
	every := fs.Duration("interval", 30*time.Second, "how often to report")
	print := fs.Duration("print", 0, "observe for this long, print the report and exit; sends nothing")
	if code, done := cli.Parse(fs, args); done {
		return code
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	src, why, err := collector.Choose(*method,
		func() (collector.Source, error) {
			o, err := ebpf.Open(ebpf.Options{Live: *live})
			if err != nil {
				return nil, err
			}
			if o.LiveErr != nil {
				log.Warn("open connections will be counted when they close, not while open", "reason", o.LiveErr)
			}
			return o, nil
		},
		func() (collector.Source, error) { return conntrack.Open(*table) })
	if err != nil {
		log.Error("cannot observe traffic on this node", "err", err)
		return 1
	}
	sources := []collector.Source{src}
	for _, w := range why {
		log.Info("a better method is not available here", "reason", w)
	}
	// eBPF counts TCP connections. UDP has none, so its flows come from the connection-tracking table
	// when the kernel keeps one; the conntrack method already reports both.
	if src.Method() == "ebpf" && *udp != "off" {
		if u, err := conntrack.Open(*table, "udp"); err == nil {
			sources = append(sources, u)
		} else if *udp == "on" {
			log.Error("--udp=on but the connection-tracking table cannot be read", "err", err)
			closeAll(sources)
			return 1
		} else {
			log.Info("UDP is not observed on this node", "reason", err)
		}
	}
	defer closeAll(sources)

	if *print > 0 {
		time.Sleep(*print)
		for _, s := range sources {
			flows, lost, err := s.Collect()
			if err != nil {
				log.Error("could not read", "err", err)
				return 1
			}
			b, _ := protojson.MarshalOptions{Multiline: true}.Marshal(collector.Report(s, *node, *print, flows, lost))
			fmt.Println(string(b))
		}
		return 0
	}

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
	methods := make([]string, 0, len(sources))
	for _, s := range sources {
		methods = append(methods, s.Method())
	}
	log.Info("continuum flow collector starting", "version", cli.Version, "node", *node, "methods", methods, "live", *live, "agent", *agentURL)
	var wg sync.WaitGroup
	for _, s := range sources {
		wg.Add(1)
		go func(s collector.Source) {
			defer wg.Done()
			collector.Run(ctx, s, strings.TrimRight(*agentURL, "/"), []byte(strings.TrimSpace(string(secret))), *node, *every, func(msg string, kv ...any) { log.Warn(msg, kv...) })
		}(s)
	}
	wg.Wait()
	return 0
}

func closeAll(sources []collector.Source) {
	for _, s := range sources {
		s.Close()
	}
}
