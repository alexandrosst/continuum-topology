package chart

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func controllerDeployments(t *testing.T, args ...string) []appsv1.Deployment {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	out, err := exec.Command(h, append([]string{"template", "ct", controllerChart(), "--namespace", ctlNamespace}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", args, err, out)
	}
	var ds []appsv1.Deployment
	dec := yaml.NewYAMLOrJSONDecoder(strings.NewReader(string(out)), 4096)
	for {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		var k struct{ Kind string }
		if json.Unmarshal(raw, &k) == nil && k.Kind == "Deployment" {
			var d appsv1.Deployment
			if err := json.Unmarshal(raw, &d); err != nil {
				t.Fatal(err)
			}
			ds = append(ds, d)
		}
	}
	return ds
}

func TestTheControllerChartRendersOneControllerThatCannotRunTwice(t *testing.T) {
	ds := controllerDeployments(t, "--set", "image.repository=ghcr.io/acme/controller", "--set", "image.tag=1.2.3",
		"--set", "agent.image.repository=ghcr.io/acme/ikhnos", "--set", "agent.linkImage.repository=ghcr.io/acme/link", "--set", "maxAccessTier=1")
	if len(ds) != 1 {
		t.Fatalf("%d Deployments", len(ds))
	}
	d := ds[0]
	if *d.Spec.Replicas != 1 || d.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("replicas %d, strategy %s: two controllers would fight over the release", *d.Spec.Replicas, d.Spec.Strategy.Type)
	}
	pod := d.Spec.Template.Spec
	c := pod.Containers[0]
	if c.Image != "ghcr.io/acme/controller:1.2.3" {
		t.Errorf("image %s", c.Image)
	}
	env := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		env[e.Name] = e
	}
	if e := env["POD_NAMESPACE"]; e.ValueFrom == nil || e.ValueFrom.FieldRef == nil || e.ValueFrom.FieldRef.FieldPath != "metadata.namespace" {
		t.Errorf("POD_NAMESPACE is not the pod's own: %+v", e)
	}
	for k, want := range map[string]string{"IKHNOS_AGENT_IMAGE_REPOSITORY": "ghcr.io/acme/ikhnos", "IKHNOS_LINK_IMAGE_REPOSITORY": "ghcr.io/acme/link", "IKHNOS_AGENT_IMAGE_TAG": ""} {
		if e, ok := env[k]; !ok || e.Value != want {
			t.Errorf("env %s = %q (present %v), want %q", k, e.Value, ok, want)
		}
	}
	// The ceiling the permissions were trimmed to is the ceiling the controller enforces.
	found := false
	for _, a := range c.Args {
		found = found || a == "--max-access-tier=1"
	}
	if !found {
		t.Errorf("args %v do not carry the ceiling", c.Args)
	}
	sc := c.SecurityContext
	if pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot ||
		sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation ||
		sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("hardening: pod %+v, container %+v", pod.SecurityContext, sc)
	}
}

func TestTheControllerChartDefaultsToTheTopCeilingAndTheEdgeTagIsAlwaysPulled(t *testing.T) {
	d := controllerDeployments(t)[0]
	args := strings.Join(d.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--max-access-tier=2") {
		t.Errorf("args %q", args)
	}
	// Telemetry is off unless it is asked for, and what it is set to is what the controller is told.
	if !strings.Contains(args, "--allow-telemetry=false") {
		t.Errorf("args %q do not say telemetry is off", args)
	}
	on := strings.Join(controllerDeployments(t, "--set", "allowTelemetry=true")[0].Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(on, "--allow-telemetry=true") {
		t.Errorf("args %q", on)
	}
	if p := controllerDeployments(t, "--set", "image.tag=edge")[0].Spec.Template.Spec.Containers[0].ImagePullPolicy; p != corev1.PullAlways {
		t.Errorf("edge is pulled %s", p)
	}
	if p := controllerDeployments(t, "--set", "image.tag=1.0.0")[0].Spec.Template.Spec.Containers[0].ImagePullPolicy; p != corev1.PullIfNotPresent {
		t.Errorf("a fixed tag is pulled %s", p)
	}
	img := controllerDeployments(t, "--set", "image.digest=sha256:"+strings.Repeat("a", 64))[0].Spec.Template.Spec.Containers[0].Image
	if !strings.Contains(img, "@sha256:") || strings.Contains(img, ":0.1.0") {
		t.Errorf("a digest does not win: %s", img)
	}
}

func TestTheControllerChartRefusesWhatItCannotMean(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	for _, c := range []struct{ name, set, want string }{
		{"a tier that does not exist", "maxAccessTier=3", "maxAccessTier"},
		{"a typo", "maxAcessTier=1", "additional properties"},
		{"telemetry that is not a switch", "allowTelemetry=maybe", "allowTelemetry"},
		{"a digest that is not one", "image.digest=latest", "digest"},
	} {
		out, err := exec.Command(h, "template", "ct", controllerChart(), "--set", c.set).CombinedOutput()
		if err == nil || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: err %v, output %q", c.name, err, out)
		}
	}
}

func TestTheControllerChartShipsItsDefinitionInCrdsAndNowhereElse(t *testing.T) {
	for _, o := range helmObjects(t, controllerChart()) {
		if o.Kind == "CustomResourceDefinition" {
			t.Fatal("the definition is rendered by the chart: it belongs in crds/, which Helm installs once and never changes; a copy in templates/ would be replaced by every upgrade without anyone asking")
		}
	}
}
