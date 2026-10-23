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

// Without a key named, the server is started as it was before sealing existed: no flag, no volume. An upgrade of an existing install must not
// change what runs.
func TestTheServerChartSealsNothingUntilAKeyIsNamed(t *testing.T) {
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
		extra := []string{"--set", "sealing.keySecret.name=" + c.name}
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
