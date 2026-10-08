// Package certrenew is the "continuum cert-renew" role: the sidecar of a regional operator that keeps the operator's own
// TLS Secrets (its receiver certificate, and the client certificate its exporter presents to another operator) from
// running out. The discovery agent does the same for the Secrets of its own cluster's collectors; both use
// internal/certrenew.
package certrenew

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"continuum/internal/certrenew"
	"continuum/internal/cli"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Main returns the process exit code.
func Main(args []string) int {
	fs := cli.NewFlagSet("continuum cert-renew", os.Stderr)
	server := fs.String("server", cli.Env("CONTINUUM_SERVER", ""), "the Ikhnos server's agent address, host:port")
	pin := fs.String("ca-pin", cli.Env("CONTINUUM_CA_PIN", ""), "pin of the server CA (sha256/... or the legacy hex fingerprint)")
	ns := fs.String("namespace", cli.Env("POD_NAMESPACE", ""), "the namespace of the Secrets (the pod's own)")
	secrets := fs.String("secrets", cli.Env("CONTINUUM_RENEW_SECRETS", ""), "Secrets to keep renewed, comma separated; each holds tls.crt, tls.key and ca.crt")
	every := fs.Duration("every", time.Hour, "how often the Secrets are looked at")
	renewBefore := fs.Duration("renew-before", 0, "renew a certificate with less than this left (default: the server's 20 days)")
	once := fs.Bool("once", false, "check every Secret one time and exit (non-zero if one could not be renewed)")
	kubeconfig := fs.String("kubeconfig", "", "development only: use this kubeconfig instead of the in-cluster service account")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *server == "" || *pin == "" {
		return cli.Fatal(log, fmt.Errorf("--server and --ca-pin are required"))
	}
	if _, _, err := net.SplitHostPort(*server); err != nil {
		return cli.Fatal(log, fmt.Errorf("--server must be host:port: %w", err))
	}
	if *ns == "" {
		return cli.Fatal(log, fmt.Errorf("--namespace is required (the chart sets POD_NAMESPACE)"))
	}
	targets := certrenew.ParseTargets(*ns, *secrets)
	if len(targets) == 0 {
		return cli.Fatal(log, fmt.Errorf("--secrets names no Secret"))
	}
	var rc *rest.Config
	var err error
	if *kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return cli.Fatal(log, err)
	}
	rc.UserAgent = "continuum-cert-renew/" + cli.Version
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return cli.Fatal(log, err)
	}
	cfg := certrenew.Config{Server: *server, CAPin: *pin, Client: client, Targets: targets, Log: log, Every: *every, RenewBefore: *renewBefore}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	names := make([]string, len(targets))
	for i, t := range targets {
		names[i] = t.String()
	}
	log.Info("continuum cert-renew starting", "version", cli.Version, "server", *server, "secrets", strings.Join(names, ","))
	if *once {
		if certrenew.Once(ctx, cfg) > 0 {
			return 1
		}
		return 0
	}
	certrenew.Run(ctx, cfg)
	return 0
}
