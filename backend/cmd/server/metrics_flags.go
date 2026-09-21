package main

import (
	"errors"
	"flag"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"continuum/internal/server"
)

var (
	metricsListen = flag.String("metrics-listen", os.Getenv("CONTINUUM_METRICS_LISTEN"),
		"address to serve Prometheus metrics on (GET /metrics), for example 127.0.0.1:9090. Off by default. Counts only: agents by status, syncs applied and refused, failed sign-ins, rate-limited requests, recovered panics, store errors; no names or cluster data. It is separate from the admin listener so it can stay on a network only your monitoring reaches; give --metrics-token-file when that is not loopback; env CONTINUUM_METRICS_LISTEN")
	metricsTokenFile = flag.String("metrics-token-file", os.Getenv("CONTINUUM_METRICS_TOKEN_FILE"),
		"file holding a bearer token that scrapers must present to read /metrics; env CONTINUUM_METRICS_TOKEN_FILE")
)

// serveMetrics starts the metrics listener when one was asked for and returns what to shut down.
func serveMetrics(log *slog.Logger, src server.MetricsSource, version string) (*http.Server, error) {
	if *metricsListen == "" {
		return nil, nil
	}
	token := ""
	if *metricsTokenFile != "" {
		b, err := os.ReadFile(*metricsTokenFile)
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(string(b))
		if token == "" {
			return nil, errors.New("--metrics-token-file is empty")
		}
	}
	if token == "" && !isLoopback(*metricsListen) {
		return nil, errors.New("refusing to serve metrics on " + *metricsListen + " without a token: they count agents across every organisation. Give --metrics-token-file, or listen on a loopback address")
	}
	l, err := net.Listen("tcp", *metricsListen)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: server.MetricsHandler(src, version, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(l); !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics listener stopped", "err", err)
		}
	}()
	log.Info("metrics are served", "listen", *metricsListen, "token", token != "")
	return srv, nil
}
