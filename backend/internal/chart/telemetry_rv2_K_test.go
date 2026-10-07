package chart

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// Kepler review (agent K): the two upstream lines (release-0.7 "ebpf", v0.10+ "powercap"), what each needs on a node, and what
// the chart does on a node that cannot run it. The facts these tests rest on were read from the upstream source at the tags
// the chart pins (v0.7.12, v0.12.0) and from quay.io's registry API; the shell scripts are run for real below.

func keplerDS(t *testing.T, extra ...string) (rendered, corev1.PodSpec) {
	t.Helper()
	r := render(t, append(append([]string{}, energyOn...), extra...)...)
	ds, ok := r.daemonsets["continuum-telemetry-kepler"]
	if !ok {
		t.Fatalf("no Kepler DaemonSet; have %v", keys(r.daemonsets))
	}
	return r, ds.Spec.Template.Spec
}

func powercapOn() []string {
	return []string{"--set", "telemetry.energy.metrics.engine=powercap"}
}

func container(t *testing.T, cs []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %q in %v", name, cs)
	return corev1.Container{}
}

// The default stays the legacy eBPF Kepler (its metric names are what a deployed backend already knows), but is no longer
// started blind: an init container asks the node first and the metrics endpoint is probed for what is served.
func TestKeplerEbpfEngineIsTheDefaultAndIsChecked(t *testing.T) {
	r, pod := keplerDS(t)
	main := container(t, pod.Containers, "kepler")
	if main.Image != "quay.io/sustainable_computing_io/kepler:release-0.7.12" {
		t.Errorf("image = %q", main.Image)
	}
	if len(main.Args) != 0 {
		t.Errorf("the ebpf engine is configured by environment, args = %v", main.Args)
	}
	if _, ok := r.configmaps["continuum-telemetry-kepler-config"]; ok {
		t.Error("a Kepler config file was rendered for the ebpf engine, which does not read one")
	}
	if pod.NodeSelector["kubernetes.io/arch"] != "" {
		t.Errorf("the ebpf image is multi-arch, nodeSelector = %v", pod.NodeSelector)
	}
	pre := container(t, pod.InitContainers, "preflight")
	if pre.Image != main.Image || pre.SecurityContext == nil || pre.SecurityContext.Privileged == nil || !*pre.SecurityContext.Privileged {
		t.Errorf("the preflight must be the Kepler image, privileged (bpf() needs it): %+v", pre)
	}
	script := strings.Join(pre.Args, "\n")
	for _, want := range []string{"/usr/bin/bpftool feature probe kernel", "eBPF program_type tracing is NOT available", "NOT SUPPORTED ON THIS NODE", "Init:0/1"} {
		if !strings.Contains(script, want) {
			t.Errorf("preflight script lacks %q", want)
		}
	}
	for _, e := range main.Env {
		if e.Name == "BIND_ADDRESS" && e.Value != ":9103" {
			t.Errorf("BIND_ADDRESS = %q, want :9103 (all address families)", e.Value)
		}
	}
	if main.StartupProbe == nil || main.StartupProbe.Exec == nil {
		t.Fatal("no startup probe")
	}
	if main.LivenessProbe == nil || main.LivenessProbe.HTTPGet == nil || main.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("liveness = %+v, want release-0.7's /healthz", main.LivenessProbe)
	}
	// Nothing a node already offered is mounted differently: still /proc and /sys, read-only.
	if len(pod.Volumes) != 2 {
		t.Errorf("volumes = %v, want only the /proc and /sys hostPaths", pod.Volumes)
	}
	if !pod.HostPID {
		t.Error("Kepler needs hostPID")
	}
	// Pods held on unsupported nodes are never available; a 10% budget would be used up by them and stop the rollout.
	ru := r.daemonsets["continuum-telemetry-kepler"].Spec.UpdateStrategy.RollingUpdate
	if ru == nil || ru.MaxUnavailable == nil || ru.MaxUnavailable.String() != "100%" {
		t.Errorf("update strategy = %+v, want maxUnavailable 100%% (held pods count against it)", r.daemonsets["continuum-telemetry-kepler"].Spec.UpdateStrategy)
	}
}

