package chart

import (
	"sort"
	"testing"
)

// Least privilege for the host collector: resourceUsage alone reads CPU and memory, which come from the node's /proc, so
// that is the only host path it mounts. The whole root is mounted only for the node-wide filesystem scraper
// (resourceUsage.metrics.hostFilesystem) or for journaling: host, which chroots into it.
func TestResourceUsageMountsOnlyProcUnlessTheNodeFilesystemIsAsked(t *testing.T) {
	hostPaths := func(args ...string) []string {
		ps := hostPod(t, withTel(args...)...)
		var out []string
		for _, v := range ps.Volumes {
			if v.HostPath != nil {
				out = append(out, v.HostPath.Path)
			}
		}
		sort.Strings(out)
		return out
	}
	mountsOf := func(args ...string) map[string]string {
		ps := hostPod(t, withTel(args...)...)
		m := map[string]string{}
		for _, vm := range ps.Containers[0].VolumeMounts {
			m[vm.Name] = vm.MountPath
		}
		return m
	}
	usage := []string{"--set", "telemetry.resourceUsage.metrics.enabled=true"}
	if got := hostPaths(usage...); len(got) != 1 || got[0] != "/proc" {
		t.Errorf("resourceUsage hostPaths = %v, want [/proc]", got)
	}
	if m := mountsOf(usage...); m["host-proc"] != "/hostfs/proc" || m["host-fs"] != "" {
		t.Errorf("mounts = %v, want host-proc at /hostfs/proc and no host-fs", m)
	}
	// the filesystem scraper is only configured when its mount exists
	scrapers := func(args ...string) map[string]any {
		cfg := hostConfig(t, withTel(args...)...)
		return cfg["receivers"].(map[string]any)["hostmetrics"].(map[string]any)["scrapers"].(map[string]any)
	}
	if sc := scrapers(usage...); sc["cpu"] == nil || sc["memory"] == nil || sc["filesystem"] != nil {
		t.Errorf("default scrapers = %v, want cpu and memory only", sc)
	}
	wide := append(append([]string{}, usage...), "--set", "telemetry.resourceUsage.metrics.hostFilesystem=true")
	if got := hostPaths(wide...); len(got) != 1 || got[0] != "/" {
		t.Errorf("hostFilesystem hostPaths = %v, want [/] (proc comes through it)", got)
	}
	if m := mountsOf(wide...); m["host-fs"] != "/hostfs" || m["host-proc"] != "" {
		t.Errorf("mounts = %v, want only host-fs at /hostfs", m)
	}
	if sc := scrapers(wide...); sc["filesystem"] == nil {
		t.Errorf("hostFilesystem=true scrapers = %v, want filesystem", sc)
	}
	// journaling: host needs the root for the node's journalctl, and then proc comes through it as well
	journal := append(append([]string{}, usage...), "--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.systemLogs.logs.journaling=host")
	if m := mountsOf(journal...); m["host-fs"] != "/hostfs" || m["host-proc"] != "" {
		t.Errorf("journaling host mounts = %v", m)
	}
	// nodeRuntime alone and system logs alone: nothing of the node's /proc, nothing of its root
	for name, args := range map[string][]string{
		"nodeRuntime": {"--set", "telemetry.nodeRuntime.metrics.enabled=true"},
		"systemLogs":  {"--set", "telemetry.systemLogs.logs.enabled=true"},
	} {
		for _, p := range hostPaths(args...) {
			if p == "/" || p == "/proc" {
				t.Errorf("%s mounts %s", name, p)
			}
		}
	}
}

// A user who scoped the telemetry to some namespaces must not receive energy or GPU data of the pods of the others, without
// having to know a second switch: applyScope is "auto", which follows the scope whenever the intent defines one (namespaces,
// exclude or workloads) and does nothing when it does not, so an unscoped install gains no filter and no extra mount.
func TestEnergyAndGPUFollowTheScopeByDefault(t *testing.T) {
	both := append(append([]string{}, energyOn...), "--set", "telemetry.accelerators.metrics.enabled=true", "--set", "telemetry.accelerators.metrics.source=bundle-dcgm")
	filters := func(args ...string) (energy, gpu bool, socket bool) {
		r := render(t, append(append([]string{}, both...), args...)...)
		procs := sub(t, otelConfig(t, r.configmaps["continuum-telemetry-cluster-config"].Data), "processors")
		_, energy = procs["filter/scope_energy"]
		_, gpu = procs["filter/scope_accelerators"]
		for _, v := range r.daemonsets["continuum-telemetry-dcgm"].Spec.Template.Spec.Volumes {
			if v.HostPath != nil && v.HostPath.Path == "/var/lib/kubelet/pod-resources" {
				socket = true
			}
		}
		return
	}
	for name, tc := range map[string]struct {
		args                      []string
		wantEnergy, wantGPU, sock bool
	}{
		"no scope":          {nil, false, false, false},
		"namespaces":        {[]string{"--set", "telemetry.scope.namespaces={shop}"}, true, true, true},
		"exclude only":      {[]string{"--set", "telemetry.scope.exclude={kube-system}"}, true, true, true},
		"workloads only":    {[]string{"--set", "telemetry.scope.workloads[0].namespace=shop", "--set", "telemetry.scope.workloads[0].names={web}"}, true, true, true},
		"scope, forced off": {[]string{"--set", "telemetry.scope.namespaces={shop}", "--set", "telemetry.energy.metrics.applyScope=false", "--set", "telemetry.accelerators.metrics.applyScope=false"}, false, false, false},
		"energy off only":   {[]string{"--set", "telemetry.scope.namespaces={shop}", "--set", "telemetry.energy.metrics.applyScope=false"}, false, true, true},
		"no scope, forced":  {[]string{"--set", "telemetry.accelerators.metrics.applyScope=true"}, false, false, true},
	} {
		e, g, s := filters(tc.args...)
		if e != tc.wantEnergy || g != tc.wantGPU || s != tc.sock {
			t.Errorf("%s: energy filter %v gpu filter %v pod-resources socket %v, want %v %v %v", name, e, g, s, tc.wantEnergy, tc.wantGPU, tc.sock)
		}
	}
	// the GPU-to-pod mapping that gives GPU data a namespace is on exactly when the socket is mounted
	pod := dcgmPod(t, "--set", "telemetry.scope.namespaces={shop}")
	var k8s bool
	for _, e := range pod.Containers[0].Env {
		if e.Name == "DCGM_EXPORTER_KUBERNETES" && e.Value == "true" {
			k8s = true
		}
	}
	if !k8s {
		t.Error("a scoped install must turn on dcgm-exporter's pod mapping, or every GPU series has no namespace and the allow-list drops them all")
	}
	if out, err := helmTemplate(t, withTel("--set", "telemetry.energy.metrics.applyScope=sometimes")...); err == nil {
		t.Errorf("applyScope=sometimes accepted:\n%s", out)
	}
}
