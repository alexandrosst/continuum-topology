package chart

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// Review round 2, agent D: the dcgm-exporter (accelerators) extractor on as many GPU clusters as possible. Each test pins
// one behaviour to what NVIDIA's own chart, the NVIDIA GPU Operator's assets or the platform docs say (cited at the
// template that depends on it).

var (
	// the operator's own exporter, found pod by pod
	accelOperator = []string{"--set", "telemetry.export.otlp.endpoint=x:4317",
		"--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing",
		"--set", "telemetry.accelerators.metrics.existing.pods.labelSelector=app=nvidia-dcgm-exporter"}
)

func dcgmPod(t *testing.T, args ...string) corev1.PodSpec {
	t.Helper()
	ds, ok := render(t, append(append([]string{}, accelOn...), args...)...).daemonsets["continuum-telemetry-dcgm"]
	if !ok {
		t.Fatal("DaemonSet continuum-telemetry-dcgm not rendered")
	}
	return ds.Spec.Template.Spec
}

// dcgm-exporter needs neither a token nor any permission. A ServiceAccount of its own (instead of the namespace's "default")
// keeps the namespace's "default" account out of it.
func TestDcgmHasItsOwnPermissionlessServiceAccount(t *testing.T) {
	r := render(t, accelOn...)
	pod := r.daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec
	if pod.ServiceAccountName != "continuum-agent-dcgm" {
		t.Fatalf("serviceAccountName = %q, want continuum-agent-dcgm (never \"default\", never the collectors' or Kepler's)", pod.ServiceAccountName)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Error("dcgm-exporter must not mount a token: it never talks to the Kubernetes API")
	}
	sa, ok := r.serviceaccounts["continuum-agent-dcgm"]
	if !ok {
		t.Fatal("ServiceAccount continuum-agent-dcgm not rendered")
	}
	if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
		t.Error("the ServiceAccount itself must not auto-mount a token")
	}
	for name, b := range r.clusterrolebindings {
		for _, s := range b.Subjects {
			if s.Name == "continuum-agent-dcgm" {
				t.Errorf("ClusterRoleBinding %s grants something to continuum-agent-dcgm: it needs no RBAC at all", name)
			}
		}
	}
	// and nothing is rendered when it is not bundled
	for name, args := range map[string][]string{"off": nil, "existing": accelOperator} {
		if _, ok := render(t, append([]string{"--set", "telemetry.export.otlp.endpoint=x:4317"}, args...)...).serviceaccounts["continuum-agent-dcgm"]; ok {
			t.Errorf("%s: ServiceAccount continuum-agent-dcgm rendered without a bundled exporter", name)
		}
	}
}

func affinityKeys(t *testing.T, pod corev1.PodSpec) map[string]string {
	t.Helper()
	got := map[string]string{}
	if pod.Affinity == nil || pod.Affinity.NodeAffinity == nil || pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		return got
	}
	for _, term := range pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
		if len(term.MatchExpressions) != 1 {
			t.Errorf("term %+v: each GPU label must be a term of its own (expressions inside one term are ANDed)", term)
			continue
		}
		e := term.MatchExpressions[0]
		got[e.Key] = string(e.Operator) + strings.Join(e.Values, ",")
	}
	return got
}

// A DaemonSet that selects no node has 0 desired pods and says nothing. By default it now goes to a node announcing an NVIDIA
// GPU by any label the common installers leave (GPU Operator, GFD, NFD, GKE), and tolerates every taint (GKE's
// nvidia.com/gpu=present, AKS' sku=gpu, ...).
func TestDcgmDefaultsReachGpuNodesOfEveryInstallerAndTolerateAnyTaint(t *testing.T) {
	pod := dcgmPod(t)
	if len(pod.NodeSelector) != 0 {
		t.Errorf("nodeSelector = %v, want none by default (the old nvidia.com/gpu.present=true alone is set only by the GPU Operator)", pod.NodeSelector)
	}
	want := map[string]string{
		"nvidia.com/gpu.present":                           "In" + "true",
		"feature.node.kubernetes.io/pci-10de.present":      "In" + "true",
		"feature.node.kubernetes.io/pci-0302_10de.present": "In" + "true",
		"feature.node.kubernetes.io/pci-0300_10de.present": "In" + "true",
		"nvidia.com/gpu.count":                             "Exists",
		"cloud.google.com/gke-accelerator":                 "Exists",
	}
	got := affinityKeys(t, pod)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("node affinity = %v, want %v", got, want)
	}
	if len(pod.Tolerations) != 1 || pod.Tolerations[0].Operator != corev1.TolerationOpExists || pod.Tolerations[0].Key != "" {
		t.Errorf("tolerations = %+v, want one that tolerates everything", pod.Tolerations)
	}
}

