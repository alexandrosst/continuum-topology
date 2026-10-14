package chart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// owping, owampd (as a sender) and iperf3 (client and server) each make a temp file under TMPDIR, or /tmp, for every test. The
// link containers run with a read-only root filesystem and the prober starts the tools with a cleared environment, so unless /tmp
// is a volume every test fails while the pod stays Ready. No test with fake executables can see that, so this one reads the
// rendered pod specs: any container of the link pods with a read-only root must have a writable emptyDir at /tmp (or at the
// directory its TMPDIR names).
func TestEveryReadOnlyLinkContainerHasAWritableTemporaryDirectory(t *testing.T) {
	for name, args := range map[string][]string{
		"probe":                    probeArgs(),
		"throughput tests":         capArgs(),
		"throughput tests, authed": capArgs("telemetry.clusterLinks.probe.capacity.auth.existingSecret=k"),
	} {
		r := render(t, args...)
		pods := map[string]corev1.PodSpec{
			"prober":    r.deployments["continuum-link-prober"].Spec.Template.Spec,
			"responder": r.daemonsets["continuum-link-responder"].Spec.Template.Spec,
		}
		for pod, ps := range pods {
			if len(ps.Containers) == 0 {
				t.Fatalf("%s: no %s containers", name, pod)
			}
			for _, c := range ps.Containers {
				if c.SecurityContext == nil || c.SecurityContext.ReadOnlyRootFilesystem == nil || !*c.SecurityContext.ReadOnlyRootFilesystem {
					continue
				}
				dir := "/tmp"
				for _, e := range c.Env {
					if e.Name == "TMPDIR" {
						dir = e.Value
					}
				}
				if !hasWritableEmptyDir(ps, c, dir) {
					t.Errorf("%s: %s container %q has a read-only root filesystem and no writable emptyDir at %s (mounts %+v): owping and iperf3 cannot create their temp files",
						name, pod, c.Name, dir, c.VolumeMounts)
				}
			}
		}
	}
}

func hasWritableEmptyDir(ps corev1.PodSpec, c corev1.Container, dir string) bool {
	for _, m := range c.VolumeMounts {
		if m.MountPath != dir || m.ReadOnly {
			continue
		}
		for _, v := range ps.Volumes {
			if v.Name == m.Name && v.EmptyDir != nil {
				return true
			}
		}
	}
	return false
}

// The scratch volume is small, so a runaway cannot fill the node, and the pods keep their read-only roots.
func TestTheTemporaryDirectoryIsASmallScratchVolume(t *testing.T) {
	r := render(t, capArgs()...)
	for pod, ps := range map[string]corev1.PodSpec{
		"prober":    r.deployments["continuum-link-prober"].Spec.Template.Spec,
		"responder": r.daemonsets["continuum-link-responder"].Spec.Template.Spec,
	} {
		found := false
		for _, v := range ps.Volumes {
			if v.Name == "tmp" {
				found = true
				if v.EmptyDir == nil || v.EmptyDir.SizeLimit == nil || v.EmptyDir.SizeLimit.Value() > 64<<20 {
					t.Errorf("%s: tmp volume = %+v, want an emptyDir with a size limit of at most 64Mi", pod, v)
				}
			}
		}
		if !found {
			t.Errorf("%s: no tmp volume", pod)
		}
	}
}

func TestTheThroughputParallelismTheProberAcceptsIsTheOneTheSchemaAccepts(t *testing.T) {
	for _, n := range []string{"0", "2", "32"} {
		if out, err := helmTemplate(t, capArgs("telemetry.clusterLinks.probe.capacity.parallel="+n)...); err != nil {
			t.Errorf("parallel=%s refused: %v\n%s", n, err, out)
		}
	}
	// 1 is the value the prober refuses at start-up (a "multi-stream" test of one stream), which would crash-loop the pod.
	for _, n := range []string{"1", "33", "-1"} {
		if out, err := helmTemplate(t, capArgs("telemetry.clusterLinks.probe.capacity.parallel="+n)...); err == nil {
			t.Errorf("parallel=%s accepted\n%s", n, out)
		}
	}
}

// An open iperf3 port is allowed (a firewall may be what restricts it) but the notes say so. `helm template` does not print
// NOTES.txt, so (as telemetry_rv2_K_test.go does) the notes go through a ConfigMap.
func TestTheNotesWarnWhenTheThroughputServerIsOpen(t *testing.T) {
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
		args := append(append([]string{"template", "ct", dir, "--show-only", "templates/notes.yaml"}, baseSet...), probeArgs(extra...)...)
		out, err := exec.Command(h, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("helm template of the notes: %v\n%s", err, out)
		}
		return string(out)
	}
	const warn = "without authentication"
	if got := notes("telemetry.clusterLinks.probe.capacity.enabled=true"); !strings.Contains(got, warn) {
		t.Errorf("no warning for an open iperf3 server:\n%s", got)
	}
	if got := notes("telemetry.clusterLinks.probe.capacity.enabled=true", "telemetry.clusterLinks.probe.capacity.auth.existingSecret=k"); strings.Contains(got, warn) {
		t.Errorf("warning although the server authenticates:\n%s", got)
	}
	if got := notes(); strings.Contains(got, warn) {
		t.Errorf("warning with the throughput tests off:\n%s", got)
	}
}