// engine=powercap runs v0.12.0 from a config file: the API-server pod informer (the kubelet one needs nodes/proxy get, which
// Kubernetes documents as not read-only), one listen port for every address family, no fake meter, no process level.
func TestKeplerPowercapEngineConfig(t *testing.T) {
	r, pod := keplerDS(t, powercapOn()...)
	main := container(t, pod.Containers, "kepler")
	if main.Image != "quay.io/sustainable_computing_io/kepler:v0.12.0" {
		t.Errorf("image = %q", main.Image)
	}
	if got := strings.Join(main.Args, " "); got != "--config.file=/etc/kepler/config.yaml --kube.node-name=$(NODE_NAME)" {
		t.Errorf("args = %q", got)
	}
	for _, e := range main.Env {
		if e.Name == "BIND_ADDRESS" {
			t.Error("BIND_ADDRESS is release-0.7's; v0.10+ listens where its config file says")
		}
	}
	if envSource(main, "NODE_NAME") == nil {
		t.Error("NODE_NAME (the node whose pods the informer lists) is not set")
	}
	cm, ok := r.configmaps["continuum-telemetry-kepler-config"]
	if !ok {
		t.Fatalf("no config ConfigMap; have %v", keys(r.configmaps))
	}
	var cfg struct {
		CPU struct {
			PreferredMeters []string `json:"preferredMeters"`
		} `json:"cpu"`
		Exporter struct {
			Prometheus struct {
				MetricsLevel    []string `json:"metricsLevel"`
				DebugCollectors []string `json:"debugCollectors"`
			} `json:"prometheus"`
		} `json:"exporter"`
		Web struct {
			ListenAddresses []string `json:"listenAddresses"`
		} `json:"web"`
		Kube struct {
			Enabled     bool `json:"enabled"`
			PodInformer struct {
				Mode string `json:"mode"`
			} `json:"podInformer"`
		} `json:"kube"`
	}
	if err := sigsyaml.Unmarshal([]byte(cm.Data["config.yaml"]), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Web.ListenAddresses; len(got) != 1 || got[0] != ":9103" {
		t.Errorf("listenAddresses = %v, want [:9103]: the Service, the scrape job and the NetworkPolicy use 9103", got)
	}
	if !cfg.Kube.Enabled || cfg.Kube.PodInformer.Mode != "apiserver" {
		t.Errorf("kube = %+v, want enabled with the apiserver informer", cfg.Kube)
	}
	if got := strings.Join(cfg.CPU.PreferredMeters, ","); got != "rapl,hwmon" {
		t.Errorf("preferredMeters = %v: fake readings must never be offered", got)
	}
	if got := strings.Join(cfg.Exporter.Prometheus.MetricsLevel, ","); got != "node,pod" {
		t.Errorf("metricsLevel = %v, want node,pod (the process level is one series per host process)", got)
	}
	if len(cfg.Exporter.Prometheus.DebugCollectors) != 0 {
		t.Errorf("debugCollectors = %v", cfg.Exporter.Prometheus.DebugCollectors)
	}
	// v0.10+ is amd64-only upstream.
	if pod.NodeSelector["kubernetes.io/arch"] != "amd64" || pod.NodeSelector["kubernetes.io/os"] != "linux" {
		t.Errorf("nodeSelector = %v, want os=linux and arch=amd64", pod.NodeSelector)
	}
	if main.ReadinessProbe == nil || main.ReadinessProbe.HTTPGet == nil || main.ReadinessProbe.HTTPGet.Path != "/probe/readyz" {
		t.Errorf("readiness = %+v, want /probe/readyz", main.ReadinessProbe)
	}
	if main.LivenessProbe == nil || main.LivenessProbe.HTTPGet == nil || main.LivenessProbe.HTTPGet.Path != "/probe/livez" {
		t.Errorf("liveness = %+v, want /probe/livez", main.LivenessProbe)
	}
	// The permissions stay exactly the pods-only rule: a kubelet-mode Kepler would have needed nodes/proxy.
	cr := r.clusterroles["continuum-agent-kepler"]
	if len(cr.Rules) != 1 || strings.Join(cr.Rules[0].Resources, ",") != "pods" || strings.Join(cr.Rules[0].Verbs, ",") != "get,list,watch" {
		t.Errorf("Kepler ClusterRole = %+v", cr.Rules)
	}
	if ann := r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Annotations["checksum/config"]; ann == "" {
		t.Error("no checksum/config annotation: a changed Kepler config would not roll the pods")
	}
}

func TestKeplerPowercapConfigChangeRollsThePods(t *testing.T) {
	a := render(t, append(append([]string{}, energyOn...), powercapOn()...)...)
	b := render(t, append(append(append([]string{}, energyOn...), powercapOn()...), "--set", "telemetry.energy.metrics.powercap.metricLevels={node,pod,container}")...)
	ca := a.daemonsets["continuum-telemetry-kepler"].Spec.Template.Annotations["checksum/config"]
	cb := b.daemonsets["continuum-telemetry-kepler"].Spec.Template.Annotations["checksum/config"]
	if ca == "" || ca == cb {
		t.Errorf("checksums %q / %q: the levels changed, the pods must roll", ca, cb)
	}
	if !strings.Contains(b.configmaps["continuum-telemetry-kepler-config"].Data["config.yaml"], `["node","pod","container"]`) {
		t.Errorf("levels not rendered: %s", b.configmaps["continuum-telemetry-kepler-config"].Data["config.yaml"])
	}
}

func TestKeplerPowercapRespectsAnArchitectureTheOperatorSets(t *testing.T) {
	_, pod := keplerDS(t, append(powercapOn(), "--set-string", "telemetry.energy.metrics.nodeSelector.kubernetes\\.io/arch=arm64")...)
	if pod.NodeSelector["kubernetes.io/arch"] != "arm64" {
		t.Errorf("nodeSelector = %v: an explicit architecture must win (they may run their own image)", pod.NodeSelector)
	}
}

func TestKeplerEngineAndLevelsAreValidated(t *testing.T) {
	if out, err := helmTemplate(t, append(append([]string{}, energyOn...), "--set", "telemetry.energy.metrics.engine=rapl")...); err == nil || !strings.Contains(out, "engine") {
		t.Errorf("an unknown engine rendered or gave an unhelpful error: %v\n%s", err, out)
	}
	out, err := helmTemplate(t, append(append(append([]string{}, energyOn...), powercapOn()...), "--set", "telemetry.energy.metrics.powercap.metricLevels={pod}")...)
	if err == nil || !strings.Contains(out, "must include node") {
		t.Errorf("metricLevels without node rendered or gave an unhelpful error: %v\n%s", err, out)
	}
	if out, err := helmTemplate(t, append(append(append([]string{}, energyOn...), powercapOn()...), "--set", "telemetry.energy.metrics.powercap.metricLevels={node,gpu}")...); err == nil {
		t.Errorf("an unknown metric level rendered:\n%s", out)
	}
	// A bad digest names the image that is actually used.
	out, err = helmTemplate(t, append(append(append([]string{}, energyOn...), powercapOn()...), "--set", "telemetry.energy.metrics.powercapImage.digest=sha256:abc")...)
	if err == nil {
		t.Errorf("a malformed digest rendered:\n%s", out)
	}
}

func TestKeplerNodeChecksCanBeTurnedOff(t *testing.T) {
	for _, extra := range [][]string{{}, powercapOn()} {
		_, pod := keplerDS(t, append(append([]string{}, extra...), "--set", "telemetry.energy.metrics.nodeChecks=false")...)
		if len(pod.InitContainers) != 0 {
			t.Errorf("%v: init containers = %v", extra, pod.InitContainers)
		}
		if container(t, pod.Containers, "kepler").StartupProbe != nil {
			t.Errorf("%v: startup probe stayed", extra)
		}
	}
}

// A pod held by the preflight is Pending: the cluster collector must not scrape it (a permanently failing target), but a
// Kepler that is Running and broken keeps its target (up=0 is the signal).
func TestKeplerScrapeJobSkipsPodsThatNeverStarted(t *testing.T) {
	jobs := scrapeConfigs(t, render(t, energyOn...))
	var keeps bool
	for _, rc := range jobs["kepler"]["relabel_configs"].([]any) {
		m := rc.(map[string]any)
		if m["action"] == "keep" && m["regex"] == "Running" && m["source_labels"].([]any)[0] == "__meta_kubernetes_pod_phase" {
			keeps = true
		}
	}
	if !keeps {
		t.Errorf("the kepler job does not keep only Running pods: %v", jobs["kepler"]["relabel_configs"])
	}
}

func energyScopeFilter(t *testing.T, r rendered) (map[string]any, []any) {
	t.Helper()
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	procs := sub(t, cfg, "processors")
	f, _ := procs["filter/scope_energy"].(map[string]any)
	pipe := sub(t, cfg, "service", "pipelines", "metrics/infra")
	return f, pipe["processors"].([]any)
}

// Energy follows the shared namespace scope only when asked, and then by the namespace label of each series (the resource is the
// exporter pod's), per engine.
func TestKeplerApplyScope(t *testing.T) {
	f, _ := energyScopeFilter(t, render(t, append(append([]string{}, energyOn...), "--set", "telemetry.scope.namespaces={prod}", "--set", "telemetry.energy.metrics.applyScope=false")...))
	if f != nil {
		t.Fatal("energy is scoped with applyScope=false")
	}
	for engine, label := range map[string]string{"ebpf": "container_namespace", "powercap": "pod_namespace"} {
		r := render(t, append(append([]string{}, energyOn...), "--set", "telemetry.energy.metrics.engine="+engine, "--set", "telemetry.energy.metrics.applyScope=true",
			"--set", "telemetry.scope.namespaces={prod,stage}", "--set", "telemetry.scope.exclude={kube-system}")...)
		f, procs := energyScopeFilter(t, r)
		if f == nil {
			t.Fatalf("%s: no filter/scope_energy", engine)
		}
		conds := fmt.Sprint(f["metric_conditions"])
		for _, want := range []string{`datapoint.attributes["` + label + `"] != nil`, "^(prod|stage)$", "^(kube-system)$", `resource.attributes["service.name"] == "kepler"`, `metric.name != "up"`, "^scrape_"} {
			if !strings.Contains(conds, want) {
				t.Errorf("%s: conditions lack %q: %s", engine, want, conds)
			}
		}
		if engine == "ebpf" && strings.Contains(conds, "pod_namespace") || engine == "powercap" && strings.Contains(conds, "container_namespace") {
			t.Errorf("%s: reads the other engine's label: %s", engine, conds)
		}
		var order []string
		for _, p := range procs {
			order = append(order, p.(string))
		}
		in := strings.Join(order, ",")
		if !strings.Contains(in, "transform/kepler_node,filter/scope_energy") {
			t.Errorf("%s: the filter must follow the identity transform: %s", engine, in)
		}
	}
	// Not for an existing endpoint (its labels are not known).
	r := render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.source=existing",
		"--set", "telemetry.energy.metrics.existing.prometheusEndpoint=k.x:9102", "--set", "telemetry.energy.metrics.applyScope=true", "--set", "telemetry.scope.namespaces={prod}")
	if f, _ := energyScopeFilter(t, r); f != nil {
		t.Error("applyScope filtered an existing endpoint")
	}
}