// Whatever the release sets replaces the built-in list, so a cluster labelled its own way is not widened behind its back, and an
// upgrade that keeps the old default nodeSelector (--reuse-values) keeps exactly that selector.
func TestDcgmNodeSelectorOrAffinityOfTheReleaseReplacesTheBuiltInList(t *testing.T) {
	pod := dcgmPod(t, "--set", "telemetry.accelerators.metrics.nodeSelector.gpu=yes")
	if pod.NodeSelector["gpu"] != "yes" || pod.Affinity != nil {
		t.Errorf("with a nodeSelector: selector %v affinity %+v, want the selector alone", pod.NodeSelector, pod.Affinity)
	}
	pod = dcgmPod(t, "--set", "telemetry.accelerators.metrics.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].key=mine",
		"--set", "telemetry.accelerators.metrics.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[0].matchExpressions[0].operator=Exists")
	if got := affinityKeys(t, pod); len(got) != 1 || got["mine"] != "Exists" {
		t.Errorf("with an affinity of the release: %v, want only {mine: Exists}", got)
	}
}

// dcgm-exporter labels each series with the node name only when NODE_NAME is set (release 3.1.6-3.1.3); without it the label is the
// pod's name. DCGM_EXPORTER_LISTEN keeps the port the Service, the probes and the scrape use as the one it binds.
func TestDcgmGetsItsNodeNameAndListenAddress(t *testing.T) {
	c := dcgmPod(t).Containers[0]
	if src := envSource(c, "NODE_NAME"); src == nil || src.FieldRef == nil || src.FieldRef.FieldPath != "spec.nodeName" {
		t.Errorf("NODE_NAME = %+v, want the pod's spec.nodeName", src)
	}
	if v, ok := env(c, "DCGM_EXPORTER_LISTEN"); !ok || v != ":9400" {
		t.Errorf("DCGM_EXPORTER_LISTEN = %q (set %v), want :9400", v, ok)
	}
	if _, ok := env(c, "DCGM_EXPORTER_KUBERNETES"); ok {
		t.Error("DCGM_EXPORTER_KUBERNETES set without applyScope: the kubelet socket is not mounted then")
	}
}

// NVIDIA's chart runs root + SYS_ADMIN; its own GPU Operator runs the exporter privileged. The wider grant is opt-in, and a
// privileged container may not also say allowPrivilegeEscalation: false (the API refuses the pair).
func TestDcgmPrivilegedIsOptInAndValid(t *testing.T) {
	sc := dcgmPod(t).Containers[0].SecurityContext
	if sc.Privileged != nil && *sc.Privileged {
		t.Fatal("privileged by default")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Add) != 1 || sc.Capabilities.Add[0] != "SYS_ADMIN" {
		t.Errorf("default capabilities = %+v, want exactly SYS_ADMIN added", sc.Capabilities)
	}
	sc = dcgmPod(t, "--set", "telemetry.accelerators.metrics.privileged=true").Containers[0].SecurityContext
	if sc.Privileged == nil || !*sc.Privileged {
		t.Fatal("privileged=true did not make the container privileged")
	}
	if sc.AllowPrivilegeEscalation != nil && !*sc.AllowPrivilegeEscalation {
		t.Error("privileged together with allowPrivilegeEscalation: false is refused by the API server")
	}
	if sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Errorf("runAsUser = %v, want 0 (the image's own user)", sc.RunAsUser)
	}
}

