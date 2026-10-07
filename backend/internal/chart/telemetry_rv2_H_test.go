package chart

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// The host collector as it meets the node it lands on: how it reaches the kubelet, what it reads of the node, which
// node it has to fit. Each test pins a behaviour that was checked by
// running the real collector (0.160.0, the version the chart pins) or against the platform's own source/documentation,
// cited next to it.

func kubeletStats(t *testing.T, extra ...string) map[string]any {
	t.Helper()
	return sub(t, hostConfig(t, extra...), "receivers", "kubelet_stats")
}

func volumeNamed(ps corev1.PodSpec, name string) *corev1.Volume {
	for i := range ps.Volumes {
		if ps.Volumes[i].Name == name {
			return &ps.Volumes[i]
		}
	}
	return nil
}

// The kubelet endpoint is a valid URL for each way of addressing the node (its IP, or its name).
func TestKubeletEndpointIsAValidURLForEachAddressMode(t *testing.T) {
	const v4 = "10.0.0.7"
	for _, tc := range []struct {
		name, address, nodeIP, host string
		wantNodeIPEnv               bool
	}{
		{"ipv4 (default)", "", v4, v4, true},
		{"node name", "nodeName", "", "n1", false},
	} {
		set := []string{"--set", "telemetry.nodeRuntime.metrics.enabled=true"}
		if tc.address != "" {
			set = append(set, "--set", "telemetry.kubelet.address="+tc.address)
		}
		r := render(t, withTel(set...)...)
		ep, _ := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "kubelet_stats")["endpoint"].(string)
		ep = strings.NewReplacer("${env:NODE_IP}", tc.nodeIP, "${env:NODE_NAME}", "n1").Replace(ep)
		u, err := url.Parse(ep)
		if err != nil {
			t.Errorf("%s: endpoint %q is not a URL: %v", tc.name, ep, err)
			continue
		}
		if u.Hostname() != tc.host || u.Port() != "10250" || u.Scheme != "https" {
			t.Errorf("%s: endpoint %q parses to host %q port %q, want %q 10250", tc.name, ep, u.Hostname(), u.Port(), tc.host)
		}
		env := containerEnv(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_IP")
		if (env != nil) != tc.wantNodeIPEnv {
			t.Errorf("%s: NODE_IP set = %v, want %v", tc.name, env != nil, tc.wantNodeIPEnv)
		}
	}
}

