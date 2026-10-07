package chart

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"
)

// Chart-level deployment correctness of the telemetry part: what exactly each single-signal intent deploys (and
// nothing else), what the collectors' pods are probed on, which API permissions they hold, what the egress lockdown
// lets through, what survives a `helm upgrade --reuse-values`, and that the images the defaults name exist.

// doc is one rendered manifest, loosely typed.
type doc map[string]any

// renderDocs renders the chart (the same way render does) and returns every manifest, in order.
func renderDocs(t *testing.T, extra ...string) []doc {
	t.Helper()
	out, err := helmTemplate(t, extra...)
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", extra, err, out)
	}
	var docs []doc
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(out), 4096)
	for {
		var d doc
		if err := dec.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if d["kind"] != nil {
			docs = append(docs, d)
		}
	}
	return docs
}

func (d doc) kind() string { return fmt.Sprint(d["kind"]) }
func (d doc) name() string { return fmt.Sprint(d["metadata"].(map[string]any)["name"]) }

// telemetryObjects is what a render holds that belongs to telemetry (everything named continuum-telemetry*, and the
// telemetry ServiceAccount, ClusterRole and ClusterRoleBinding), as "Kind/name" -> manifest.
func telemetryObjects(docs []doc) map[string]doc {
	out := map[string]doc{}
	for _, d := range docs {
		if strings.HasPrefix(d.name(), "continuum-telemetry") || strings.HasPrefix(d.name(), "continuum-agent-telemetry") {
			out[d.kind()+"/"+d.name()] = d
		}
	}
	return out
}

