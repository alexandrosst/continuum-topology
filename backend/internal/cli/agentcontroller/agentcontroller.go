// Package agentcontroller is the Ikhnos controller's role: it runs internal/controller in a cluster, with the Helm library as its
// installer. It is not one of the roles of the `ikhnos` binary: it has an image of its own (cmd/controller), because it carries the
// Helm library and the other roles, which run on every node, must not.
package agentcontroller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"ikhnos/internal/chart"
	"ikhnos/internal/cli"
	"ikhnos/internal/controller"
	"ikhnos/internal/controller/helminstall"
)

// ChartVersion is the version the published agent chart was packaged with (a release build sets it with the linker); the version the
// controller installs the agent release at. Empty: what the embedded chart says.
var ChartVersion string

// Defaults are the Helm values every agent release gets under what an IkhnosAgent says: where this build's images are. The agent
// chart in the source tree names placeholder images; a release build's published chart has the registry it published to baked in, and
// this binary, which carries the chart's files and not that edit, is told the same things instead.
func Defaults(imageRepository, imageTag, linkRepository string) map[string]any {
	d := map[string]any{}
	set := func(path []string, v string) {
		if v == "" {
			return
		}
		m := d
		for _, p := range path[:len(path)-1] {
			next, ok := m[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				m[p] = next
			}
			m = next
		}
		m[path[len(path)-1]] = v
	}
	set([]string{"image", "repository"}, imageRepository)
	set([]string{"image", "tag"}, imageTag)
	set([]string{"telemetry", "clusterLinks", "probe", "image", "repository"}, linkRepository)
	return d
}

// Main returns the process exit code.
func Main(args []string) int {
	fs := cli.NewFlagSet("controller", os.Stderr)
	ns := fs.String("namespace", cli.Env("POD_NAMESPACE", ""), "the namespace to watch for IkhnosAgents and to install the agent in (the pod's own)")
	imageRepo := fs.String("agent-image-repository", cli.Env("IKHNOS_AGENT_IMAGE_REPOSITORY", ""), "the agent image's repository, when not the chart's own")
	imageTag := fs.String("agent-image-tag", cli.Env("IKHNOS_AGENT_IMAGE_TAG", ""), "the agent image's tag; empty: the chart's appVersion")
	linkRepo := fs.String("link-image-repository", cli.Env("IKHNOS_LINK_IMAGE_REPOSITORY", ""), "the cluster-link image's repository, when not the chart's own")
	maxTier := fs.Int("max-access-tier", cli.Atoi(cli.Env("IKHNOS_MAX_ACCESS_TIER", "2")), "the highest access tier (0, 1 or 2) an IkhnosAgent may ask for: what the controller was given the permissions for")
	allowTelemetry := fs.Bool("allow-telemetry", cli.Env("IKHNOS_ALLOW_TELEMETRY", "false") == "true", "whether an IkhnosAgent may turn telemetry on: what the controller was given the permissions for")
	timeout := fs.Duration("timeout", helminstall.DefaultTimeout, "how long one install, upgrade or uninstall may take")
	health := fs.String("health-addr", cli.Env("IKHNOS_HEALTH_ADDR", ":8081"), "where /healthz answers; empty: nowhere")
	kubeconfig := fs.String("kubeconfig", "", "development only: use this kubeconfig instead of the in-cluster service account")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *ns == "" {
		return cli.Fatal(log, errors.New("--namespace is required (the chart sets POD_NAMESPACE)"))
	}
	if *maxTier < 0 || *maxTier > 2 {
		return cli.Fatal(log, errors.New("--max-access-tier must be 0, 1 or 2"))
	}
	var rc *rest.Config
	var err error
	if *kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return cli.Fatal(log, fmt.Errorf("no cluster connection: %w", err))
	}
	dyn, err := dynamic.NewForConfig(rc)
	if err != nil {
		return cli.Fatal(log, err)
	}
	kube, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return cli.Fatal(log, err)
	}
	inst := &helminstall.Installer{Chart: chart.Agent, Configure: helminstall.Config(rc, log), Timeout: *timeout, Log: log}
	if ChartVersion != "" {
		inst.Version, inst.AppVersion = ChartVersion, cli.Version
	}
	r := &controller.Reconciler{Dyn: dyn, Kube: kube, Installer: inst, Namespace: *ns, Log: log, Defaults: Defaults(*imageRepo, *imageTag, *linkRepo), MaxTier: maxTier, AllowTelemetry: *allowTelemetry}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *health != "" {
		srv := serveHealth(*health, log)
		defer srv.Close()
	}
	if err := r.Run(ctx); err != nil {
		return cli.Fatal(log, err)
	}
	return 0
}

// serveHealth answers /healthz for the kubelet's probe: the process is up and its loop has not returned.
func serveHealth(addr string, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Error("the health port is not available", "addr", addr, "err", err)
		return srv
	}
	go func() { _ = srv.Serve(ln) }()
	return srv
}
