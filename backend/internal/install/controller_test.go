package install

import (
	"strings"
	"testing"
)

func TestTheResourceSaysWhatTheCommandSays(t *testing.T) {
	r := AgentResource{
		Server: "ik.example.com:8443", CAPin: "sha256/AbC+/=", Tier: 1,
		Collectors:    Collectors{NodeProbe: true},
		Scope:         Scope{Namespaces: []string{"shop", "pay"}, Exclude: []string{"pay-test"}, Selector: "ikhnos.io/scope=yes"},
		PurgeIdentity: true,
		Image:         &ResourceImage{Repository: "reg.example/ikhnos", Tag: "1.2.3", Digest: "sha256:" + strings.Repeat("a", 64)},
	}
	got, err := r.Manifest("ikhnos-system")
	if err != nil {
		t.Fatal(err)
	}
	want := `apiVersion: ikhnos.io/v1alpha1
kind: IkhnosAgent
metadata:
  name: this-cluster
  namespace: ikhnos-system
spec:
  server:
    address: "ik.example.com:8443"
    caPin: "sha256/AbC+/="
  enrollment:
    tokenSecretRef:
      name: ikhnos-enrollment
  accessTier: 1
  collectors:
    nodeProbe: true
  scope:
    namespaces: ["shop","pay"]
    exclude: ["pay-test"]
    selector: "ikhnos.io/scope=yes"
  purgeIdentityOnDelete: true
  image:
    repository: "reg.example/ikhnos"
    tag: "1.2.3"
    digest: "sha256:` + strings.Repeat("a", 64) + `"
`
	if got != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s", got, want)
	}
}

func TestAResourceThatAsksForNothingExtraSaysNothingExtra(t *testing.T) {
	got, err := AgentResource{Server: "h:1", CAPin: "sha256/x", Tier: 2}.Manifest("ikhnos-system")
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"accessTier", "collectors", "scope", "purgeIdentityOnDelete", "image"} {
		if strings.Contains(got, word) {
			t.Errorf("a plain install mentions %s:\n%s", word, got)
		}
	}
}

func TestAResourceTheControllerWouldRefuseIsNotWritten(t *testing.T) {
	ok := AgentResource{Server: "h:1", CAPin: "sha256/x", Tier: 2}
	for name, r := range map[string]AgentResource{
		"no port":                    {Server: "h", CAPin: "sha256/x", Tier: 2},
		"no host":                    {Server: ":1", CAPin: "sha256/x", Tier: 2},
		"a port out of range":        {Server: "h:70000", CAPin: "sha256/x", Tier: 2},
		"no pin":                     {Server: "h:1", Tier: 2},
		"a pin with a dot":           {Server: "h:1", CAPin: "a.b", Tier: 2},
		"a tier above the highest":   {Server: "h:1", CAPin: "sha256/x", Tier: 3},
		"a tier below the lowest":    {Server: "h:1", CAPin: "sha256/x", Tier: -1},
		"a collector above its tier": {Server: "h:1", CAPin: "sha256/x", Tier: 1, Collectors: Collectors{FlowObserver: true}},
		"a scope the agent refuses":  {Server: "h:1", CAPin: "sha256/x", Tier: 2, Scope: Scope{Namespaces: []string{"A b"}}},
	} {
		if _, err := r.Manifest("ikhnos-system"); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if _, err := ok.Manifest("Not A Name"); err == nil {
		t.Error("a namespace that is not a name was written")
	}
}

// Whatever a value holds, it is one quoted scalar in the manifest, and the manifest does not end early.
func TestAValueInTheResourceIsOneScalarWhateverItHolds(t *testing.T) {
	r := AgentResource{Server: "h:1", CAPin: "sha256/x", Tier: 2, Image: &ResourceImage{Repository: "r\"\nkind: Secret\n", Tag: "x: y # z"}}
	got, err := r.Apply("ikhnos-system")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(got, "\nkind:") != 1 || !strings.HasSuffix(got, "\nYAML") || strings.Count(got, "\nYAML") != 1 {
		t.Fatalf("a value broke out of its line:\n%s", got)
	}
	if !strings.HasPrefix(got, "kubectl apply -f - <<'YAML'\n") {
		t.Fatalf("not a quoted here-document:\n%s", got)
	}
}

func TestTheControllerIsInstalledForTheTierAndNoMoreAndFromTheRegistryChosen(t *testing.T) {
	got, err := ControllerInstall{Chart: "oci://reg.example/ikhnos-controller", Version: "1.2.3", MaxTier: 1, Registry: "reg.example/"}.Render()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"helm install ikhnos-controller oci://reg.example/ikhnos-controller", "--version 1.2.3", "--namespace ikhnos-system", "--create-namespace",
		"--set maxAccessTier=1", "--set image.repository=reg.example/controller", "--set agent.image.repository=reg.example/ikhnos", "--set agent.linkImage.repository=reg.example/link",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in\n%s", want, got)
		}
	}
	// With no registry the chart's own image names stand, and with no known version the command asks for none.
	got, err = ControllerInstall{Chart: "oci://x/ikhnos-controller", MaxTier: 2}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "image.") || strings.Contains(got, "--version") || !strings.Contains(got, "maxAccessTier=2") {
		t.Errorf("a plain controller install:\n%s", got)
	}
	if _, err := (ControllerInstall{Chart: "oci://x/ikhnos-controller", MaxTier: 3}).Render(); err == nil {
		t.Error("a tier above the highest was written")
	}
	if _, err := (ControllerInstall{Chart: "-oops", MaxTier: 1}).Render(); err == nil {
		t.Error("a chart that is an option was written")
	}
}

func TestTheTokenIsInTheSecretsCommandAndNowhereElse(t *testing.T) {
	const token = "s3cr3t-token-value"
	secret, err := ControllerTokenSecretCommand(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"kind: Secret", "name: ikhnos-enrollment", "namespace: ikhnos-system", `token: "` + token + `"`, "<<'IKHNOS_SECRET'"} {
		if !strings.Contains(secret, want) {
			t.Errorf("%q is not in\n%s", want, secret)
		}
	}
	// It is read from standard input: nothing on the command line, which every process on the machine can read.
	if first, _, _ := strings.Cut(secret, "\n"); strings.Contains(first, token) {
		t.Errorf("the token is on the command line: %s", first)
	}
	m, _ := AgentResource{Server: "h:1", CAPin: "sha256/x", Tier: 2}.Apply("ikhnos-system")
	inst, _ := ControllerInstall{Chart: "oci://x/ikhnos-controller", MaxTier: 2}.Render()
	if strings.Contains(m, token) || strings.Contains(inst, token) {
		t.Error("the token reached the resource or the controller's install")
	}
}