// With viaAPIServer the receiver (auth_type kubeConfig, no kubeconfig file: it falls back to the pod's in-cluster
// credentials - verified by running it against a fake API server, which saw GET /api/v1/nodes/n1/proxy/stats/summary
// with the bearer token) asks the API server to reach the kubelet. The API server authorizes that as nodes/proxy, which
// Kubernetes documents as not read-only (https://kubernetes.io/docs/reference/access-authn-authz/kubelet-authn-authz/,
// "get permission on nodes/proxy ... authorizes executing commands in any container on the node"), so that rule exists only
// when asked for, replaces nodes/stats, and the pod no longer needs the kubelet's port or address.
func TestKubeletViaAPIServerAsksTheAPIServerAndPaysForItInRBAC(t *testing.T) {
	lock := []string{"--set", "networkPolicy.telemetryEgress.enabled=true", "--set", "networkPolicy.telemetryEgress.allowedEgress[0].cidr=203.0.113.7/32",
		"--set", "networkPolicy.telemetryEgress.allowedEgress[0].ports[0]=4317", "--set", "networkPolicy.telemetryEgress.apiServerCIDRs[0]=10.1.1.1/32"}
	args := withTel(append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubelet.viaAPIServer=true"}, lock...)...)
	r := render(t, args...)
	ks := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "kubelet_stats")
	if ks["auth_type"] != "kubeConfig" || ks["endpoint"] != "${env:NODE_NAME}" {
		t.Errorf("kubelet_stats = %v, want auth_type kubeConfig and the bare node name as endpoint", ks)
	}
	if v, ok := ks["insecure_skip_verify"]; ok {
		t.Errorf("insecure_skip_verify = %v: with kubeConfig it would switch off the check of the API server", v)
	}
	if containerEnv(r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Containers[0], "NODE_IP") != nil {
		t.Error("NODE_IP is set although the kubelet is never dialled")
	}
	var proxy, stats bool
	for _, rule := range r.clusterroles["continuum-agent-telemetry"].Rules {
		for _, res := range rule.Resources {
			proxy = proxy || res == "nodes/proxy"
			stats = stats || res == "nodes/stats"
		}
	}
	if !proxy || stats {
		t.Errorf("RBAC: nodes/proxy = %v, nodes/stats = %v; viaAPIServer needs nodes/proxy and nothing of nodes/stats", proxy, stats)
	}
	for _, e := range r.policies["continuum-telemetry-egress"].Spec.Egress {
		for _, p := range e.Ports {
			if p.Port != nil && p.Port.String() == "10250" {
				t.Errorf("an egress rule for the kubelet port although the pod never dials a kubelet: %+v", e)
			}
		}
	}
	// ... and the default is unchanged: direct, nodes/stats only, the kubelet port open.
	d := render(t, withTel(append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true"}, lock...)...)...)
	var open bool
	for _, e := range d.policies["continuum-telemetry-egress"].Spec.Egress {
		for _, p := range e.Ports {
			open = open || p.Port != nil && p.Port.String() == "10250"
		}
	}
	if !open {
		t.Error("the default no longer allows the kubelet port in the egress policy")
	}
	for _, rule := range d.clusterroles["continuum-agent-telemetry"].Rules {
		for _, res := range rule.Resources {
			if res == "nodes/proxy" {
				t.Errorf("nodes/proxy granted without telemetry.kubelet.viaAPIServer: %+v", rule)
			}
		}
	}
}

// A ca_file REPLACES the pod's cluster CA for the kubelet connection (the receiver picks one or the other:
// internal/kubelet/client.go), so it has to be a file the pod really has: a ConfigMap or Secret key mounted read-only.
func TestKubeletCABundleIsMountedWhereTheReceiverReadsIt(t *testing.T) {
	for _, tc := range []struct {
		name, set string
		secret    bool
	}{{"configMap", "telemetry.kubelet.ca.configMap=kubelet-serving-ca", false}, {"secret", "telemetry.kubelet.ca.secret=kubelet-serving-ca", true}} {
		args := withTel("--set", "telemetry.nodeRuntime.metrics.enabled=true", "--set", tc.set, "--set", "telemetry.kubelet.ca.key=ca-bundle.crt")
		r := render(t, args...)
		ks := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "kubelet_stats")
		if ks["ca_file"] != "/etc/continuum/kubelet-ca/ca.crt" {
			t.Errorf("%s: ca_file = %v", tc.name, ks["ca_file"])
		}
		ps := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
		v := volumeNamed(ps, "kubelet-ca")
		if v == nil {
			t.Fatalf("%s: no kubelet-ca volume", tc.name)
		}
		var items []corev1.KeyToPath
		var name string
		if tc.secret {
			if v.Secret == nil {
				t.Fatalf("%s: not a Secret volume: %+v", tc.name, v)
			}
			items, name = v.Secret.Items, v.Secret.SecretName
		} else {
			if v.ConfigMap == nil {
				t.Fatalf("%s: not a ConfigMap volume: %+v", tc.name, v)
			}
			items, name = v.ConfigMap.Items, v.ConfigMap.Name
		}
		if name != "kubelet-serving-ca" || len(items) != 1 || items[0].Key != "ca-bundle.crt" || items[0].Path != "ca.crt" {
			t.Errorf("%s: volume = %s %+v, want kubelet-serving-ca with ca-bundle.crt mapped to ca.crt", tc.name, name, items)
		}
		var mounted bool
		for _, m := range ps.Containers[0].VolumeMounts {
			mounted = mounted || m.Name == "kubelet-ca" && m.MountPath == "/etc/continuum/kubelet-ca" && m.ReadOnly
		}
		if !mounted {
			t.Errorf("%s: the bundle is not mounted read-only at /etc/continuum/kubelet-ca", tc.name)
		}
		// nodeRuntime with a CA bundle still mounts no HOST path: it stays in a restricted namespace.
		for _, vol := range ps.Volumes {
			if vol.HostPath != nil {
				t.Errorf("%s: hostPath volume %s", tc.name, vol.Name)
			}
		}
	}
	if ks := kubeletStats(t, withTel("--set", "telemetry.nodeRuntime.metrics.enabled=true")...); ks["ca_file"] != nil {
		t.Errorf("ca_file = %v without a bundle: the pod's cluster CA must stay in use", ks["ca_file"])
	}
}

// Combinations that would be silently ignored are refused at render time with the setting named.
func TestKubeletSettingsThatContradictEachOtherAreRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  []string
		want string
	}{
		{"configMap and secret", []string{"telemetry.kubelet.ca.configMap=a", "telemetry.kubelet.ca.secret=b"}, "either configMap or secret"},
		{"viaAPIServer with skip", []string{"telemetry.kubelet.viaAPIServer=true", "telemetry.kubelet.insecureSkipVerify=true"}, "viaAPIServer"},
		{"viaAPIServer with ca", []string{"telemetry.kubelet.viaAPIServer=true", "telemetry.kubelet.ca.configMap=a"}, "viaAPIServer"},
		{"viaAPIServer with address", []string{"telemetry.kubelet.viaAPIServer=true", "telemetry.kubelet.address=nodeName"}, "viaAPIServer"},
		{"relative pod log dir", []string{"telemetry.systemLogs.logs.podLogsDir=var/log/pods"}, "podLogsDir"},
		{"extra path with ..", []string{"telemetry.systemLogs.logs.extraHostPaths[0]=/var/lib/../etc"}, "extraHostPaths"},
		{"root as extra path", []string{"telemetry.systemLogs.logs.extraHostPaths[0]=/"}, "extraHostPaths"},
		{"relative journalctl", []string{"telemetry.systemLogs.logs.journalctlPath=journalctl"}, "journalctlPath"},
	} {
		args := []string{"--set", "telemetry.nodeRuntime.metrics.enabled=true"}
		for _, s := range tc.set {
			args = append(args, "--set", s)
		}
		out, err := helmTemplate(t, withTel(args...)...)
		if err == nil || !strings.Contains(out, tc.want) {
			t.Errorf("%s: want a refusal naming %q, got err=%v\n%s", tc.name, tc.want, err, out)
		}
	}
}