// NVIDIA's chart probes /health for liveness as well as readiness; /health only says the server is up, so the pod restarts when it hangs.
func TestDcgmHasALivenessProbe(t *testing.T) {
	p := dcgmPod(t).Containers[0].LivenessProbe
	if p == nil || p.HTTPGet == nil || p.HTTPGet.Path != "/health" || p.InitialDelaySeconds < 45 {
		t.Fatalf("liveness probe = %+v, want /health after at least NVIDIA's 45 s", p)
	}
}

// GKE keeps the driver outside any container runtime (Google's own manifest mounts /home/kubernetes/bin/nvidia and sets
// LD_LIBRARY_PATH); hostMounts + extraEnv are the generic way to say that, and the reserved variables cannot be overridden.
func TestDcgmExtraEnvAndHostMounts(t *testing.T) {
	pod := dcgmPod(t,
		"--set", "telemetry.accelerators.metrics.hostMounts[0].hostPath=/home/kubernetes/bin/nvidia",
		"--set", "telemetry.accelerators.metrics.hostMounts[0].mountPath=/usr/local/nvidia",
		"--set", "telemetry.accelerators.metrics.extraEnv[0].name=LD_LIBRARY_PATH",
		"--set", "telemetry.accelerators.metrics.extraEnv[0].value=/usr/local/nvidia/lib64",
		"--set", "telemetry.accelerators.metrics.extraEnv[1].name=DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE",
		"--set", "telemetry.accelerators.metrics.extraEnv[1].value=device-name")
	c := pod.Containers[0]
	if v, _ := env(c, "LD_LIBRARY_PATH"); v != "/usr/local/nvidia/lib64" {
		t.Errorf("LD_LIBRARY_PATH = %q", v)
	}
	if v, _ := env(c, "DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE"); v != "device-name" {
		t.Errorf("DCGM_EXPORTER_KUBERNETES_GPU_ID_TYPE = %q", v)
	}
	var mount *corev1.VolumeMount
	for i, m := range c.VolumeMounts {
		if m.MountPath == "/usr/local/nvidia" {
			mount = &c.VolumeMounts[i]
		}
	}
	if mount == nil || !mount.ReadOnly {
		t.Fatalf("host mount missing or writable: %+v", c.VolumeMounts)
	}
	var vol *corev1.Volume
	for i, v := range pod.Volumes {
		if v.Name == mount.Name {
			vol = &pod.Volumes[i]
		}
	}
	if vol == nil || vol.HostPath == nil || vol.HostPath.Path != "/home/kubernetes/bin/nvidia" {
		t.Errorf("volume behind the mount = %+v", vol)
	}
	for name, set := range map[string][]string{
		"NODE_NAME":                 {"telemetry.accelerators.metrics.extraEnv[0].name=NODE_NAME", "telemetry.accelerators.metrics.extraEnv[0].value=x"},
		"DCGM_EXPORTER_LISTEN":      {"telemetry.accelerators.metrics.extraEnv[0].name=DCGM_EXPORTER_LISTEN", "telemetry.accelerators.metrics.extraEnv[0].value=:1"},
		"KUBERNETES without scope":  {"telemetry.accelerators.metrics.extraEnv[0].name=DCGM_EXPORTER_KUBERNETES", "telemetry.accelerators.metrics.extraEnv[0].value=true"},
		"relative hostPath":         {"telemetry.accelerators.metrics.hostMounts[0].hostPath=x", "telemetry.accelerators.metrics.hostMounts[0].mountPath=/x"},
		"relative mountPath":        {"telemetry.accelerators.metrics.hostMounts[0].hostPath=/x", "telemetry.accelerators.metrics.hostMounts[0].mountPath=x"},
		"unknown key in a mount":    {"telemetry.accelerators.metrics.hostMounts[0].hostPath=/x", "telemetry.accelerators.metrics.hostMounts[0].mountPath=/x", "telemetry.accelerators.metrics.hostMounts[0].readOnly=false"},
		"env entry without a name":  {"telemetry.accelerators.metrics.extraEnv[0].value=x"},
		"labelSelector with quotes": {"telemetry.accelerators.metrics.existing.pods.labelSelector=a\"b"},
	} {
		args := append([]string{}, accelOn...)
		for _, s := range set {
			args = append(args, "--set", s)
		}
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%s: accepted:\n%.300s", name, out)
		}
	}
}