func podSpecOf(t *testing.T, d doc) corev1.PodSpec {
	t.Helper()
	b, _ := json.Marshal(d["spec"].(map[string]any)["template"].(map[string]any)["spec"])
	var ps corev1.PodSpec
	if err := json.Unmarshal(b, &ps); err != nil {
		t.Fatal(err)
	}
	return ps
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ruleStrings is a ClusterRole's rules as "group:resource,resource:verb,verb", sorted.
func ruleStrings(d doc) []string {
	var out []string
	for _, r := range d["rules"].([]any) {
		rm := r.(map[string]any)
		join := func(k string) string {
			var s []string
			for _, x := range rm[k].([]any) {
				s = append(s, fmt.Sprint(x))
			}
			return strings.Join(s, ",")
		}
		out = append(out, join("apiGroups")+":"+join("resources")+":"+join("verbs"))
	}
	sort.Strings(out)
	return out
}

var clusterStateRules = []string{
	":nodes,namespaces,pods,replicationcontrollers,resourcequotas,services:get,list,watch",
	"apps:deployments,replicasets,statefulsets,daemonsets:get,list,watch",
	"batch:jobs,cronjobs:get,list,watch",
	"autoscaling:horizontalpodautoscalers:get,list,watch",
}

// Each single-signal intent deploys exactly the extractors that signal needs: no other workload, receiver, RBAC rule,
// volume, host path, port or Service. The expectation is spelled out for every signal.
func TestSingleSignalIntentsDeployOnlyTheirOwnExtractors(t *testing.T) {
	podsOnly := []string{":pods:get,list,watch"}
	kubelet := []string{":nodes/stats:get", ":pods:get,list,watch"}
	type want struct {
		set       []string            // the --set flags beyond the destination
		objects   []string            // every telemetry object, Kind/name
		rules     []string            // the telemetry ClusterRole's rules
		hostPaths map[string][]string // workload -> hostPath volumes
		ports     map[string][]int32  // workload -> container ports
		receivers map[string][]string // collector ConfigMap -> receivers
	}
	hostObjs := []string{"ClusterRole/continuum-agent-telemetry", "ClusterRoleBinding/continuum-agent-telemetry-default", "ServiceAccount/continuum-agent-telemetry",
		"ConfigMap/continuum-telemetry-host-config", "DaemonSet/continuum-telemetry-host", "Service/continuum-telemetry-host-metrics", "NetworkPolicy/continuum-telemetry-host-health"}
	clusterObjs := []string{"ClusterRole/continuum-agent-telemetry", "ClusterRoleBinding/continuum-agent-telemetry-default", "ServiceAccount/continuum-agent-telemetry",
		"ConfigMap/continuum-telemetry-cluster-config", "Deployment/continuum-telemetry-cluster", "Service/continuum-telemetry-cluster-metrics"}
	withOTLP := append(append([]string{}, clusterObjs...), "Service/continuum-telemetry-cluster")
	withKepler := append(append([]string{}, clusterObjs...), "DaemonSet/continuum-telemetry-kepler", "Service/continuum-telemetry-kepler")
	withDCGM := append(append([]string{}, clusterObjs...), "DaemonSet/continuum-telemetry-dcgm", "Service/continuum-telemetry-dcgm")
	host, cluster := "continuum-telemetry-host-config", "continuum-telemetry-cluster-config"
	const hp, cp = "DaemonSet/continuum-telemetry-host", "Deployment/continuum-telemetry-cluster"
	cases := map[string]want{
		"resourceUsage": {
			set: []string{"telemetry.resourceUsage.metrics.enabled=true"}, objects: hostObjs, rules: kubelet,
			hostPaths: map[string][]string{hp: {"/proc"}}, ports: map[string][]int32{hp: {13133}}, receivers: map[string][]string{host: {"hostmetrics", "kubelet_stats"}}},
		"nodeRuntime": {
			set: []string{"telemetry.nodeRuntime.metrics.enabled=true"}, objects: hostObjs, rules: kubelet,
			ports: map[string][]int32{hp: {13133}}, receivers: map[string][]string{host: {"kubelet_stats"}}},
		"systemLogs": {
			set: []string{"telemetry.systemLogs.logs.enabled=true"}, objects: hostObjs, rules: podsOnly,
			hostPaths: map[string][]string{hp: {"/var/log/pods"}}, ports: map[string][]int32{hp: {13133}}, receivers: map[string][]string{host: {"filelog/containers"}}},
		"kubernetesState": {
			set: []string{"telemetry.kubernetesState.metrics.enabled=true"}, objects: clusterObjs, rules: append([]string{":pods:get,list,watch"}, clusterStateRules...),
			ports: map[string][]int32{cp: {13133}}, receivers: map[string][]string{cluster: {"k8s_cluster"}}},
		"kubernetesEvents": {
			set: []string{"telemetry.kubernetesEvents.logs.enabled=true"}, objects: clusterObjs, rules: []string{":events:get,list,watch", ":pods:get,list,watch"},
			ports: map[string][]int32{cp: {13133}}, receivers: map[string][]string{cluster: {"k8sobjects"}}},
		"energy (bundled Kepler)": {
			set: []string{"telemetry.energy.metrics.enabled=true"}, objects: withKepler, rules: podsOnly,
			hostPaths: map[string][]string{"DaemonSet/continuum-telemetry-kepler": {"/proc", "/sys"}},
			ports:     map[string][]int32{cp: {13133}, "DaemonSet/continuum-telemetry-kepler": {9103}}, receivers: map[string][]string{cluster: {"prometheus/infra"}}},
		"energy (existing endpoint)": {
			set:     []string{"telemetry.energy.metrics.enabled=true", "telemetry.energy.metrics.source=existing", "telemetry.energy.metrics.existing.prometheusEndpoint=kepler.x:9102"},
			objects: clusterObjs, rules: podsOnly, ports: map[string][]int32{cp: {13133}}, receivers: map[string][]string{cluster: {"prometheus/infra"}}},
		"accelerators (bundled dcgm)": {
			set: []string{"telemetry.accelerators.metrics.enabled=true"}, objects: withDCGM, rules: podsOnly,
			// no hostPath: the kubelet pod-resources socket is mounted only with applyScope
			ports: map[string][]int32{cp: {13133}, "DaemonSet/continuum-telemetry-dcgm": {9400}}, receivers: map[string][]string{cluster: {"prometheus/infra"}}},
		"accelerators (existing endpoint)": {
			set:     []string{"telemetry.accelerators.metrics.enabled=true", "telemetry.accelerators.metrics.source=existing", "telemetry.accelerators.metrics.existing.prometheusEndpoint=dcgm.x:9400"},
			objects: clusterObjs, rules: podsOnly, ports: map[string][]int32{cp: {13133}}, receivers: map[string][]string{cluster: {"prometheus/infra"}}},
		"applicationMetrics": {
			set: []string{"telemetry.applicationMetrics.metrics.enabled=true"}, objects: withOTLP, rules: podsOnly,
			ports: map[string][]int32{cp: {4317, 4318, 13133}}, receivers: map[string][]string{cluster: {"otlp"}}},
		"applicationLogs": {
			set: []string{"telemetry.applicationLogs.logs.enabled=true"}, objects: withOTLP, rules: podsOnly,
			ports: map[string][]int32{cp: {4317, 4318, 13133}}, receivers: map[string][]string{cluster: {"otlp"}}},
		"traces": {
			set: []string{"telemetry.traces.traces.enabled=true"}, objects: withOTLP, rules: podsOnly,
			ports: map[string][]int32{cp: {4317, 4318, 13133}}, receivers: map[string][]string{cluster: {"otlp"}}},
		"networkLatency": {
			set: []string{"telemetry.networkLatency.metrics.enabled=true", "measurements.enabled=true"}, objects: withOTLP, rules: podsOnly,
			ports: map[string][]int32{cp: {4317, 4318, 13133}}, receivers: map[string][]string{cluster: {"otlp"}}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			args := append([]string{}, agentTel...)
			for _, s := range w.set {
				args = append(args, "--set", s)
			}
			objs := telemetryObjects(renderDocs(t, args...))
			if got, want := sortedKeys(objs), append([]string(nil), w.objects...); !reflect.DeepEqual(got, sortedStrings(want)) {
				t.Errorf("telemetry objects\n got %v\nwant %v", got, sortedStrings(want))
			}
			if got := ruleStrings(objs["ClusterRole/continuum-agent-telemetry"]); !reflect.DeepEqual(got, sortedStrings(w.rules)) {
				t.Errorf("ClusterRole rules\n got %v\nwant %v", got, sortedStrings(w.rules))
			}
			for key, d := range objs {
				if d.kind() != "DaemonSet" && d.kind() != "Deployment" {
					continue
				}
				ps := podSpecOf(t, d)
				var hostPaths []string
				for _, v := range ps.Volumes {
					if v.HostPath != nil {
						hostPaths = append(hostPaths, v.HostPath.Path)
					}
				}
				sort.Strings(hostPaths)
				if !reflect.DeepEqual(hostPaths, sortedStrings(w.hostPaths[key])) && !(len(hostPaths) == 0 && len(w.hostPaths[key]) == 0) {
					t.Errorf("%s hostPath volumes = %v, want %v", key, hostPaths, w.hostPaths[key])
				}
				var ports []int32
				for _, c := range ps.Containers {
					for _, p := range c.Ports {
						ports = append(ports, p.ContainerPort)
					}
				}
				sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
				if !reflect.DeepEqual(ports, w.ports[key]) {
					t.Errorf("%s container ports = %v, want %v", key, ports, w.ports[key])
				}
				if ps.HostNetwork || ps.HostIPC {
					t.Errorf("%s uses the host network or IPC", key)
				}
			}
			for cm, want := range w.receivers {
				cfg := otelConfig(t, map[string]string{"otel-collector-config.yaml": objs["ConfigMap/"+cm]["data"].(map[string]any)["otel-collector-config.yaml"].(string)})
				got := sortedKeys(sub(t, cfg, "receivers"))
				if !reflect.DeepEqual(got, sortedStrings(want)) {
					t.Errorf("%s receivers = %v, want %v", cm, got, want)
				}
			}
		})
	}
}

