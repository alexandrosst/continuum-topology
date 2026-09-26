package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"continuum/internal/agent"
	"continuum/internal/agent/collect"
	"continuum/internal/cli"
	"continuum/internal/flow"
	"continuum/internal/measure"
	"continuum/internal/probe"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Main runs inside a cluster (or beside one, for development) and reports what it discovers. It returns the exit code.
func Main(args []string) int {
	fs := cli.NewFlagSet("continuum agent", os.Stderr)
	server := fs.String("server", cli.Env("CONTINUUM_SERVER", ""), "server host:port")
	pin := fs.String("ca-pin", cli.Env("CONTINUUM_CA_PIN", ""), "SHA-256 pin of the server CA (from the install command)")
	tier := fs.Int("tier", cli.Atoi(cli.Env("CONTINUUM_TIER", "2")), "highest access tier the installed RBAC allows (0-2)")
	kubeconfig := fs.String("kubeconfig", "", "development only: use this kubeconfig instead of the in-cluster service account")
	stateDir := fs.String("state-dir", "", "development only: keep the identity in this directory instead of a Kubernetes Secret")
	secret := fs.String("identity-secret", cli.Env("CONTINUUM_IDENTITY_SECRET", "continuum-agent-identity"), "Secret (in the agent's namespace) that holds the identity")
	ns := fs.String("namespace", cli.Env("POD_NAMESPACE", "continuum-system"), "the agent's namespace")
	releaseName := fs.String("release-name", cli.Env("CONTINUUM_RELEASE_NAME", ""), "the Helm release this agent was installed as (the chart sets this; empty outside it)")
	rbacSelfCheck := fs.Bool("rbac-self-check", cli.Env("CONTINUUM_RBAC_SELF_CHECK", "true") == "true", "periodically ask the cluster (SelfSubjectAccessReview) whether it still grants more than --tier declares, and report it as a problem if so; catches a helm upgrade that narrowed access.tier locally but was never run against the cluster")
	probeListen := fs.String("probe-listen", cli.Env("CONTINUUM_PROBE_LISTEN", ""), "address to listen on for node probe reports, e.g. :8081 (empty: no node probes)")
	probeSecretFile := fs.String("probe-secret-file", cli.Env("CONTINUUM_PROBE_SECRET_FILE", ""), "file holding the secret shared with the node probes")
	healthListen := fs.String("health-listen", cli.Env("CONTINUUM_HEALTH_LISTEN", ""), "address to serve /healthz (liveness) and /readyz (readiness) on, e.g. :8082 (empty: off)")
	measureOn := fs.Bool("measure", cli.Env("CONTINUUM_MEASURE", "") == "true", "allow timing TCP connections to the addresses the server names (to learn how far away other places are); off by default")
	flowWindow := fs.Duration("flow-window", 0, "development only: how often observed traffic is sent to the server (default 60s)")
	revokedHold := fs.Duration("revoked-hold", 0, "development only: how long a restarted agent whose identity was revoked waits before it exits with code 3 (default: a random 5 to 10 minutes; negative: do not wait)")
	flowSecretFile := fs.String("flow-secret-file", cli.Env("CONTINUUM_FLOW_SECRET_FILE", ""), "file holding the secret shared with the node flow collectors (needs --probe-listen)")
	rbacMode := fs.String("rbac-mode", cli.Env("CONTINUUM_RBAC_MODE", "cluster"), "how tier 2's installed RBAC was granted: cluster (one ClusterRole, default) or namespaced (a Role per namespace in --scope-namespaces instead). Must match the chart's rbac.mode; the chart sets this automatically")
	scopeNS := fs.String("scope-namespaces", cli.Env("CONTINUUM_SCOPE_NAMESPACES", ""), "report only these namespaces, comma separated (empty: all). Combined with --scope-label as a union. Required, and the only way to select namespaces, when --rbac-mode=namespaced")
	scopeExclude := fs.String("scope-exclude", cli.Env("CONTINUUM_SCOPE_EXCLUDE", ""), "never report these namespaces, comma separated")
	scopeLabel := fs.String("scope-label", cli.Env("CONTINUUM_SCOPE_LABEL", ""), "report only namespaces matching this label selector, e.g. continuum.io/observe=true (only labels the agent keeps, such as continuum.io/*)")
	probeEvery := fs.Duration("probe-interval", envDuration("CONTINUUM_PROBE_INTERVAL"), "how often the node probes were told to report (the chart's nodeProbe.interval); the agent calls them silent after three of these without a word (default 3m)")
	flowEvery := fs.Duration("flow-interval", envDuration("CONTINUUM_FLOW_INTERVAL"), "how often the flow collectors were told to report (the chart's flowObserver.interval; default 30s)")
	measurePorts := fs.String("measure-allow-ports", cli.Env("CONTINUUM_MEASURE_ALLOW_PORTS", ""), "comma separated control-plane ports (etcd, kubelet, ...) that connection timing may use anyway; refused by default")
	measureCIDRs := fs.String("measure-allow-cidrs", cli.Env("CONTINUUM_MEASURE_ALLOW_CIDRS", ""), "comma separated ranges in which connection timing may reach the API server's address anyway; refused by default")
	if code, done := cli.Parse(fs, args); done {
		return code
	}
	var pol measure.Policy
	for _, f := range strings.Split(*measurePorts, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		n, perr := strconv.Atoi(f)
		if perr != nil || n < 1 || n > 65535 {
			fmt.Fprintf(os.Stderr, "--measure-allow-ports: %q is not a port\n", f)
			return 2
		}
		pol.AllowPorts = append(pol.AllowPorts, n)
	}
	for _, f := range strings.Split(*measureCIDRs, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		pf, perr := netip.ParsePrefix(f)
		if perr != nil {
			fmt.Fprintf(os.Stderr, "--measure-allow-cidrs: %q is not a CIDR range\n", f)
			return 2
		}
		pol.AllowCIDRs = append(pol.AllowCIDRs, pf)
	}
	measure.SetPolicy(pol)
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	// The token is read from the environment only, so it does not appear in the process list.
	token := os.Getenv("CONTINUUM_TOKEN")
	if *server == "" || *pin == "" {
		return cli.Fatal(log, fmt.Errorf("--server and --ca-pin are required"))
	}
	scope, err := collect.ParseScope(*scopeNS, *scopeExclude, *scopeLabel)
	if err != nil {
		return cli.Fatal(log, err)
	}
	var rbacNamespaced bool
	switch *rbacMode {
	case "cluster":
	case "namespaced":
		rbacNamespaced = true
		if len(scope.Include) == 0 {
			return cli.Fatal(log, fmt.Errorf("--rbac-mode=namespaced needs --scope-namespaces: it cannot discover namespaces on its own (that needs the cluster-wide list permission this mode avoids), so there is nothing to watch without one"))
		}
		if *scopeLabel != "" {
			return cli.Fatal(log, fmt.Errorf("--rbac-mode=namespaced cannot be combined with --scope-label: evaluating a label selector needs to list namespaces cluster-wide, which is exactly the permission this mode avoids"))
		}
	default:
		return cli.Fatal(log, fmt.Errorf("--rbac-mode must be \"cluster\" or \"namespaced\", got %q", *rbacMode))
	}
	if *tier < 0 || *tier > 2 {
		return cli.Fatal(log, fmt.Errorf("--tier must be 0, 1 or 2 in this release"))
	}
	if _, _, err := net.SplitHostPort(*server); err != nil {
		return cli.Fatal(log, fmt.Errorf("--server must be host:port: %w", err))
	}

	var rc *rest.Config
	if *kubeconfig != "" {
		rc, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		rc, err = rest.InClusterConfig()
	}
	if err != nil {
		return cli.Fatal(log, err)
	}
	rc.UserAgent = "continuum-agent/" + cli.Version
	client, err := kubernetes.NewForConfig(rc)
	if err != nil {
		return cli.Fatal(log, err)
	}
	apiHost := rc.Host
	if u, err := url.Parse(rc.Host); err == nil && u.Host != "" {
		apiHost = u.Host
	}
	if real := resolveApiEndpoint(client); real != "" {
		apiHost = real
	}

	var ids agent.IdentityStore
	if *stateDir != "" {
		ids = agent.FileStore{Dir: *stateDir}
	} else {
		ids = agent.SecretStore{Client: client, Namespace: *ns, Name: *secret}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("continuum agent starting", "version", cli.Version, "server", *server, "tier", *tier)
	var probes *probe.Receiver
	if *probeListen != "" && *probeSecretFile != "" {
		sec, rerr := os.ReadFile(*probeSecretFile)
		sec = []byte(strings.TrimSpace(string(sec)))
		if rerr != nil || len(sec) < 16 {
			return cli.Fatal(log, fmt.Errorf("--probe-listen needs --probe-secret-file with a secret of at least 16 characters: %v", rerr))
		}
		probes = probe.NewReceiver(sec, log)
	}
	var flows *flow.Pipeline
	if *flowSecretFile != "" {
		if *probeListen == "" {
			return cli.Fatal(log, fmt.Errorf("--flow-secret-file needs --probe-listen: reports arrive on that address"))
		}
		sec, rerr := os.ReadFile(*flowSecretFile)
		sec = []byte(strings.TrimSpace(string(sec)))
		if rerr != nil || len(sec) < 16 {
			return cli.Fatal(log, fmt.Errorf("--flow-secret-file must hold a secret of at least 16 characters: %v", rerr))
		}
		flows = flow.NewPipeline(sec, func() *collect.Index { return nil }, log)
	}
	if *probeListen != "" && probes == nil && flows == nil {
		return cli.Fatal(log, fmt.Errorf("--probe-listen needs --probe-secret-file and/or --flow-secret-file"))
	}
	var health *agent.Health
	if *healthListen != "" {
		health = agent.NewHealth()
		stopHealth, herr := agent.ServeHealth(*healthListen, health, log)
		if herr != nil {
			return cli.Fatal(log, fmt.Errorf("--health-listen: %w", herr))
		}
		defer stopHealth()
	}
	err = agent.Run(ctx, agent.Config{Server: *server, CAPin: *pin, Token: token, Tier: *tier, Kube: client, APIHost: apiHost, Identity: ids, Version: cli.Version, Log: log, Probes: probes, Flows: flows, FlowWindow: *flowWindow, Measure: *measureOn, ProbeListen: *probeListen, Scope: scope, Health: health, RevokedHold: *revokedHold, ProbeInterval: *probeEvery, FlowInterval: *flowEvery, Namespace: *ns, ReleaseName: *releaseName, RBACSelfCheck: *rbacSelfCheck, RBACNamespaced: rbacNamespaced})
	if errors.Is(err, agent.ErrRevoked) {
		// Exit with a code of its own (agent.ExitRevoked) so `kubectl get pod` and the restart count say what
		// happened. Run has already said why, in plain words, and has waited a random 5-10 minutes if this was a
		// restart, so the pod's restarts are spaced out and the server is not contacted at all.
		return agent.ExitRevoked
	}
	if err != nil && ctx.Err() == nil {
		return cli.Fatal(log, err)
	}
	return 0
}

// envDuration reads a Go duration ("3m", "30s") from the environment; anything else means "not given" (0).
func envDuration(k string) time.Duration {
	d, _ := time.ParseDuration(cli.Env(k, ""))
	return d
}

// resolveApiEndpoint tries to name the API server's real address instead of the virtual ClusterIP every
// in-cluster client (this agent included) is handed by default via KUBERNETES_SERVICE_HOST/PORT: it reads
// the Endpoints object for "kubernetes" in "default", which every Kubernetes cluster carries under that
// exact, reserved name - not a guess, a fixed part of the API - and which lists the control plane's actual
// backend address(es). Read-only, one named resource, in a fixed namespace unrelated to rbac.mode's
// namespace scoping; reveals nothing an operator could not already see with `kubectl get endpoints
// kubernetes`. Returns "" on any failure (RBAC not granted - access.resolveApiEndpoint=false in the chart,
// or an older install that predates this - a non-standard cluster with no such Service, or simply no
// subset ready yet): the caller keeps whatever address it already had, exactly as before this existed.
func resolveApiEndpoint(client kubernetes.Interface) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ep, err := client.CoreV1().Endpoints("default").Get(ctx, "kubernetes", metav1.GetOptions{})
	if err != nil {
		return ""
	}
	for _, s := range ep.Subsets {
		if len(s.Addresses) == 0 || len(s.Ports) == 0 {
			continue
		}
		port := s.Ports[0].Port
		for _, p := range s.Ports {
			if p.Name == "https" || p.Port == 443 { // the API server's port, if named or guessable; else the first one listed
				port = p.Port
				break
			}
		}
		return net.JoinHostPort(s.Addresses[0].IP, strconv.Itoa(int(port)))
	}
	return ""
}
