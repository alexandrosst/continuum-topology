package chart

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func serverPod(t *testing.T, r serverRender) corev1.PodSpec {
	t.Helper()
	for _, d := range r.deployments {
		if d.Labels["app.kubernetes.io/name"] == "ikhnos-fusion" || d.Labels["app.kubernetes.io/component"] != "server" {
			continue
		}
		return d.Spec.Template.Spec
	}
	t.Fatal("no server Deployment")
	return corev1.PodSpec{}
}

// With sealing switched off on purpose, the server is started as it was before sealing existed: no flag, no volume.
func TestTheServerChartSealsNothingWhenSealingIsSwitchedOff(t *testing.T) {
	pod := serverPod(t, serverRenderWith(t, "ikhnos"))
	for _, a := range pod.Containers[0].Args {
		if strings.Contains(a, "sealing") {
			t.Errorf("arg %q without a key named", a)
		}
	}
	for _, e := range pod.Containers[0].Env {
		if e.Name == "IKHNOS_SEALING_KEY_FILE" {
			t.Errorf("%s is set without a key named", e.Name)
		}
	}
	for _, v := range pod.Volumes {
		if v.Name == "sealing-key" {
			t.Error("a sealing volume without a key named")
		}
	}
}

func TestTheServerChartMountsTheSealingKeyAsAFileAndTellsTheServerWhere(t *testing.T) {
	for _, c := range []struct{ name, key, file string }{
		{"ikhnos-sealing", "", "sealing.key"},
		{"my-keys", "k.txt", "k.txt"},
	} {
		extra := []string{"--set", "sealing.disabled=false", "--set", "sealing.keySecret.name=" + c.name}
		if c.key != "" {
			extra = append(extra, "--set", "sealing.keySecret.key="+c.key)
		}
		pod := serverPod(t, serverRenderWith(t, "ikhnos", extra...))
		// In the environment, not an argument: a command run with `kubectl exec` has the environment of the container and not its arguments.
		env := ""
		for _, e := range pod.Containers[0].Env {
			if e.Name == "IKHNOS_SEALING_KEY_FILE" {
				env = e.Value
			}
		}
		if want := "/etc/ikhnos/sealing/" + c.file; env != want {
			t.Errorf("%v: IKHNOS_SEALING_KEY_FILE = %q, want %q", c, env, want)
		}
		var vol *corev1.Volume
		for i := range pod.Volumes {
			if pod.Volumes[i].Name == "sealing-key" {
				vol = &pod.Volumes[i]
			}
		}
		if vol == nil || vol.Secret == nil || vol.Secret.SecretName != c.name || len(vol.Secret.Items) != 1 || vol.Secret.Items[0].Key != c.file || vol.Secret.Items[0].Path != c.file {
			t.Fatalf("%v: volume = %+v", c, vol)
		}
		// Only the key is mounted, read only, and group-readable at most: it is not world readable.
		if vol.Secret.DefaultMode == nil || *vol.Secret.DefaultMode&0o007 != 0 {
			t.Errorf("%v: the key file mode is %v", c, vol.Secret.DefaultMode)
		}
		found := false
		for _, m := range pod.Containers[0].VolumeMounts {
			if m.Name == "sealing-key" {
				found = m.MountPath == "/etc/ikhnos/sealing" && m.ReadOnly
			}
		}
		if !found {
			t.Errorf("%v: the key is not mounted read-only at /etc/ikhnos/sealing", c)
		}
	}
}

func TestTheServerChartRefusesASealingSecretWithoutAKeyName(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), "ikhnos-server")
	copyTree(t, filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-server"), dir)
	copyTree(t, "ikhnos-fusion", filepath.Join(dir, "charts", "ikhnos-fusion"))
	out, err := exec.Command(h, "template", "ikhnos", dir, "-n", "ikhnos",
		"--set", "agent.publicAddress=192.0.2.10:30443", "--set", "agent.service.type=NodePort", "--set", "admin.behindTlsProxy=true",
		"--set", "sealing.keySecret.name=s", "--set-string", "sealing.keySecret.key=").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "sealing.keySecret.key must be set together with .name") {
		t.Fatalf("a Secret named without a key name was rendered: %v\n%s", err, out)
	}
}

// serverTemplate renders the server chart with the values given and nothing else beyond what it needs to render at all, returning the output
// and the error: for the tests about what the chart refuses.
func serverTemplate(t *testing.T, extra ...string) (string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	dir := filepath.Join(t.TempDir(), "ikhnos-server")
	copyTree(t, filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-server"), dir)
	copyTree(t, "ikhnos-fusion", filepath.Join(dir, "charts", "ikhnos-fusion"))
	out, err := exec.Command(h, append([]string{"template", "ikhnos", dir, "-n", "ikhnos",
		"--set", "agent.publicAddress=192.0.2.10:30443", "--set", "agent.service.type=NodePort", "--set", "admin.behindTlsProxy=true"}, extra...)...).CombinedOutput()
	return string(out), err
}

// A new install has a key or says it has none: the chart's defaults alone do not render, and the message carries the commands that fix it.
func TestTheServerChartDoesNotInstallWithoutASealingKeyOrASaidSoDisable(t *testing.T) {
	out, err := serverTemplate(t)
	if err == nil {
		t.Fatalf("the defaults rendered with no sealing key and no opt-out:\n%.400s", out)
	}
	for _, want := range []string{"sealing.keySecret.name is not set", "kubectl -n <namespace> create secret generic ikhnos-sealing", "--set sealing.keySecret.name=ikhnos-sealing", "--set sealing.disabled=true"} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, out)
		}
	}
	// Either of the two ways in renders.
	if out, err := serverTemplate(t, "--set", "sealing.keySecret.name=ikhnos-sealing"); err != nil {
		t.Errorf("a named key was refused: %v\n%.400s", err, out)
	}
	if out, err := serverTemplate(t, "--set", "sealing.disabled=true"); err != nil {
		t.Errorf("sealing.disabled was refused: %v\n%.400s", err, out)
	}
}

func TestTheServerChartRefusesToBeBothSealedAndNot(t *testing.T) {
	out, err := serverTemplate(t, "--set", "sealing.keySecret.name=ikhnos-sealing", "--set", "sealing.disabled=true")
	if err == nil || !strings.Contains(out, "sealing.disabled=true and sealing.keySecret.name are both set") {
		t.Fatalf("a contradiction rendered: %v\n%.400s", err, out)
	}
}

// The ready-made values files under the chart's ci/ directory are what the documentation points people at; each must render by itself,
// with nothing set beside it (so none of them can be left behind by a rule the chart gains).
func TestEveryReadyMadeServerValuesFileRendersByItself(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-server", "ci", "*.yaml"))
	if len(files) == 0 {
		t.Fatal("no ready-made values files found")
	}
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		dir := filepath.Join(t.TempDir(), "ikhnos-server")
		copyTree(t, filepath.Join("..", "..", "..", "deploy", "helm", "ikhnos-server"), dir)
		copyTree(t, "ikhnos-fusion", filepath.Join(dir, "charts", "ikhnos-fusion"))
		if out, err := exec.Command(h, "template", "ikhnos", dir, "-n", "ikhnos", "-f", abs).CombinedOutput(); err != nil {
			t.Errorf("%s does not render: %v\n%.600s", filepath.Base(f), err, out)
		}
	}
}