// ---- the shell the init container and the startup probe run, executed for real ----

func needTools(t *testing.T, tools ...string) {
	t.Helper()
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// runScript runs script with sh and returns what it printed and whether it was still running after d (held) or exited, and its exit code.
func runScript(t *testing.T, script string, d time.Duration) (out string, held bool, code int) {
	t.Helper()
	logf := filepath.Join(t.TempDir(), "out")
	f, err := os.Create(logf)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command("sh", "-c", script)
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	select {
	case err := <-done:
		code = 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		}
	case <-time.After(d):
		held = true
		// A SIGTERM (what the kubelet sends when the pod is deleted) must end it at once, not after the grace period.
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		case <-time.After(3 * time.Second):
			t.Error("the held preflight ignored SIGTERM")
		}
	}
	b, _ := os.ReadFile(logf)
	return string(b), held, code
}

// preflightScript is the init container's script with its absolute paths (/usr/bin/bpftool, /sys) moved into dir.
func preflightScript(t *testing.T, dir string, extra ...string) string {
	t.Helper()
	_, pod := keplerDS(t, extra...)
	s := strings.Join(container(t, pod.InitContainers, "preflight").Args, "\n")
	s = strings.ReplaceAll(s, "/usr/bin/bpftool", filepath.Join(dir, "bpftool"))
	s = strings.ReplaceAll(s, "/sys/", dir+"/sys/")
	return s
}