func sortedStrings(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}

// The agent's own workloads never mount a host path unless the node probe / flow observer was asked for, and the
// cluster collector never does (it holds the receiver reachable from the rest of the cluster).
func TestClusterCollectorMountsNothingFromTheHostAndRunsRestricted(t *testing.T) {
	all := append(append([]string{}, agentTel...), allSignals...)
	all = append(all, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true")
	r := render(t, all...)
	ps := r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec
	if reason := violatesRestricted(ps); reason != "" {
		t.Errorf("the cluster collector no longer passes the Pod Security restricted profile: %s", reason)
	}
	// nodeRuntime reads the kubelet over the network and mounts nothing, so it stays restricted too.
	r = render(t, append(append([]string{}, agentTel...), "--set", "telemetry.nodeRuntime.metrics.enabled=true")...)
	if reason := violatesRestricted(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec); reason != "" {
		t.Errorf("the nodeRuntime-only host collector no longer passes restricted: %s", reason)
	}
}

// violatesRestricted is the part of the Pod Security "restricted" profile this chart's pods can break (volume types,
// host namespaces, privilege, capabilities, user, seccomp); "" when the pod passes.
func violatesRestricted(ps corev1.PodSpec) string {
	if ps.HostNetwork || ps.HostPID || ps.HostIPC {
		return "host namespaces"
	}
	for _, v := range ps.Volumes {
		if v.HostPath != nil {
			return "hostPath volume " + v.HostPath.Path
		}
	}
	for _, c := range ps.Containers {
		sc := c.SecurityContext
		if sc == nil || (sc.Privileged != nil && *sc.Privileged) {
			return c.Name + ": privileged or no securityContext"
		}
		if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
			return c.Name + ": allowPrivilegeEscalation"
		}
		if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) > 0 {
			return c.Name + ": capabilities"
		}
		nonRoot := (sc.RunAsNonRoot != nil && *sc.RunAsNonRoot) || (ps.SecurityContext != nil && ps.SecurityContext.RunAsNonRoot != nil && *ps.SecurityContext.RunAsNonRoot)
		if !nonRoot {
			return c.Name + ": runAsNonRoot"
		}
		sec := ps.SecurityContext != nil && ps.SecurityContext.SeccompProfile != nil || sc.SeccompProfile != nil
		if !sec {
			return c.Name + ": seccompProfile"
		}
	}
	return ""
}

// The pods are probed on the collector itself. Before this, neither collector had a probe: a pod was Ready the moment
// its container started, whether or not the collector had come up, and the OTLP Service sent clients to it at once.
func TestCollectorsAreProbedOnTheirHealthCheck(t *testing.T) {
	args := append(append([]string{}, agentTel...), allSignals...)
	r := render(t, args...)
	for name, tc := range map[string]struct {
		pod corev1.PodSpec
		cm  string
	}{
		"host":    {r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec, "continuum-telemetry-host-config"},
		"cluster": {r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec, "continuum-telemetry-cluster-config"},
	} {
		c := tc.pod.Containers[0]
		var health *corev1.ContainerPort
		for i := range c.Ports {
			if c.Ports[i].Name == "health" {
				health = &c.Ports[i]
			}
		}
		if health == nil || health.ContainerPort != 13133 {
			t.Fatalf("%s: no health port 13133: %+v", name, c.Ports)
		}
		for probe, p := range map[string]*corev1.Probe{"startup": c.StartupProbe, "readiness": c.ReadinessProbe, "liveness": c.LivenessProbe} {
			if p == nil || p.HTTPGet == nil || p.HTTPGet.Port.StrVal != "health" || p.HTTPGet.Path != "/" {
				t.Errorf("%s: %s probe = %+v, want an HTTP GET / on the health port", name, probe, p)
			}
		}
		// A slow start (informers syncing on a big cluster) must not be a restart loop: liveness only runs after startup.
		if c.StartupProbe != nil && c.StartupProbe.PeriodSeconds*c.StartupProbe.FailureThreshold < 240 {
			t.Errorf("%s: startupProbe allows only %ds to start", name, c.StartupProbe.PeriodSeconds*c.StartupProbe.FailureThreshold)
		}
		cfg := otelConfig(t, r.configmaps[tc.cm].Data)
		ext := sub(t, cfg, "extensions", "health_check")
		if ext["endpoint"] != ":13133" {
			t.Errorf("%s: health_check endpoint = %v, want :13133 (every address of the pod)", name, ext["endpoint"])
		}
		found := false
		for _, e := range stringsOf(t, sub(t, cfg, "service")["extensions"]) {
			found = found || e == "health_check"
		}
		if !found {
			t.Errorf("%s: health_check is configured but not listed in service.extensions, so it would never start", name)
		}
	}
	// The probe does not depend on the export-health switch.
	r = render(t, append(args, "--set", "telemetry.health.enabled=false")...)
	if r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec.Containers[0].ReadinessProbe == nil {
		t.Error("telemetry.health.enabled=false removed the readiness probe")
	}
}

