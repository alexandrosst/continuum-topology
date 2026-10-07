package chart

import (
	"regexp"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The host collector as it behaves on a real node, not as its YAML reads. Each test here pins something that was
// checked by running the real collector (v0.160.0, the version the chart pins) over a real or fabricated host.

func hostPod(t *testing.T, extra ...string) corev1.PodSpec {
	t.Helper()
	return render(t, withTel(extra...)...).daemonsets["continuum-telemetry-host"].Spec.Template.Spec
}

func hasMount(c corev1.Container, name, path string) bool {
	for _, m := range c.VolumeMounts {
		if m.Name == name && m.MountPath == path {
			return true
		}
	}
	return false
}

// The container runtime creates every container log file as root, mode 0640 (containerd's openLogFile) or 0600
// (conmon, for CRI-O); a collector running as 65532 gets "permission denied" on every one of them, ships no log and
// still looks healthy. fsGroup does not apply to a hostPath volume. So the pod that reads them is root - with every
// capability dropped, since the owner needs none - and only while logs are being read: a pod with no container logs
// to read keeps the non-root user (and so still fits a restricted namespace when it mounts no hostPath at all).
func TestHostCollectorIsRootOnlyWhenItReadsContainerLogs(t *testing.T) {
	logs := hostPod(t, "--set", "telemetry.systemLogs.logs.enabled=true")
	sc := logs.SecurityContext
	if sc == nil || sc.RunAsUser == nil || *sc.RunAsUser != 0 {
		t.Fatalf("with systemLogs on the host collector must run as uid 0 to read root-owned log files, got %+v", sc)
	}
	if sc.RunAsNonRoot != nil && *sc.RunAsNonRoot {
		t.Errorf("runAsNonRoot: true contradicts runAsUser: 0")
	}
	c := logs.Containers[0].SecurityContext
	if c == nil || c.Privileged != nil && *c.Privileged || c.AllowPrivilegeEscalation == nil || *c.AllowPrivilegeEscalation ||
		c.ReadOnlyRootFilesystem == nil || !*c.ReadOnlyRootFilesystem || c.Capabilities == nil || len(c.Capabilities.Add) != 0 ||
		len(c.Capabilities.Drop) != 1 || c.Capabilities.Drop[0] != "ALL" {
		t.Errorf("root must come with nothing else: no privileged, no escalation, read-only root, all capabilities dropped, got %+v", c)
	}
	if sc.SeccompProfile == nil || sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("seccomp profile = %+v, want RuntimeDefault", sc.SeccompProfile)
	}

	for name, set := range map[string][]string{
		"resourceUsage only": {"--set", "telemetry.resourceUsage.metrics.enabled=true"},
		"nodeRuntime only":   {"--set", "telemetry.nodeRuntime.metrics.enabled=true"},
	} {
		sc := hostPod(t, set...).SecurityContext
		if sc == nil || sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot || sc.RunAsUser == nil || *sc.RunAsUser != 65532 {
			t.Errorf("%s: nothing here needs root, the pod must stay non-root 65532, got %+v", name, sc)
		}
	}
	// nodeRuntime alone mounts no host path at all, which is what lets it into a namespace enforcing "restricted".
	if vols := hostPod(t, "--set", "telemetry.nodeRuntime.metrics.enabled=true").Volumes; len(vols) != 1 || vols[0].Name != "config" {
		t.Errorf("nodeRuntime alone should mount only its config, got %+v", vols)
	}
}