// source=existing finds the exporter either by one address or by its pods' label, exactly one.
func TestExistingAcceleratorsNeedsExactlyOneWayToFindTheExporter(t *testing.T) {
	base := []string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing"}
	out, err := helmTemplate(t, base...)
	if err == nil || !strings.Contains(out, "existing.pods.labelSelector") || !strings.Contains(out, "prometheusEndpoint") {
		t.Errorf("neither given: err=%v, want a message naming both ways\n%.400s", err, out)
	}
	if out, err := helmTemplate(t, append(base, "--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=a:9400", "--set", "telemetry.accelerators.metrics.existing.pods.labelSelector=app=x")...); err == nil {
		t.Errorf("both given: accepted\n%.300s", out)
	}
	for _, port := range []string{"0", "70000"} {
		if _, err := helmTemplate(t, append(accelOperator, "--set", "telemetry.accelerators.metrics.existing.pods.port="+port)...); err == nil {
			t.Errorf("port %s accepted", port)
		}
	}
}

// The GPU Operator's exporters are a DaemonSet behind one ClusterIP Service: a scrape of the Service sees one node. Pod
// discovery by label gives each node its own target; the address is the pod IP and the configured port whatever ports the pod
// declares; pods that are not running are skipped; and the whole thing is one namespace or all.
func TestExistingAcceleratorsAreScrapedPodByPod(t *testing.T) {
	r := render(t, accelOperator...)
	if len(r.daemonsets) != 0 && r.daemonsets["continuum-telemetry-dcgm"].Name != "" {
		t.Error("source=existing deployed a second exporter")
	}
	job := scrapeConfigs(t, r)["dcgm-exporter"]
	if job == nil {
		t.Fatalf("no dcgm-exporter job: %v", keys(scrapeConfigs(t, r)))
	}
	sd := job["kubernetes_sd_configs"].([]any)[0].(map[string]any)
	if _, has := sd["namespaces"]; has {
		t.Errorf("namespaces = %v, want the search over all namespaces when none is named", sd["namespaces"])
	}
	if sel := sd["selectors"].([]any)[0].(map[string]any); sel["role"] != "pod" || sel["label"] != "app=nvidia-dcgm-exporter" {
		t.Errorf("selector = %v", sel)
	}
	var phase, v4, node bool
	for _, rc := range job["relabel_configs"].([]any) {
		m := rc.(map[string]any)
		src := m["source_labels"].([]any)[0]
		phase = phase || (src == "__meta_kubernetes_pod_phase" && m["action"] == "keep" && m["regex"] == "Running")
		v4 = v4 || (src == "__meta_kubernetes_pod_ip" && m["target_label"] == "__address__" && m["replacement"] == "$${1}:9400" && m["regex"] == "([0-9.]+)")
		node = node || (src == "__meta_kubernetes_pod_node_name" && m["target_label"] == "node")
	}
	if !phase || !v4 || !node {
		t.Errorf("relabel_configs: running-only %v, address %v, node label %v", phase, v4, node)
	}
	// a namespace and a port of its own
	sd = scrapeConfigs(t, render(t, append(accelOperator, "--set", "telemetry.accelerators.metrics.existing.pods.namespace=nvidia-gpu-operator", "--set", "telemetry.accelerators.metrics.existing.pods.port=9500")...))["dcgm-exporter"]["kubernetes_sd_configs"].([]any)[0].(map[string]any)
	if got := sd["namespaces"].(map[string]any)["names"].([]any); len(got) != 1 || got[0] != "nvidia-gpu-operator" {
		t.Errorf("namespaces = %v", got)
	}
}