// The collector's real readiness check is the real binary's: this is skipped unless one is on the machine
// (CONTINUUM_OTELCOL, the path to otelcol-contrib), and then every configuration the chart renders must validate.
func TestRenderedCollectorConfigsValidateWithTheRealCollector(t *testing.T) {
	bin := os.Getenv("CONTINUUM_OTELCOL")
	if bin == "" {
		t.Skip("set CONTINUUM_OTELCOL to the otelcol-contrib binary (the version telemetry.collectorImage.tag names) to run this")
	}
	envs := append(os.Environ(), "POD_IP=10.0.0.5", "NODE_NAME=n1", "NODE_IP=10.0.0.1", "CONTINUUM_TELEMETRY_AUTH=x", "CONTINUUM_TELEMETRY_RECEIVER_AUTH=t")
	every := append(append([]string{}, agentTel...), allSignals...)
	every = append(every, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.applyScope=true",
		"--set", "telemetry.applicationMetrics.metrics.enabled=true", "--set", "telemetry.applicationLogs.logs.enabled=true",
		"--set", "telemetry.scope.namespaces[0]=shop", "--set", "telemetry.receiver.auth.enabled=true", "--set", "telemetry.receiver.auth.secretName=tok",
		"--set", "telemetry.export.queue.persistent.enabled=true")
	for name, args := range map[string][]string{"every signal": every, "kepler only": withTel("--set", "telemetry.energy.metrics.enabled=true"), "dcgm only": withTel("--set", "telemetry.accelerators.metrics.enabled=true")} {
		r := render(t, args...)
		for _, cm := range []string{"continuum-telemetry-host-config", "continuum-telemetry-cluster-config"} {
			c, ok := r.configmaps[cm]
			if !ok {
				continue
			}
			f := filepath.Join(t.TempDir(), "c.yaml")
			// hostmetrics insists its root_path exists where the validation runs; in the pod it is the host mounted at /hostfs.
			cfg := strings.ReplaceAll(c.Data["otel-collector-config.yaml"], "/hostfs", t.TempDir())
			os.WriteFile(f, []byte(cfg), 0o600)
			cmd := exec.Command(bin, "validate", "--config="+f)
			cmd.Env = envs
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("%s: %s does not validate: %v\n%s", name, cm, err, out)
			}
		}
	}
}