// Container logs, as measured with the real collector over 200 pods' files (idle, 4,000 and 19,000 lines/s, destination
// down): polling every 200ms made 1000 idle files cost ~10% of a core (1s: ~1%), and a refused batch was dropped - 85% of
// the lines at 19,000/s with the destination down, at ten times the CPU - unless the receiver retries. filelog has used no
// inotify since fsnotify left pkg/stanza's dependencies, so the poll interval is the only knob.
func TestContainerLogsPollGentlyAndPauseInsteadOfDroppingWhenTheDownstreamRefuses(t *testing.T) {
	fl := sub(t, hostConfig(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...), "receivers", "filelog/containers")
	if fl["poll_interval"] != "1s" {
		t.Errorf("poll_interval = %v, want 1s", fl["poll_interval"])
	}
	retry := sub(t, fl, "retry_on_failure")
	if retry["enabled"] != true || retry["max_elapsed_time"] != "0s" {
		t.Errorf("retry_on_failure = %v: reading must pause (enabled) and never give up (max_elapsed_time 0s), or refused lines are lost", retry)
	}
}

// Where the logs are: kubelet's podLogsDir, and what its symlinks point to. With Docker through cri-dockerd every file
// under /var/log/pods is a symlink into /var/lib/docker/containers (kubernetes design proposal kubelet-cri-logging:
// "creating symbolic links for kubelet to access"); a pod that does not mount the target reads nothing.
func TestContainerLogDirectoryAndSymlinkTargetsAreMountedAndRead(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.podLogsDir=/data/kubelet-pods/",
		"--set", "telemetry.systemLogs.logs.extraHostPaths[0]=/var/lib/docker/containers", "--set", "telemetry.scope.namespaces[0]=shop")...)
	fl := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "filelog/containers")
	if got := stringsOf(t, fl["include"]); len(got) != 1 || got[0] != "/data/kubelet-pods/shop_*/*/*.log" {
		t.Errorf("include = %v, want the namespace glob under podLogsDir", got)
	}
	if got := stringsOf(t, fl["exclude"]); !strings.HasPrefix(got[0], "/data/kubelet-pods/") {
		t.Errorf("exclude = %v: this release's own pods are looked up under podLogsDir too", got)
	}
	ps := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	for name, path := range map[string]string{"var-log-pods": "/data/kubelet-pods", "log-extra-0": "/var/lib/docker/containers"} {
		v := volumeNamed(ps, name)
		if v == nil || v.HostPath == nil || v.HostPath.Path != path {
			t.Errorf("volume %s = %+v, want hostPath %s", name, v, path)
			continue
		}
		if !hasMount(ps.Containers[0], name, path) {
			t.Errorf("%s is not mounted at the same path (a symlink resolves by path)", name)
		}
		for _, m := range ps.Containers[0].VolumeMounts {
			if m.Name == name && !m.ReadOnly {
				t.Errorf("%s is mounted read-write", name)
			}
		}
	}
	if v := volumeNamed(ps, "log-extra-0"); v != nil && (v.HostPath.Type == nil || *v.HostPath.Type != corev1.HostPathDirectory) {
		t.Errorf("an extra path must be type Directory so a node without it fails visibly: %+v", v.HostPath)
	}
	// Default: nothing extra.
	d := hostPod(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...)
	if volumeNamed(d, "log-extra-0") != nil || !hasMount(d.Containers[0], "var-log-pods", "/var/log/pods") {
		t.Errorf("default volumes = %+v", d.Volumes)
	}
}