func fakeBpftool(t *testing.T, dir, output string, exit int) {
	t.Helper()
	body := fmt.Sprintf("#!/bin/sh\ncat <<'EOF'\n%s\nEOF\nexit %d\n", output, exit)
	if err := os.WriteFile(filepath.Join(dir, "bpftool"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestKeplerEbpfPreflightHoldsOnlyOnADefiniteNo(t *testing.T) {
	needTools(t, "sh", "timeout")
	// What bpftool of the release-0.7.12 image printed on a kernel that refuses tracing programs (the failure that panics Kepler).
	refused := "eBPF program_type kprobe is available\neBPF program_type tracepoint is available\neBPF program_type tracing is NOT available"
	dir := t.TempDir()
	fakeBpftool(t, dir, refused, 0)
	out, held, _ := runScript(t, preflightScript(t, dir), 2*time.Second)
	if !held || !strings.Contains(out, "NOT SUPPORTED ON THIS NODE") || !strings.Contains(out, "engine=powercap") {
		t.Errorf("a kernel that refuses tracing programs: held=%v out=%q", held, out)
	}

	dir = t.TempDir()
	fakeBpftool(t, dir, "eBPF program_type kprobe is available\neBPF program_type tracing is available", 0)
	_ = os.MkdirAll(filepath.Join(dir, "sys/kernel/btf"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sys/kernel/btf/vmlinux"), nil, 0o644)
	out, held, code := runScript(t, preflightScript(t, dir), 5*time.Second)
	if held || code != 0 || strings.Contains(out, "NOT SUPPORTED") || strings.Contains(out, "warning") {
		t.Errorf("a kernel that accepts them: held=%v code=%d out=%q", held, code, out)
	}

	// Missing BTF alone does not hold (the probe is the judge), but says so.
	dir = t.TempDir()
	fakeBpftool(t, dir, "eBPF program_type tracing is available", 0)
	out, held, code = runScript(t, preflightScript(t, dir), 5*time.Second)
	if held || code != 0 || !strings.Contains(out, "BTF") {
		t.Errorf("no BTF: held=%v code=%d out=%q", held, code, out)
	}

	// A probe that cannot run is not a verdict: Kepler is started.
	dir = t.TempDir() // no bpftool at all
	out, held, code = runScript(t, preflightScript(t, dir), 5*time.Second)
	if held || code != 0 {
		t.Errorf("no bpftool: held=%v code=%d out=%q (an inconclusive check must let Kepler try)", held, code, out)
	}
	dir = t.TempDir()
	fakeBpftool(t, dir, "Error: can't probe", 1)
	if _, held, code = runScript(t, preflightScript(t, dir), 5*time.Second); held || code != 0 {
		t.Errorf("a failing probe: held=%v code=%d", held, code)
	}
}

func TestKeplerPowercapPreflightNeedsAnEnergySource(t *testing.T) {
	needTools(t, "sh")
	args := powercapOn()
	dir := t.TempDir() // a cloud VM: no powercap, no hwmon
	_ = os.MkdirAll(filepath.Join(dir, "sys/class"), 0o755)
	out, held, _ := runScript(t, preflightScript(t, dir, args...), 2*time.Second)
	if !held || !strings.Contains(out, "NOT SUPPORTED ON THIS NODE") || !strings.Contains(out, "engine=ebpf") {
		t.Errorf("no energy source: held=%v out=%q", held, out)
	}

	// hwmon with only temperature sensors (every machine has some) is not a power source.
	dir = t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "sys/class/hwmon/hwmon0"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "sys/class/hwmon/hwmon0/temp1_input"), []byte("40000"), 0o644)
	if _, held, _ = runScript(t, preflightScript(t, dir, args...), 2*time.Second); !held {
		t.Error("a temperature sensor was taken for a power source")
	}

	for name, file := range map[string]string{
		"RAPL":       "sys/class/powercap/intel-rapl:0/energy_uj",
		"hwmon":      "sys/class/hwmon/hwmon3/power1_input",
		"hwmon enrg": "sys/class/hwmon/hwmon3/energy1_input",
	} {
		dir = t.TempDir()
		p := filepath.Join(dir, file)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("1"), 0o644)
		out, held, code := runScript(t, preflightScript(t, dir, args...), 5*time.Second)
		if held || code != 0 || !strings.Contains(out, "power source is present") {
			t.Errorf("%s: held=%v code=%d out=%q", name, held, code, out)
		}
	}
}

// The startup probe must not be satisfied by the series Kepler v0.10+ serves even when it collects nothing
// (kepler_build_info, kepler_node_cpu_info) - that was found by running the real image on a node where every collection failed.
func TestKeplerStartupProbeChecksForRealData(t *testing.T) {
	needTools(t, "sh", "curl", "grep")
	for _, tc := range []struct {
		extra  []string
		body   string
		wantOK bool
	}{
		{powercapOn(), "kepler_build_info{version=\"v0.12.0\"} 1\nkepler_node_cpu_info{processor=\"0\"} 1\n", false},
		{powercapOn(), "kepler_node_cpu_info{processor=\"0\"} 1\nkepler_node_cpu_watts{node_name=\"n\",zone=\"package\"} 12.5\n", true},
		{nil, "", false},
		{nil, "kepler_node_info{cpu_architecture=\"Skylake\"} 1\n", true},
	} {
		_, pod := keplerDS(t, tc.extra...)
		probe := container(t, pod.Containers, "kepler").StartupProbe.Exec.Command
		if len(probe) != 3 || probe[0] != "/bin/sh" {
			t.Fatalf("probe command = %v", probe)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, tc.body) })}
		go srv.Serve(ln)
		script := strings.ReplaceAll(probe[2], "localhost:9103", ln.Addr().String())
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := exec.CommandContext(ctx, "sh", "-c", script).CombinedOutput()
		cancel()
		srv.Close()
		if (err == nil) != tc.wantOK {
			t.Errorf("engine %v, body %q: probe ok=%v, want %v (out %q)", tc.extra, tc.body, err == nil, tc.wantOK, out)
		}
		if !tc.wantOK && !strings.Contains(string(out), "kepler_node_") {
			t.Errorf("a failing probe must say what is missing, got %q", out)
		}
	}
}