func TestTelemetryHealthPortCannotCollideWithTheCollectorsOwnPorts(t *testing.T) {
	for _, port := range []string{"4317", "4318", "13133"} {
		out, err := helmTemplate(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.health.port="+port)...)
		if err == nil || !strings.Contains(out, "telemetry.health.port") {
			t.Errorf("telemetry.health.port=%s accepted (the collector would fail to bind): %v\n%s", port, err, out)
		}
	}
	if out, err := helmTemplate(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.health.port=9464")...); err != nil {
		t.Errorf("telemetry.health.port=9464 rejected: %v\n%s", err, out)
	}
	// memory_limiter refuses to start when the spike limit is not below the limit.
	out, err := helmTemplate(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.processors.memoryLimiter.spikeLimitPercentage=80")...)
	if err == nil || !strings.Contains(out, "spikeLimitPercentage") {
		t.Errorf("a spike limit equal to the limit was accepted: %v\n%s", err, out)
	}
}

// The ClusterRole holds what the receivers and processors this chart configures call in collector v0.160.0, no more:
// the k8sattributes processor (as configured: metadata only, no labels/annotations, no *.uid) watches Pods and
// nothing else; namespaces and nodes are the cluster-state receiver's, and only exist when that signal is on.
func TestTelemetryRBACIsPodsUnlessASignalNeedsMore(t *testing.T) {
	for name, set := range map[string][]string{
		"logs":   {"telemetry.systemLogs.logs.enabled=true"},
		"energy": {"telemetry.energy.metrics.enabled=true"},
		"otlp":   {"telemetry.traces.traces.enabled=true", "telemetry.applicationMetrics.metrics.enabled=true"},
	} {
		args := append([]string{}, agentTel...)
		for _, s := range set {
			args = append(args, "--set", s)
		}
		r := render(t, args...)
		for _, rule := range r.clusterroles["continuum-agent-telemetry"].Rules {
			if !reflect.DeepEqual(rule.Resources, []string{"pods"}) {
				t.Errorf("%s: telemetry ClusterRole grants %v, want pods only", name, rule.Resources)
			}
			for _, v := range rule.Verbs {
				if v != "get" && v != "list" && v != "watch" {
					t.Errorf("%s: verb %s is not read-only", name, v)
				}
			}
		}
	}
	// Namespaces and nodes are there once the cluster-state receiver is.
	r := render(t, withTel("--set", "telemetry.kubernetesState.metrics.enabled=true")...)
	got := map[string]bool{}
	for _, rule := range r.clusterroles["continuum-agent-telemetry"].Rules {
		for _, res := range rule.Resources {
			got[res] = true
		}
	}
	if !got["namespaces"] || !got["nodes"] {
		t.Errorf("cluster-state receiver without namespaces/nodes: %v", got)
	}
}

// Kepler and dcgm are scraped by pod IP through Kubernetes service discovery: it lists only this release's own pods in
// its own namespace (not every pod of the cluster), by the exact labels the DaemonSet's pods carry.
func TestBundledScrapeJobsOnlyWatchTheirOwnPods(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true")...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	jobs := sub(t, cfg, "receivers", "prometheus/infra", "config")["scrape_configs"].([]any)
	if len(jobs) != 2 {
		t.Fatalf("scrape jobs = %v", jobs)
	}
	for _, j := range jobs {
		job := j.(map[string]any)
		name := job["job_name"].(string)
		sds, _ := job["kubernetes_sd_configs"].([]any)
		if len(sds) == 0 {
			t.Errorf("%s: no kubernetes_sd_configs (job %v): it discovers pods by whatever static target it was given", name, job)
			continue
		}
		sd := sds[0].(map[string]any)
		nsm, _ := sd["namespaces"].(map[string]any)
		ns, _ := nsm["names"].([]any)
		if len(ns) != 1 || ns[0] != "default" {
			t.Errorf("%s: service discovery namespaces = %v, want only the release namespace", name, ns)
		}
		sels, _ := sd["selectors"].([]any)
		if len(sels) == 0 {
			t.Errorf("%s: service discovery has no selector, so it lists every pod of the namespace", name)
			continue
		}
		sel := sels[0].(map[string]any)
		want := map[string]string{"kepler": "continuum-telemetry-kepler", "dcgm-exporter": "continuum-telemetry-dcgm"}[name]
		if sel["role"] != "pod" || sel["label"] != "app.kubernetes.io/name="+want+",app.kubernetes.io/instance=ct" {
			t.Errorf("%s: selector = %v", name, sel)
		}
		// The label the selector uses is the one the pods carry.
		var carried map[string]string
		if name == "kepler" {
			carried = r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Labels
		} else {
			carried = r.daemonsets["continuum-telemetry-dcgm"].Spec.Template.Labels
		}
		if carried["app.kubernetes.io/name"] != want || carried["app.kubernetes.io/instance"] != "ct" {
			t.Errorf("%s: pod labels %v do not match the selector", name, carried)
		}
	}
	// The dcgm scrape follows telemetry.accelerators.metrics.interval (it used to be read nowhere).
	r = render(t, withTel("--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.interval=45s")...)
	cfg = otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data)
	job := sub(t, cfg, "receivers", "prometheus/infra", "config")["scrape_configs"].([]any)[0].(map[string]any)
	if job["scrape_interval"] != "45s" {
		t.Errorf("dcgm scrape_interval = %v, want 45s", job["scrape_interval"])
	}
}

// With the egress lockdown on, the cluster collector still reaches what it scrapes inside the cluster: the policy that
// selects it denies every address it does not list, so Kepler, dcgm-exporter and the scrape targets each need a rule,
// and only the cluster collector gets them.
func TestTelemetryEgressLockdownStillAllowsTheInClusterScrapes(t *testing.T) {
	lock := []string{"networkPolicy.telemetryEgress.enabled=true", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317",
		"networkPolicy.telemetryEgress.apiServerCIDRs[0]=10.1.1.1/32"}
	scrape := []string{"telemetry.applicationMetrics.metrics.enabled=true", "telemetry.applicationMetrics.metrics.scrapeTargets[0].jobName=shop",
		"telemetry.applicationMetrics.metrics.scrapeTargets[0].namespace=shop", "telemetry.applicationMetrics.metrics.scrapeTargets[0].podLabelSelector=app=cart",
		"telemetry.applicationMetrics.metrics.scrapeTargets[0].port=9090"}
	flags := func(sets ...[]string) []string {
		out := append([]string{}, agentTel...)
		for _, s := range sets {
			for _, x := range s {
				out = append(out, "--set", x)
			}
		}
		return out
	}
	// Locked down, nothing scraped inside the cluster: no extra policy.
	r := render(t, flags(lock, []string{"telemetry.kubernetesState.metrics.enabled=true"})...)
	if _, ok := r.policies["continuum-telemetry-scrape-egress"]; ok {
		t.Error("a scrape-egress policy although nothing is scraped inside the cluster")
	}
	// Not locked down: no policy at all (a selected pod would be cut off from everything else).
	r = render(t, flags(scrape, []string{"telemetry.energy.metrics.enabled=true"})...)
	if _, ok := r.policies["continuum-telemetry-scrape-egress"]; ok || len(r.policies["continuum-telemetry-egress"].Spec.Egress) != 0 {
		t.Error("an egress policy although telemetryEgress is off")
	}

	r = render(t, flags(lock, scrape, []string{"telemetry.energy.metrics.enabled=true", "telemetry.accelerators.metrics.enabled=true"})...)
	p, ok := r.policies["continuum-telemetry-scrape-egress"]
	if !ok {
		t.Fatal("no scrape-egress policy: the locked-down cluster collector cannot reach Kepler, dcgm-exporter or its scrape targets")
	}
	if got := p.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"]; got != "continuum-telemetry-cluster" || len(p.Spec.PodSelector.MatchExpressions) != 0 {
		t.Errorf("selects %v: only the cluster collector may be given these", p.Spec.PodSelector)
	}
	got := map[string]string{}
	for _, e := range p.Spec.Egress {
		if len(e.To) != 1 || len(e.Ports) != 1 || e.Ports[0].Port == nil {
			t.Fatalf("unexpected rule %+v", e)
		}
		peer := e.To[0]
		switch {
		case peer.PodSelector != nil && peer.NamespaceSelector == nil:
			got[peer.PodSelector.MatchLabels["app.kubernetes.io/name"]] = e.Ports[0].Port.String()
		case peer.NamespaceSelector != nil && peer.PodSelector == nil:
			got["ns:"+peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]] = e.Ports[0].Port.String()
		default:
			t.Errorf("rule %+v is neither a pod nor a namespace", e)
		}
	}
	want := map[string]string{"continuum-telemetry-kepler": "9103", "continuum-telemetry-dcgm": "9400", "ns:shop": "9090"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scrape-egress rules = %v, want %v", got, want)
	}
	// The ports allowed are the ports the scraped containers declare.
	for name, ds := range map[string]string{"continuum-telemetry-kepler": "9103", "continuum-telemetry-dcgm": "9400"} {
		var declared []string
		for _, c := range r.daemonsets[name].Spec.Template.Spec.Containers {
			for _, pt := range c.Ports {
				declared = append(declared, fmt.Sprint(pt.ContainerPort))
			}
		}
		if !reflect.DeepEqual(declared, []string{ds}) {
			t.Errorf("%s declares %v, the policy allows %s", name, declared, ds)
		}
	}
}