// The exporter's own pod identity (and its DaemonSet's name) must not land on the GPU records of an existing exporter either:
// telemetry.scope.infra would read the operator's namespace as the namespace of every GPU.
func TestExistingExporterPodsGetTheSameIdentityCleanup(t *testing.T) {
	for name, args := range map[string][]string{"bundled": accelOn, "existing pods": accelOperator} {
		cfg := otelConfig(t, render(t, args...).configmaps["continuum-telemetry-cluster-config"].Data)
		tr, ok := cfg["processors"].(map[string]any)["transform/dcgm_node"]
		if !ok {
			t.Fatalf("%s: no transform/dcgm_node", name)
		}
		b, _ := json.Marshal(tr)
		for _, a := range []string{"k8s.namespace.name", "k8s.pod.name", "k8s.pod.uid", "k8s.container.name", "k8s.daemonset.name", "k8s.deployment.name", "k8s.replicaset.name", "k8s.statefulset.name", "k8s.job.name", "k8s.cronjob.name"} {
			if !strings.Contains(string(b), `delete_key(attributes, \"`+a+`\")`) {
				t.Errorf("%s: transform/dcgm_node does not delete %s", name, a)
			}
		}
		pipe := cfg["service"].(map[string]any)["pipelines"].(map[string]any)["metrics/infra"].(map[string]any)["processors"]
		if !strings.Contains(fmt.Sprint(pipe), "transform/dcgm_node") {
			t.Errorf("%s: transform/dcgm_node is not in the pipeline: %v", name, pipe)
		}
	}
	// an address is not a pod: nothing to clean, and the job keeps its own name
	cfg := otelConfig(t, render(t, "--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.accelerators.metrics.enabled=true",
		"--set", "telemetry.accelerators.metrics.source=existing", "--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=gpu:9400").configmaps["continuum-telemetry-cluster-config"].Data)
	if _, has := cfg["processors"].(map[string]any)["transform/dcgm_node"]; has {
		t.Error("transform/dcgm_node rendered for a single-address existing exporter")
	}
}

// applyScope follows the exporter however it was found: the filter is gated on the job's own service.name.
func TestApplyScopeFollowsTheScrapeJobOfEverySource(t *testing.T) {
	for name, c := range map[string]struct {
		args []string
		job  string
	}{
		"bundled":       {accelOn, "dcgm-exporter"},
		"existing pods": {accelOperator, "dcgm-exporter"},
		"existing addr": {[]string{"--set", "telemetry.export.otlp.endpoint=x:4317", "--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=existing", "--set", "telemetry.accelerators.metrics.existing.prometheusEndpoint=gpu:9400"}, "accelerators-existing"},
	} {
		cfg := otelConfig(t, render(t, append(append([]string{}, c.args...), "--set", "telemetry.scope.namespaces={a,b}", "--set", "telemetry.accelerators.metrics.applyScope=true")...).configmaps["continuum-telemetry-cluster-config"].Data)
		f, ok := cfg["processors"].(map[string]any)["filter/scope_accelerators"]
		if !ok {
			t.Errorf("%s: no filter/scope_accelerators with applyScope", name)
			continue
		}
		b, _ := json.Marshal(f)
		for _, want := range []string{`datapoint.attributes[\"namespace\"]`, `service.name\"] == \"` + c.job + `\"`, `metric.name != \"up\"`} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: filter lacks %s: %s", name, want, b)
			}
		}
	}
}

// With applyScope on, an existing exporter other than the bundled one gets no DCGM_EXPORTER_KUBERNETES from this chart (it
// deploys nothing), and the bundled one's pod gets exactly the mapping and the socket.
func TestApplyScopeWithBundledExporterMountsOnlyTheSocket(t *testing.T) {
	pod := dcgmPod(t, "--set", "telemetry.accelerators.metrics.applyScope=true")
	if v, _ := env(pod.Containers[0], "DCGM_EXPORTER_KUBERNETES"); v != "true" {
		t.Errorf("DCGM_EXPORTER_KUBERNETES = %q", v)
	}
	for _, v := range pod.Volumes {
		if v.HostPath != nil && v.HostPath.Path != "/var/lib/kubelet/pod-resources" {
			t.Errorf("unexpected hostPath %s", v.HostPath.Path)
		}
	}
	if _, ok := env(pod.Containers[0], "DCGM_EXPORTER_KUBERNETES_ENABLE_POD_LABELS"); ok {
		t.Error("pod labels need API access this pod does not have")
	}
}