// hostmetrics' filesystem scraper, given root_path, reads /hostfs/proc/1/mountinfo, which lists mount points as the
// host sees them: /var/lib/kubelet/pods/..., never /hostfs/var/lib/kubelet/pods/... (verified by running it: a bind
// mount at /var/lib/kubelet/pods/u1/volumes/a was reported with exactly that mountpoint, and the chart's old
// "/hostfs/var/lib/kubelet/.*" pattern left it in). Filters are applied to that string, so a pattern that starts with
// /hostfs matches nothing, the kubelet's per-pod mounts are scraped, and a non-root collector logs a permission
// error for each of them at every interval.
func TestHostmetricsFilesystemExclusionsMatchMountPointsAsTheHostSeesThem(t *testing.T) {
	cfg := hostConfig(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.resourceUsage.metrics.hostFilesystem=true")...)
	fs := sub(t, cfg, "receivers", "hostmetrics", "scrapers", "filesystem", "exclude_mount_points")
	if fs["match_type"] != "regexp" {
		t.Fatalf("match_type = %v", fs["match_type"])
	}
	pats := stringsOf(t, fs["mount_points"])
	var res []*regexp.Regexp
	for _, p := range pats {
		re, err := regexp.Compile(p)
		if err != nil {
			t.Fatalf("mount point pattern %q: %v", p, err)
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
		"/var/lib/kubelet/pods/0c1f/volumes/kubernetes.io~csi/pvc-1/mount",
		"/var/lib/kubelet/pods/0c1f/volume-subpaths/cfg/app/0",
		"/var/lib/kubelet/plugins/kubernetes.io/csi/ebs.csi.aws.com/ab12/globalmount",
		"/run/containerd/io.containerd.runtime.v2.task/k8s.io/ab12/rootfs",
		"/run/k3s/containerd/io.containerd.runtime.v2.task/k8s.io/ab12/rootfs",
		"/var/lib/containerd/io.containerd.grpc.v1.cri/sandboxes/ab12/shm",
		"/var/lib/rancher/k3s/agent/containerd/io.containerd.snapshotter.v1.overlayfs/snapshots/9/fs",
		"/var/lib/docker/overlay2/ab12/merged",
		"/dev/shm", "/proc/sys/fs/binfmt_misc", "/sys/fs/cgroup",
		// what the scraper reads when it cannot read pid 1's mountinfo and falls back to its own, prefix included
		"/hostfs/var/lib/kubelet/pods/0c1f/volumes/kubernetes.io~csi/pvc-1/mount", "/hostfs/dev/shm",
	} {
		if !excluded(mp) {
			t.Errorf("%s is not excluded: patterns %v", mp, pats)
		}
	}
	for _, mp := range []string{"/", "/boot", "/boot/efi", "/var", "/var/lib/longhorn", "/var/lib/kubelets", "/mnt/disks/ssd0", "/data", "/opt/dev/x", "/home/sys"} {
		if excluded(mp) {
			t.Errorf("%s is a real data mount the node's owner wants and must stay in: patterns %v", mp, pats)
		}
	}
}

// The network scraper reads /proc/net/dev (through /hostfs/proc, which is only a path: "net" is a link to
// self/net, the reading process's own network namespace). This pod has its own network namespace, so it reports the
// pod's eth0 and lo as the node's interfaces. Verified by running hostmetrics under `unshare -n`: system.network.io
// came out with device=lo only. The node's real network totals are in kubelet_stats' "node" group.
func TestHostmetricsHasNoNetworkScraperThatWouldReadThePodsOwnInterfaces(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.resourceUsage.metrics.hostFilesystem=true")...)
	if pod := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec; pod.HostNetwork {
		t.Skip("hostNetwork: the pod's interfaces are the node's")
	}
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	scrapers := sub(t, cfg, "receivers", "hostmetrics", "scrapers")
	if _, ok := scrapers["network"]; ok {
		t.Errorf("hostmetrics' network scraper reports this pod's interfaces, not the node's: %v", scrapers)
	}
	for _, want := range []string{"cpu", "memory", "filesystem"} {
		if _, ok := scrapers[want]; !ok {
			t.Errorf("hostmetrics lost its %s scraper: %v", want, scrapers)
		}
	}
	if groups := stringsOf(t, sub(t, cfg, "receivers", "kubelet_stats")["metric_groups"]); indexOf(groups, "node") < 0 {
		t.Errorf("kubelet_stats' node group is where the node's network totals come from, got %v", groups)
	}
}

// Without a storage extension filelog keeps its read positions in memory: a collector restarted by an OOM kill or a
// rollout reads nothing of what was written while it was down (start_at: end). Run twice over the same files with a
// restart in between: without storage the lines written during the downtime were lost, with file_storage they all
// arrived exactly once.
func TestHostContainerLogsKeepTheirReadPositionAcrossARestart(t *testing.T) {
	r := render(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true")...)
	cfg := otelConfig(t, r.configmaps["continuum-telemetry-host-config"].Data)
	fl := sub(t, cfg, "receivers", "filelog/containers")
	if fl["storage"] != "file_storage/logs" {
		t.Fatalf("filelog/containers storage = %v, want file_storage/logs", fl["storage"])
	}
	ext := sub(t, cfg, "extensions", "file_storage/logs")
	dir, _ := ext["directory"].(string)
	if indexOf(stringsOf(t, sub(t, cfg, "service")["extensions"]), "file_storage/logs") < 0 {
		t.Errorf("file_storage/logs is defined but not in service.extensions")
	}
	// The root filesystem is read-only: the directory has to be a writable volume.
	pod := r.daemonsets["continuum-telemetry-host"].Spec.Template.Spec
	if !hasMount(pod.Containers[0], "log-checkpoints", dir) {
		t.Fatalf("nothing writable is mounted at %q: %+v", dir, pod.Containers[0].VolumeMounts)
	}
	var found bool
	for _, v := range pod.Volumes {
		if v.Name == "log-checkpoints" {
			found = true
			if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil {
				t.Errorf("the checkpoint volume must be an emptyDir with a size limit, got %+v", v.VolumeSource)
			}
		}
	}
	if !found {
		t.Errorf("no log-checkpoints volume")
	}

	// Off with the logs: no extension, no volume, no mount.
	off := render(t, withTel("--set", "telemetry.resourceUsage.metrics.enabled=true")...)
	offCfg := otelConfig(t, off.configmaps["continuum-telemetry-host-config"].Data)
	if exts, ok := offCfg["extensions"].(map[string]any); ok {
		if _, has := exts["file_storage/logs"]; has {
			t.Errorf("a log checkpoint store without logs")
		}
	}
	for _, v := range off.daemonsets["continuum-telemetry-host"].Spec.Template.Spec.Volumes {
		if v.Name == "log-checkpoints" {
			t.Errorf("a log checkpoint volume without logs")
		}
	}
	// And it composes with the persistent export queue's own file_storage.
	both := hostConfig(t, withTel("--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.export.queue.persistent.enabled=true")...)
	exts := stringsOf(t, sub(t, both, "service")["extensions"])
	if indexOf(exts, "file_storage/logs") < 0 || indexOf(exts, "file_storage/queue") < 0 {
		t.Errorf("service.extensions = %v, want both file_storage/logs and file_storage/queue", exts)
	}
}

// Each signal gets the kubelet data it names. nodeRuntime is "pod and volume metrics, node stats stay"; the
// per-container series are resourceUsage's (and the highest-cardinality group), so enabling only nodeRuntime must
// not start shipping them.
func TestKubeletStatsMetricGroupsFollowTheSignalsThatAskForThem(t *testing.T) {
	for name, tc := range map[string]struct {
		set  []string
		want string
	}{
		"resourceUsage":             {[]string{"telemetry.resourceUsage.metrics.enabled=true"}, "container,pod,node"},
		"nodeRuntime":               {[]string{"telemetry.nodeRuntime.metrics.enabled=true"}, "pod,node,volume"},
		"resourceUsage+nodeRuntime": {[]string{"telemetry.resourceUsage.metrics.enabled=true", "telemetry.nodeRuntime.metrics.enabled=true"}, "container,pod,node,volume"},
	} {
		var args []string
		for _, s := range tc.set {
			args = append(args, "--set", s)
		}
		cfg := hostConfig(t, withTel(args...)...)
		got := strings.Join(stringsOf(t, sub(t, cfg, "receivers", "kubelet_stats")["metric_groups"]), ",")
		if got != tc.want {
			t.Errorf("%s: metric_groups = %s, want %s", name, got, tc.want)
		}
	}
}