// Kepler and dcgm get the priority class and pull secrets the other pods of the release get.
func TestEveryTelemetryWorkloadGetsPriorityClassAndPullSecrets(t *testing.T) {
	args := append(append([]string{}, agentTel...), allSignals...)
	args = append(args, "--set", "telemetry.energy.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.enabled=true",
		"--set", "priorityClassName=high", "--set", "imagePullSecrets[0].name=rc")
	r := render(t, args...)
	pods := map[string]corev1.PodSpec{
		"host": r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec, "cluster": r.deployments["continuum-telemetry-cluster"].Spec.Template.Spec,
		"kepler": r.daemonsets["continuum-telemetry-kepler"].Spec.Template.Spec, "dcgm": r.daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec,
	}
	for name, ps := range pods {
		if ps.PriorityClassName != "high" || len(ps.ImagePullSecrets) != 1 {
			t.Errorf("%s: priorityClassName=%q imagePullSecrets=%v", name, ps.PriorityClassName, ps.ImagePullSecrets)
		}
	}
	// The two collectors hold a token, and so does Kepler (its own ServiceAccount, read access to pods only: without it
	// every container is attributed to system_processes); dcgm-exporter holds none.
	for name, ps := range pods {
		want := name != "dcgm"
		if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken != want {
			t.Errorf("%s: automountServiceAccountToken = %v, want %v", name, ps.AutomountServiceAccountToken, want)
		}
	}
}