// dcgm-exporter serves and reads local sockets only. It is not in the shared telemetry egress policy (which would open DNS, the API
// server and the export destination to it) but has one of its own that allows nothing; and the scrape of an existing exporter is
// let through to its namespace (or any) on its port.
func TestDcgmEgressAllowsNothingAndExistingScrapeIsAllowed(t *testing.T) {
	egress := []string{"--set", "networkPolicy.telemetryEgress.enabled=true",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=10.0.0.0/8", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports={4317}"}
	r := render(t, append(append([]string{}, accelOn...), egress...)...)
	p, ok := r.policies["continuum-telemetry-dcgm-egress"]
	if !ok {
		t.Fatal("no continuum-telemetry-dcgm-egress policy")
	}
	if len(p.Spec.Egress) != 0 || len(p.Spec.PolicyTypes) != 1 || p.Spec.PolicyTypes[0] != "Egress" {
		t.Errorf("dcgm policy = %+v, want Egress with no rule at all", p.Spec)
	}
	if p.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "continuum-telemetry-dcgm" {
		t.Errorf("dcgm policy selects %v", p.Spec.PodSelector)
	}
	for _, e := range r.policies["continuum-telemetry-egress"].Spec.PodSelector.MatchExpressions {
		for _, v := range e.Values {
			if v == "continuum-telemetry-dcgm" {
				t.Error("the shared egress policy still selects the dcgm pods")
			}
		}
	}
	if _, ok := render(t, append(append([]string{}, accelOperator...), egress...)...).policies["continuum-telemetry-dcgm-egress"]; ok {
		t.Error("dcgm egress policy rendered without a bundled exporter")
	}
	for name, c := range map[string]struct{ extra, ns string }{
		"named namespace": {"telemetry.accelerators.metrics.existing.pods.namespace=gpu-operator", "gpu-operator"},
		"any namespace":   {"telemetry.accelerators.metrics.existing.pods.port=9400", ""},
	} {
		q := render(t, append(append(append([]string{}, accelOperator...), egress...), "--set", c.extra)...)
		sp, ok := q.policies["continuum-telemetry-scrape-egress"]
		if !ok || len(sp.Spec.Egress) != 1 {
			t.Fatalf("%s: scrape policy = %+v", name, sp.Spec)
		}
		rule := sp.Spec.Egress[0]
		if len(rule.Ports) != 1 || rule.Ports[0].Port.IntValue() != 9400 || len(rule.To) != 1 || rule.To[0].NamespaceSelector == nil {
			t.Fatalf("%s: rule = %+v", name, rule)
		}
		got := rule.To[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
		if got != c.ns {
			t.Errorf("%s: namespace selector = %v, want %q", name, rule.To[0].NamespaceSelector, c.ns)
		}
	}
}

// Everything that differs, loaded by the real collector.
func TestDcgmConfigurationsLoadInTheRealCollector(t *testing.T) {
	bin := collectorBinary(t)
	for name, args := range map[string][]string{
		"existing pods, namespace, scope": append(append([]string{}, accelOperator...), "--set", "telemetry.accelerators.metrics.existing.pods.namespace=gpu-operator",
			"--set", "telemetry.scope.namespaces={a}", "--set", "telemetry.accelerators.metrics.applyScope=true"),
		"existing pods, any namespace": accelOperator,
		"bundled with scope":           append(append([]string{}, accelOn...), "--set", "telemetry.scope.namespaces={a}", "--set", "telemetry.accelerators.metrics.applyScope=true"),
	} {
		cm := render(t, args...).configmaps["continuum-telemetry-cluster-config"]
		f := filepath.Join(t.TempDir(), "cfg.yaml")
		if err := os.WriteFile(f, []byte(cm.Data["otel-collector-config.yaml"]), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "validate", "--config", f)
		cmd.Env = append(os.Environ(), "POD_IP=127.0.0.1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: the collector refuses the rendered config: %v\n%s", name, err, out)
		}
	}
}

func collectorBinary(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("OTELCOL_CONTRIB")
	if bin == "" {
		bin, _ = exec.LookPath("otelcol-contrib")
	}
	if bin == "" {
		t.Skip("otelcol-contrib is not available (OTELCOL_CONTRIB or PATH)")
	}
	return bin
}