// journaling: host runs the NODE's journalctl chrooted into the node's root filesystem (the journald receiver's documented
// "host's journalctl" route). Reproduced with the real collector: chroot needs CAP_SYS_CHROOT, and without it the receiver
// fails to start (`fork/exec /usr/bin/journalctl: operation not permitted`), which takes the whole collector down.
func TestJournalingHostChrootsIntoTheNodeAndOnlyThenAddsTheCapability(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.journaling=host")...)
	j := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data), "receivers", "journald")
	if j["root_path"] != "/hostfs" || j["journalctl_path"] != "/usr/bin/journalctl" {
		t.Errorf("journald = %v", j)
	}
	if _, ok := j["directory"]; ok {
		t.Errorf("journald sets a directory (%v): journalctl must look in the node's own locations, volatile and persistent", j["directory"])
	}
	ps := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	c := ps.Containers[0].SecurityContext
	if c == nil || c.Capabilities == nil || len(c.Capabilities.Add) != 1 || c.Capabilities.Add[0] != "SYS_CHROOT" || len(c.Capabilities.Drop) != 1 || c.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capabilities = %+v, want drop ALL and add only SYS_CHROOT", c.Capabilities)
	}
	if v := volumeNamed(ps, "host-fs"); v == nil || v.HostPath == nil || v.HostPath.Path != "/" {
		t.Errorf("the node's root filesystem is not mounted although journalctl runs in a chroot of it: %+v", ps.Volumes)
	}
	for _, m := range ps.Containers[0].VolumeMounts {
		if m.Name == "host-fs" && (!m.ReadOnly || m.MountPath != "/hostfs") {
			t.Errorf("host-fs mount = %+v, want read-only at /hostfs", m)
		}
		if m.Name == "journal" {
			t.Errorf("the journal directory is mounted separately in host mode: %+v", m)
		}
	}
	// The other modes add nothing.
	for _, mode := range []string{"none", "journald"} {
		ps := hostPod(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.journaling="+mode)...)
		if c := ps.Containers[0].SecurityContext; len(c.Capabilities.Add) != 0 {
			t.Errorf("journaling=%s adds capabilities %v", mode, c.Capabilities.Add)
		}
		if volumeNamed(ps, "host-fs") != nil {
			t.Errorf("journaling=%s mounts the node's root filesystem", mode)
		}
		if got := volumeNamed(ps, "journal") != nil; got != (mode == "journald") {
			t.Errorf("journaling=%s: journal volume present = %v", mode, got)
		}
	}
}