// A credential read into an environment variable is read once, at start: rotating the Secret does nothing until the
// pod restarts, so the pods carry a digest of what those Secrets hold (renders offline carry none, like checksum/mtls).
func TestCollectorsRestartWhenACredentialSecretChanges(t *testing.T) {
	k, kc := startFakeKubernetes(t)
	k.set("export-token", map[string]string{"token": "Bearer one"})
	k.set("metrics-token", map[string]string{"token": "Bearer m1"})
	k.set("recv-token", map[string]string{"token": "r1"})
	k.set("unrelated", map[string]string{"x": "1"})
	args := append(append([]string{}, baseSet...), "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.export.otlp.auth.secretName=export-token",
		"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.traces.traces.enabled=true",
		"--set", "telemetry.export.routes.metrics.endpoint=m:4317", "--set", "telemetry.export.routes.metrics.auth.secretName=metrics-token",
		"--set", "telemetry.receiver.auth.enabled=true", "--set", "telemetry.receiver.auth.secretName=recv-token")
	sums := func() (string, string) {
		a := podAnnotations(t, helmTemplateInCluster(t, Agent, kc, args...))
		return a["continuum-telemetry-host"]["checksum/auth"], a["continuum-telemetry-cluster"]["checksum/auth"]
	}
	h1, c1 := sums()
	if h1 == "" || c1 == "" {
		t.Fatalf("no checksum/auth although the Secrets exist: host %q cluster %q (lookups %v)", h1, c1, k.lookups())
	}
	if h2, c2 := sums(); h2 != h1 || c2 != c1 {
		t.Errorf("the checksum is not stable: %q/%q then %q/%q", h1, c1, h2, c2)
	}
	// The host collector exports metrics through its route (credential metrics-token) and never sees the receiver token or
	// the traces route; the cluster collector reads the receiver token as well.
	k.set("unrelated", map[string]string{"x": "2"})
	if h, c := sums(); h != h1 || c != c1 {
		t.Error("a Secret no pod reads changed the checksum")
	}
	k.set("recv-token", map[string]string{"token": "r2"})
	if h, c := sums(); h != h1 || c == c1 {
		t.Errorf("rotating the receiver token: host %q->%q (want unchanged), cluster %q->%q (want changed)", h1, h, c1, c)
	}
	h1, c1 = sums()
	k.set("metrics-token", map[string]string{"token": "Bearer m2"})
	if h, _ := sums(); h == h1 {
		t.Errorf("rotating the metrics route credential did not change the host checksum: %q -> %q", h1, h)
	}
	// Offline (helm template, Argo CD, Flux) and with the switch off: no annotation, and nothing fails.
	if a := podAnnotations(t, helmTemplateInCluster(t, Agent, "", args...))["continuum-telemetry-host"]; a["checksum/auth"] != "" || a["checksum/config"] == "" {
		t.Errorf("offline render annotations = %v", a)
	}
	if a := podAnnotations(t, helmTemplateInCluster(t, Agent, kc, append(args, "--set", "telemetry.rolloutOnSecretChange=false")...))["continuum-telemetry-host"]; a["checksum/auth"] != "" {
		t.Errorf("rolloutOnSecretChange=false still renders a checksum: %v", a)
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// `helm upgrade --reuse-values` swaps this chart's defaults for the old release's, so any option added since the install
// is absent from the values the templates see. Removing a key with --set key=null reproduces exactly that.

// The values the telemetry templates fall back on are the defaults of values.yaml, key for key.
func TestTelemetryDefaultsFileMatchesValues(t *testing.T) {
	read := func(p string) map[string]any {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := sigsyaml.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	values := read("continuum-agent/values.yaml")
	want := map[string]any{"telemetry": values["telemetry"], "measurements": values["measurements"],
		"networkPolicy": map[string]any{"telemetryEgress": values["networkPolicy"].(map[string]any)["telemetryEgress"]}}
	got := read("continuum-agent/files/telemetry-defaults.yaml")
	if !reflect.DeepEqual(got, want) {
		wb, _ := sigsyaml.Marshal(want)
		t.Errorf("files/telemetry-defaults.yaml differs from values.yaml (telemetry, measurements, networkPolicy.telemetryEgress); it must be:\n%s", wb)
	}
}

// Whatever block of the telemetry values is missing, the chart renders exactly what it renders with the default there:
// no "nil pointer evaluating interface {}", with telemetry in use or not.
func TestTelemetryRendersWhateverBlockOfTheValuesIsMissing(t *testing.T) {
	b, err := os.ReadFile("continuum-agent/values.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := sigsyaml.Unmarshal(b, &values); err != nil {
		t.Fatal(err)
	}
	var paths []string
	var walk func(prefix string, m map[string]any)
	walk = func(prefix string, m map[string]any) {
		for k, v := range m {
			if strings.Contains(k, ".") || strings.Contains(k, "/") {
				continue // a label key (kubernetes.io/os): not a value block
			}
			p := prefix + "." + k
			paths = append(paths, p)
			if sub, ok := v.(map[string]any); ok {
				walk(p, sub)
			}
		}
	}
	walk("telemetry", values["telemetry"].(map[string]any))
	paths = append(paths, "measurements", "networkPolicy.telemetryEgress", "measurements.enabled", "networkPolicy.telemetryEgress.enabled")
	sort.Strings(paths)

	for _, mode := range []struct {
		name string
		set  []string
	}{
		{"telemetry off", nil},
		{"telemetry on", []string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true", "--set", "telemetry.traces.traces.enabled=true", "--set", "telemetry.energy.metrics.enabled=true"}},
	} {
		base := append(append([]string{}, agentTel...), mode.set...)
		want, err := helmTemplate(t, base...)
		if err != nil {
			t.Fatalf("%s: baseline render failed: %v\n%s", mode.name, err, want)
		}
		// A path the mode itself sets (or a parent of one) is not "missing": removing it changes the intent.
		set := strings.Join(mode.set, " ")
		type result struct {
			p    string
			err  error
			same bool
			got  string
		}
		var mu sync.Mutex
		var bad []result
		sem := make(chan struct{}, 8)
		var wg sync.WaitGroup
		for _, p := range paths {
			if p == "telemetry.export.otlp.endpoint" || p == "telemetry.export.otlp" || p == "telemetry.export" || strings.Contains(set, p+"=") || strings.Contains(set, p+".") {
				continue // removing the destination is a real error with a signal on, and the test's own flag
			}
			wg.Add(1)
			sem <- struct{}{}
			go func(p string) {
				defer wg.Done()
				defer func() { <-sem }()
				got, err := helmTemplate(t, append(append([]string{}, base...), "--set", p+"=null")...)
				if err != nil || got != want {
					mu.Lock()
					bad = append(bad, result{p, err, got == want, got})
					mu.Unlock()
				}
			}(p)
		}
		wg.Wait()
		sort.Slice(bad, func(i, j int) bool { return bad[i].p < bad[j].p })
		for i, r := range bad {
			if i >= 5 {
				t.Errorf("%s: %d paths in all failed", mode.name, len(bad))
				break
			}
			t.Errorf("%s: with %s removed: err=%v, same render as with the default: %v\n%.300s", mode.name, r.p, r.err, r.same, r.got)
		}
		if testing.Short() {
			break
		}
	}
}

// A value a release does set - false and empty included - is never replaced by a default when the rest is filled in.
func TestMissingTelemetryDefaultsDoNotOverrideWhatTheReleaseSets(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.processors.redaction.enabled=false",
		"--set", "telemetry.health.enabled=false", "--set", "telemetry.rolloutOnSecretChange=false", "--set", "telemetry.processors.batch=null")...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	procs := stringsOf(t, sub(t, cfg, "service", "pipelines", "metrics")["processors"])
	for _, p := range procs {
		if p == "redaction" {
			t.Errorf("redaction.enabled=false was replaced by the default true: %v", procs)
		}
	}
	if _, ok := sub(t, cfg, "service")["telemetry"]; ok {
		t.Error("telemetry.health.enabled=false was replaced by the default true")
	}
	if batch := sub(t, cfg, "processors", "batch"); batch["send_batch_size"] != float64(2048) {
		t.Errorf("a removed processors.batch block did not come back with its defaults: %v", batch)
	}
}

// The upgrade command in the README is the one the server prints.
func TestReadmeUpgradesWithResetThenReuseValues(t *testing.T) {
	b, err := os.ReadFile("continuum-agent/README.md")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "helm upgrade") && strings.Contains(line, "--reuse-values") {
			t.Errorf("README.md:%d recommends --reuse-values, which fails once the chart has gained an option since the install: %s", i+1, line)
		}
	}
	if !strings.Contains(string(b), "helm upgrade <release> <chart> -n <namespace> --reset-then-reuse-values") {
		t.Error("README.md has no --reset-then-reuse-values upgrade command")
	}
	if strings.Contains(string(b), "-z <release>-telemetry") {
		t.Error("README.md names a ServiceAccount <release>-telemetry; it is continuum-agent-telemetry whatever the release is called")
	}
}

// ---------------------------------------------------------------------------------------------------------------------
// Images: a default that names a tag the registry does not have is an ImagePullBackOff on every node. This needs the
// network and is opt-in: CONTINUUM_CHECK_IMAGES=1 go test -run DefaultImagesExist ./internal/chart/

func TestDefaultImagesExistOnTheirRegistries(t *testing.T) {
	if os.Getenv("CONTINUUM_CHECK_IMAGES") == "" {
		t.Skip("set CONTINUUM_CHECK_IMAGES=1 (needs the network) to check the default image tags against their registries")
	}
	var v struct {
		Telemetry struct {
			CollectorImage struct{ Repository, Tag string } `json:"collectorImage"`
			Energy         struct {
				Metrics struct {
					KeplerImage struct{ Repository, Tag string } `json:"keplerImage"`
				} `json:"metrics"`
			} `json:"energy"`
			Accelerators struct {
				Metrics struct {
					DcgmImage struct{ Repository, Tag string } `json:"dcgmImage"`
				} `json:"metrics"`
			} `json:"accelerators"`
		} `json:"telemetry"`
	}
	b, _ := os.ReadFile("continuum-agent/values.yaml")
	if err := sigsyaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	type img struct{ repo, tag string }
	for name, i := range map[string]img{
		"collector": {v.Telemetry.CollectorImage.Repository, v.Telemetry.CollectorImage.Tag},
		"kepler":    {v.Telemetry.Energy.Metrics.KeplerImage.Repository, v.Telemetry.Energy.Metrics.KeplerImage.Tag},
		"dcgm":      {v.Telemetry.Accelerators.Metrics.DcgmImage.Repository, v.Telemetry.Accelerators.Metrics.DcgmImage.Tag},
	} {
		host, path, _ := strings.Cut(i.repo, "/")
		if !strings.ContainsAny(host, ".:") { // Docker Hub: otel/opentelemetry-collector-contrib
			host, path = "registry-1.docker.io", i.repo
		}
		if status, err := manifestStatus(host, path, i.tag); err != nil {
			t.Errorf("%s: %s/%s:%s: %v", name, host, path, i.tag, err)
		} else if status != 200 {
			t.Errorf("%s: %s/%s:%s does not exist on its registry (HTTP %d)", name, host, path, i.tag, status)
		}
	}
}

// manifestStatus asks a registry for a tag's manifest, anonymously (a bearer token fetched the way the registry's own
// WWW-Authenticate header says), and returns the HTTP status.
func manifestStatus(host, path, tag string) (int, error) {
	c := &http.Client{Timeout: 30 * time.Second}
	get := func(token string) (*http.Response, error) {
		req, _ := http.NewRequest("GET", "https://"+host+"/v2/"+path+"/manifests/"+tag, nil)
		req.Header.Set("Accept", "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return c.Do(req)
	}
	resp, err := get("")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return resp.StatusCode, nil
	}
	realm, service := "", ""
	for _, part := range strings.Split(strings.TrimPrefix(resp.Header.Get("Www-Authenticate"), "Bearer "), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch k {
		case "realm":
			realm = strings.Trim(v, `"`)
		case "service":
			service = strings.Trim(v, `"`)
		}
	}
	if realm == "" {
		return resp.StatusCode, nil
	}
	tr, err := c.Get(fmt.Sprintf("%s?service=%s&scope=repository:%s:pull", realm, service, path))
	if err != nil {
		return 0, err
	}
	defer tr.Body.Close()
	var tok struct{ Token, AccessToken string }
	if err := json.NewDecoder(tr.Body).Decode(&tok); err != nil {
		return 0, err
	}
	if tok.Token == "" {
		tok.Token = tok.AccessToken
	}
	resp2, err := get(tok.Token)
	if err != nil {
		return 0, err
	}
	defer resp2.Body.Close()
	return resp2.StatusCode, nil
}