// dcgm-exporter 4.8.x exposition as it labels a node with three GPUs: two held by pods (namespace/pod/container labels, the names
// docs.nvidia.com/datacenter/dcgm/latest/reference/dcgm-exporter-metrics.html lists for --kubernetes) and one free.
const dcgmFixture = `# HELP DCGM_FI_DEV_GPU_UTIL GPU utilization (in %).
# TYPE DCGM_FI_DEV_GPU_UTIL gauge
DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-aaaa",pci_bus_id="00000000:3B:00.0",device="nvidia0",modelName="NVIDIA A100",hostname="gpu-node-1",container="trainer",namespace="team-a",pod="train-0"} 93
DCGM_FI_DEV_GPU_UTIL{gpu="1",UUID="GPU-bbbb",pci_bus_id="00000000:5E:00.0",device="nvidia1",modelName="NVIDIA A100",hostname="gpu-node-1",container="infer",namespace="team-b",pod="serve-7"} 12
DCGM_FI_DEV_GPU_UTIL{gpu="2",UUID="GPU-cccc",pci_bus_id="00000000:86:00.0",device="nvidia2",modelName="NVIDIA A100",hostname="gpu-node-1"} 0
`

// Scrape, transform and scope filter on a fixture, in the real collector. Kubernetes service discovery is replaced by a static
// target carrying the same __meta_kubernetes_* labels the discovery would (so every relabel_configs rule of the rendered job
// runs), k8sattributes is dropped (no API server here) and the exporter is a file. Proves: GPU data of an in-scope namespace is
// kept with its namespace/pod/container labels; data of another namespace and of a free GPU is dropped; up and scrape_* stay;
// the exporter pod's own identity and DaemonSet name are not on the resource; the node is.
func TestDcgmScrapeTransformAndScopeFilterInTheRealCollector(t *testing.T) {
	bin := collectorBinary(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fmt.Fprint(w, dcgmFixture)
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host, port, _ := strings.Cut(hostport, ":")

	for name, c := range map[string]struct{ args []string }{
		"bundled":       {append(append([]string{}, accelOn...), "--set", "telemetry.accelerators.metrics.applyScope=true")},
		"existing pods": {append(append([]string{}, accelOperator...), "--set", "telemetry.accelerators.metrics.applyScope=true", "--set", "telemetry.accelerators.metrics.existing.pods.port="+port)},
	} {
		args := append(c.args, "--set", "telemetry.scope.namespaces={team-a}")
		cm := render(t, args...).configmaps["continuum-telemetry-cluster-config"]
		var cfg map[string]any
		if err := sigsyaml.Unmarshal([]byte(cm.Data["otel-collector-config.yaml"]), &cfg); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		out := filepath.Join(dir, "out.json")
		rcv := cfg["receivers"].(map[string]any)["prometheus/infra"].(map[string]any)["config"].(map[string]any)
		for _, sc := range rcv["scrape_configs"].([]any) {
			m := sc.(map[string]any)
			delete(m, "kubernetes_sd_configs")
			m["scrape_interval"], m["scrape_timeout"] = "1s", "500ms"
			m["static_configs"] = []any{map[string]any{"targets": []any{hostport}, "labels": map[string]any{
				"__meta_kubernetes_pod_node_name": "gpu-node-1", "__meta_kubernetes_pod_name": "exporter-abcde", "__meta_kubernetes_namespace": "exporter-ns",
				"__meta_kubernetes_pod_uid": "11111111-2222-3333-4444-555555555555", "__meta_kubernetes_pod_container_name": "dcgm-exporter",
				"__meta_kubernetes_pod_controller_kind": "DaemonSet", "__meta_kubernetes_pod_controller_name": "exporter",
				"__meta_kubernetes_pod_phase": "Running", "__meta_kubernetes_pod_ip": host,
				"__meta_kubernetes_pod_label_app_kubernetes_io_name": "continuum-telemetry-dcgm", "__meta_kubernetes_pod_label_app_kubernetes_io_instance": "ct"}}}
		}
		cfg["exporters"] = map[string]any{"file": map[string]any{"path": out}}
		delete(cfg["processors"].(map[string]any), "k8sattributes")
		for _, p := range cfg["service"].(map[string]any)["pipelines"].(map[string]any) {
			pm := p.(map[string]any)
			pm["exporters"] = []any{"file"}
			var keep []any
			for _, x := range pm["processors"].([]any) {
				if s := x.(string); s != "k8sattributes" && !strings.HasPrefix(s, "resource/") {
					keep = append(keep, x)
				}
			}
			pm["processors"] = keep
		}
		cfg["extensions"] = map[string]any{}
		cfg["service"].(map[string]any)["extensions"] = []any{}
		cfg["service"].(map[string]any)["telemetry"] = map[string]any{"metrics": map[string]any{"level": "none"}}
		b, _ := sigsyaml.Marshal(cfg)
		cf := filepath.Join(dir, "cfg.yaml")
		if err := os.WriteFile(cf, b, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "--config="+cf)
		cmd.Env = append(os.Environ(), "POD_IP=127.0.0.1", "NODE_NAME=n")
		logf, _ := os.Create(filepath.Join(dir, "log"))
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(20 * time.Second)
		var recs []gpuRecord
		for time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			if recs = readGpuRecords(t, out); len(recs) > 0 && hits.Load() >= 2 {
				break
			}
		}
		_ = cmd.Process.Signal(os.Interrupt)
		_ = cmd.Wait()
		if len(recs) == 0 {
			logs, _ := os.ReadFile(filepath.Join(dir, "log"))
			t.Fatalf("%s: nothing exported; collector log:\n%.2000s", name, logs)
		}
		var util, up int
		for _, r := range recs {
			switch r.metric {
			case "DCGM_FI_DEV_GPU_UTIL":
				util++
				if r.dp["namespace"] != "team-a" || r.dp["pod"] != "train-0" || r.dp["container"] != "trainer" || r.dp["gpu"] != "0" {
					t.Errorf("%s: kept a data point that is not team-a's GPU 0: %v", name, r.dp)
				}
				if r.dp["exported_namespace"] != nil || r.dp["exported_pod"] != nil {
					t.Errorf("%s: a scraped label was renamed exported_*: %v", name, r.dp)
				}
				if r.dp["node"] != "gpu-node-1" {
					t.Errorf("%s: node label = %v", name, r.dp["node"])
				}
			case "up":
				up++
			}
			if r.res["k8s.node.name"] != "gpu-node-1" {
				t.Errorf("%s: %s has no k8s.node.name on the resource: %v", name, r.metric, r.res)
			}
			for _, a := range []string{"k8s.pod.name", "k8s.namespace.name", "k8s.pod.uid", "k8s.container.name", "k8s.daemonset.name"} {
				if _, has := r.res[a]; has {
					t.Errorf("%s: %s still carries the exporter's own %s: %v", name, r.metric, a, r.res)
				}
			}
			if r.res["service.name"] != "dcgm-exporter" {
				t.Errorf("%s: service.name = %v, want dcgm-exporter", name, r.res["service.name"])
			}
		}
		if util == 0 || up == 0 {
			t.Errorf("%s: kept %d GPU data points and %d up (want >0 of each: in-scope data and the scrape's own up)", name, util, up)
		}
	}
}