// The root mount is plain by default - a runtime refuses HostToContainer on a node whose root mount is private ("path / is
// mounted on / but it is not a shared or slave mount", seen on a micro-VM node), and then the collector never starts - and
// follows the node's later mounts (a data disk attached after the pod started) only when asked to.
func TestHostRootMountPropagationIsOptIn(t *testing.T) {
	mount := func(args ...string) corev1.VolumeMount {
		ps := hostPod(t, withTel(append([]string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.resourceUsage.metrics.hostFilesystem=true"}, args...)...)...)
		for _, m := range ps.Containers[0].VolumeMounts {
			if m.Name == "host-fs" {
				return m
			}
		}
		t.Fatal("no host-fs mount")
		return corev1.VolumeMount{}
	}
	if m := mount(); m.MountPropagation != nil || !m.ReadOnly {
		t.Errorf("default host-fs mount = %+v, want read-only with no propagation", m)
	}
	if m := mount("--set", "telemetry.hostCollector.rootMountPropagation=HostToContainer"); m.MountPropagation == nil || *m.MountPropagation != corev1.MountPropagationHostToContainer || !m.ReadOnly {
		t.Errorf("host-fs mount = %+v, want read-only HostToContainer", m)
	}
	if out, err := helmTemplate(t, withTel("--set", "telemetry.hostCollector.rootMountPropagation=Bidirectional")...); err == nil {
		t.Errorf("Bidirectional accepted (it needs privileged):\n%s", out)
	}
}

// Sizing, measured with the real collector (0.160.0) over 200 pods' log files and a fake kubelet of 110 pods:
// anonymous memory idle ~50 MiB, ~95 MiB at 19,000 lines/s, ~195 MiB peak with the destination down and the same load
// arriving - which does not fit the shared 256Mi default once the Go runtime's own overhead and the 60% soft limit of
// memory_limiter are counted. GOMEMLIMIT follows the limit (80%), and memory_limiter's percentages are of the cgroup limit
// (go.opentelemetry.io/collector internal/memorylimiter/iruntime reads cgroup v1 and v2).
func TestHostCollectorIsSizedForANodeAtTheDefaultPodLimit(t *testing.T) {
	ps := hostPod(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	res := ps.Containers[0].Resources
	if got := res.Limits.Memory().String(); got != "512Mi" {
		t.Errorf("memory limit = %s, want 512Mi", got)
	}
	if got := res.Requests.Memory().String(); got != "128Mi" {
		t.Errorf("memory request = %s, want 128Mi", got)
	}
	if _, ok := res.Limits[corev1.ResourceCPU]; ok {
		t.Error("a CPU limit throttles a log reader exactly when the node is busy")
	}
	gml := containerEnv(ps.Containers[0], "GOMEMLIMIT")
	if gml == nil || gml.Value != fmt.Sprint(512*1024*1024*8/10+1) { // 429496729.6 rendered as a rounded byte count
		t.Errorf("GOMEMLIMIT = %+v, want 80%% of 512Mi", gml)
	}
	// The host collector's own block, when set, is used as given (telemetry.hostCollector.resources); the sizing above is the
	// shared telemetry.resources it falls back to.
	o := hostPod(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.hostCollector.resources.limits.memory=1Gi")...)
	if got := o.Containers[0].Resources.Limits.Memory().String(); got != "1Gi" {
		t.Errorf("override: memory limit = %s", got)
	}
}

// What the pod needs, per signal, and nothing more: nodeRuntime alone (with a CA bundle or through the API server) mounts
// nothing from the node and runs non-root; resourceUsage mounts the root read-only; systemLogs is root with nothing added.
func TestHostCollectorPrivilegeFollowsTheSignalsEnabled(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		hostPath int
		root     bool
		addCaps  int
	}{
		{"nodeRuntime", []string{"telemetry.nodeRuntime.metrics.enabled=true"}, 0, false, 0},
		{"nodeRuntime via API server", []string{"telemetry.nodeRuntime.metrics.enabled=true", "telemetry.kubelet.viaAPIServer=true"}, 0, false, 0},
		{"nodeRuntime with CA", []string{"telemetry.nodeRuntime.metrics.enabled=true", "telemetry.kubelet.ca.secret=x"}, 0, false, 0},
		{"resourceUsage", []string{"telemetry.resourceUsage.metrics.enabled=true"}, 1, false, 0},
		{"systemLogs", []string{"telemetry.systemLogs.logs.enabled=true"}, 1, true, 0},
		{"systemLogs + docker links", []string{"telemetry.systemLogs.logs.enabled=true", "telemetry.systemLogs.logs.extraHostPaths[0]=/var/lib/docker/containers"}, 2, true, 0},
		{"systemLogs + host journal", []string{"telemetry.systemLogs.logs.enabled=true", "telemetry.systemLogs.logs.journaling=host"}, 2, true, 1},
	} {
		var args []string
		for _, a := range tc.args {
			args = append(args, "--set", a)
		}
		ps := hostPod(t, withTel(args...)...)
		hp := 0
		for _, v := range ps.Volumes {
			if v.HostPath != nil {
				hp++
			}
		}
		if hp != tc.hostPath {
			t.Errorf("%s: %d hostPath volumes, want %d", tc.name, hp, tc.hostPath)
		}
		if isRoot := ps.SecurityContext.RunAsUser != nil && *ps.SecurityContext.RunAsUser == 0; isRoot != tc.root {
			t.Errorf("%s: root = %v, want %v", tc.name, isRoot, tc.root)
		}
		sc := ps.Containers[0].SecurityContext
		if sc.Privileged != nil && *sc.Privileged || len(sc.Capabilities.Add) != tc.addCaps {
			t.Errorf("%s: privileged/capabilities = %+v", tc.name, sc)
		}
	}
}

// Every new setting reaches a configuration the real collector accepts. The pod-only things (the CA bundle's path, the
// in-cluster credentials of viaAPIServer) are given to the validation the way the pod has them.
func TestHostConfigVariantsValidateWithTheRealCollector(t *testing.T) {
	bin := os.Getenv("CONTINUUM_OTELCOL")
	if bin == "" {
		t.Skip("set CONTINUUM_OTELCOL to the otelcol-contrib binary (the version telemetry.collectorImage.tag names) to run this")
	}
	all := []string{"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.nodeRuntime.metrics.enabled=true", "--set", "telemetry.systemLogs.logs.enabled=true"}
	for name, extra := range map[string][]string{
		"default":     nil,
		"node name":   {"--set", "telemetry.kubelet.address=nodeName"},
		"via API":     {"--set", "telemetry.kubelet.viaAPIServer=true"},
		"CA bundle":   {"--set", "telemetry.kubelet.ca.configMap=c"},
		"host jrnl":   {"--set", "telemetry.systemLogs.logs.journaling=host", "--set", "telemetry.systemLogs.logs.journalctlPath=/bin/journalctl"},
		"pod log dir": {"--set", "telemetry.systemLogs.logs.podLogsDir=/data/pods", "--set", "telemetry.scope.namespaces[0]=shop"},
	} {
		r := render(t, withTel(append(append([]string{}, all...), extra...)...)...)
		root, ca := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(ca, "ca.crt"), testCAPEM(t), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := strings.NewReplacer("/hostfs", root, "/etc/continuum/kubelet-ca", ca).Replace(r.configmaps["continuum-telemetry-host-config"].Data["otel-collector-config.yaml"])
		f := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(f, []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin, "validate", "--config="+f)
		cmd.Env = append(os.Environ(), "POD_IP=10.0.0.5", "NODE_NAME=n1", "NODE_IP=10.0.0.1", "KUBERNETES_SERVICE_HOST=10.96.0.1", "KUBERNETES_SERVICE_PORT=443")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s: does not validate: %v\n%s", name, err, out)
		}
	}
}

// A self-signed CA certificate in PEM, for a bundle the collector actually parses.
func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test kubelet CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// Device-backed mounts that belong to a container runtime or to the kubelet's pods, on every runtime: hostmetrics reports
// only filesystems with a device of their own (include_virtual_filesystems is false: running it here listed ext4 and
// squashfs mounts and none of the tmpfs, proc, cgroup or fuse ones), which leaves the ones a CSI driver or a runtime's
// own disk mounts under its state directory. Those directories are root-only (Docker 0710, containers/storage 0700), so a
// collector that is not root would log a permission error per mount per interval.
func TestHostmetricsFilesystemExclusionsCoverEveryRuntimesStateAndRelocatedKubelets(t *testing.T) {
	cfg := hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.resourceUsage.metrics.hostFilesystem=true")...)
	fs := sub(t, cfg, "receivers", "hostmetrics", "scrapers", "filesystem", "exclude_mount_points")
	var res []*regexp.Regexp
	for _, p := range stringsOf(t, fs["mount_points"]) {
		re, err := regexp.Compile(p)
		if err != nil {
			t.Fatal(err)
		}
		res = append(res, re)
	}
	excluded := func(mp string) bool {
		for _, re := range res {
			if re.MatchString(mp) {
				return true
			}
		}
		return false
	}
	for _, mp := range []string{
		"/var/lib/containers/storage/overlay/ab12/merged",                                              // CRI-O, Podman
		"/run/containers/storage/overlay-containers/ab12/userdata/shm",                                 // CRI-O
		"/var/lib/rancher/rke2/agent/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/4/fs", // RKE2
		"/var/snap/microk8s/common/var/lib/kubelet/pods/0c1f/volumes/kubernetes.io~csi/pvc-1/mount",    // MicroK8s relocates the kubelet
		"/var/snap/microk8s/common/run/containerd/io.containerd.runtime.v2.task/k8s.io/ab12/rootfs",    // ... and containerd
		"/data/kubelet/plugins/kubernetes.io/csi/ebs.csi.aws.com/ab12/globalmount",
	} {
		if !excluded(mp) {
			t.Errorf("%s is not excluded: %v", mp, stringsOf(t, fs["mount_points"]))
		}
	}
	for _, mp := range []string{"/", "/boot/efi", "/var/lib/longhorn", "/data/containerized", "/mnt/kubelet-backup", "/var/lib/containers-backup"} {
		if excluded(mp) {
			t.Errorf("%s is a data mount and must stay in", mp)
		}
	}
}

// The host collector runs as the fixed non-root user (podSecurity.runAsUser) unless it reads container logs, which the runtime
// writes as root; no SELinux type is ever written.
func TestHostCollectorUserFollowsPodSecurity(t *testing.T) {
	node := []string{"--set", "telemetry.nodeRuntime.metrics.enabled=true"}
	ps := hostPod(t, withTel(node...)...)
	if sc := ps.SecurityContext; sc.RunAsUser == nil || *sc.RunAsUser != 65532 || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("nodeRuntime only: %+v, want non-root 65532", sc)
	}
	ps = hostPod(t, withTel(append([]string{"--set", "podSecurity.runAsUser=10001"}, node...)...)...)
	if sc := ps.SecurityContext; sc.RunAsUser == nil || *sc.RunAsUser != 10001 {
		t.Errorf("runAsUser=10001: %+v", sc)
	}
	ps = hostPod(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...)
	if sc := ps.SecurityContext; sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Errorf("systemLogs: %+v, want root", sc)
	}
	for _, c := range ps.Containers {
		if c.SecurityContext != nil && c.SecurityContext.SELinuxOptions != nil {
			t.Errorf("an SELinux type is written: %+v", c.SecurityContext.SELinuxOptions)
		}
	}
}