// `helm template` does not print NOTES.txt, so (as server_fusion_test.go does) the notes go through a ConfigMap.
func TestNotesTellAHeldKeplerPodFromABrokenOne(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), "continuum-agent")
	copyTree(t, "continuum-agent", dir)
	text, err := os.ReadFile(filepath.Join(dir, "templates", "NOTES.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"_notes.tpl": `{{- define "test.notes" -}}` + string(text) + `{{- end -}}`,
		"notes.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: {{ include \"test.notes\" . | quote }}\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, "templates", name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	notes := func(extra ...string) string {
		args := append(append([]string{"template", "ct", dir, "--show-only", "templates/notes.yaml"}, baseSet...), "--set", "telemetry.export.otlp.endpoint=x:4317")
		out, err := exec.Command(h, append(args, extra...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template of the notes: %v\n%s", err, out)
		}
		return string(out)
	}
	on := notes("--set", "telemetry.energy.metrics.enabled=true")
	for _, want := range []string{"Energy: Kepler, ebpf engine", "Init:0/1", "logs <pod> -c preflight", "A pod that restarts is a Kepler that is broken"} {
		if !strings.Contains(on, want) {
			t.Errorf("notes lack %q:\n%s", want, on)
		}
	}
	if pc := notes("--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.engine=powercap"); !strings.Contains(pc, "powercap engine") || !strings.Contains(pc, "amd64 nodes with RAPL") {
		t.Errorf("powercap notes:\n%s", pc)
	}
	if off := notes("--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.energy.metrics.nodeChecks=false"); strings.Contains(off, "Init:0/1") {
		t.Errorf("notes explain a preflight that is off:\n%s", off)
	}
	if none := notes(); strings.Contains(none, "Kepler,") {
		t.Errorf("notes mention Kepler with energy off:\n%s", none)
	}
}