type gpuRecord struct {
	metric string
	res    map[string]any
	dp     map[string]any
}

func readGpuRecords(t *testing.T, path string) []gpuRecord {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	attrs := func(v any) map[string]any {
		out := map[string]any{}
		list, _ := v.([]any)
		for _, a := range list {
			m := a.(map[string]any)
			for _, val := range m["value"].(map[string]any) {
				out[m["key"].(string)] = val
			}
		}
		return out
	}
	var recs []gpuRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var doc struct {
			ResourceMetrics []struct {
				Resource struct {
					Attributes []any `json:"attributes"`
				} `json:"resource"`
				ScopeMetrics []struct {
					Metrics []map[string]any `json:"metrics"`
				} `json:"scopeMetrics"`
			} `json:"resourceMetrics"`
		}
		if json.Unmarshal(sc.Bytes(), &doc) != nil {
			continue // a half-written last line
		}
		for _, rm := range doc.ResourceMetrics {
			res := attrs(rm.Resource.Attributes)
			for _, sm := range rm.ScopeMetrics {
				for _, m := range sm.Metrics {
					g, _ := m["gauge"].(map[string]any)
					if g == nil {
						continue
					}
					for _, dp := range g["dataPoints"].([]any) {
						recs = append(recs, gpuRecord{m["name"].(string), res, attrs(dp.(map[string]any)["attributes"])})
					}
				}
			}
		}
	}
	return recs
}
