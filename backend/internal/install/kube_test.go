package install

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// The lines the server printed before there was a renderer for them (copied from internal/server/testdata/install_commands.golden.json).
func TestTheKubectlAndHelmLinesTheServerPrints(t *testing.T) {
	for _, c := range []struct {
		name string
		r    interface{ Render() (string, error) }
		want string
	}{
		{"uninstall", Uninstall{Release: "obs-agent", Namespace: "apps-team"}, "helm uninstall obs-agent --namespace apps-team"},
		{"delete the identity", DeleteSecret{Name: "continuum-agent-identity", Namespace: "apps-team"}, "kubectl delete secret continuum-agent-identity --namespace apps-team"},
		{"restart", RestartDeployment{Deployment: "athens-regional-operator", Namespace: "continuum-system"}, "kubectl rollout restart deployment/athens-regional-operator --namespace continuum-system"},
		{"expose", SetServiceType{Namespace: "continuum-system", Service: "athens-regional-operator", Type: NodePort},
			`kubectl --namespace continuum-system patch svc athens-regional-operator -p '{"spec":{"type":"NodePort"}}'`},
		{"a Secret", Secret{Name: "athens-heartbeat-auth", Namespace: "continuum-system", Entries: []Entry{{"token", "hb_SECRET"}}},
			"kubectl apply --server-side --force-conflicts -f - <<'CONTINUUM_SECRET'\napiVersion: v1\nkind: Secret\nmetadata:\n  name: athens-heartbeat-auth\n  namespace: continuum-system\ntype: Opaque\nstringData:\n  token: \"hb_SECRET\"\nCONTINUUM_SECRET"},
		{"a Secret in a namespace that may not exist yet", Secret{Name: "s", Namespace: "ns1", EnsureNamespace: true, Entries: []Entry{{"ca.crt", "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"}}},
			"kubectl create namespace ns1 --dry-run=client -o yaml | kubectl apply -f - && \\\n" +
				"kubectl apply --server-side --force-conflicts -f - <<'CONTINUUM_SECRET'\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns1\ntype: Opaque\nstringData:\n" +
				"  ca.crt: |\n    -----BEGIN CERTIFICATE-----\n    AAAA\n    -----END CERTIFICATE-----\nCONTINUUM_SECRET"},
	} {
		got, err := c.r.Render()
		if err != nil || got != c.want {
			t.Errorf("%s:\n got %q (%v)\nwant %q", c.name, got, err, c.want)
		}
	}
}

// What a Secret's command applies is read back by a real shell and a real YAML reader, and it is exactly the data given: nothing in a
// value ends the here-document early, runs, or is changed.
func TestASecretIsAppliedExactlyAsGiven(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	data := []Entry{
		{"plain", "value"},
		{"quotes", `it's "x" $(touch /tmp/pwned) ` + "`id`"},
		{"ends-early", "a\nCONTINUUM_SECRET\nb"},
		{"ends-early-twice", "a\nCONTINUUM_SECRET\nCONTINUUM_SECRET_X\nb"},
		{"indented-end", "a\n  CONTINUUM_SECRET  \nb"},
		{"colon-and-hash", "a: b # c"},
		{"pem", "-----BEGIN EC PRIVATE KEY-----\nMHcCAQEEIA\n-----END EC PRIVATE KEY-----"},
		{"leading-space", " x\ny"},
		{"unicode", "é世界"},
		{"empty", ""},
	}
	for _, ensure := range []bool{false, true} {
		cmd, err := Secret{Name: "my-secret", Namespace: "ns1", Entries: data, EnsureNamespace: ensure}.Render()
		if err != nil {
			t.Fatal(err)
		}
		var out, stderr bytes.Buffer
		sh := exec.Command(bash, "-c", "kubectl() { if [ \"$1\" = apply ]; then cat; else cat >/dev/null; fi; }\n"+cmd+"\n")
		sh.Stdout, sh.Stderr = &out, &stderr
		if err := sh.Run(); err != nil {
			t.Fatalf("%v\n%s\n%s", err, stderr.String(), cmd)
		}
		var manifest struct {
			Metadata   struct{ Name, Namespace string }
			StringData map[string]string `json:"stringData"`
		}
		if err := yaml.Unmarshal(out.Bytes(), &manifest); err != nil {
			t.Fatalf("the manifest is not YAML: %v\n%s", err, out.String())
		}
		if manifest.Metadata.Name != "my-secret" || manifest.Metadata.Namespace != "ns1" {
			t.Fatalf("metadata: %+v", manifest.Metadata)
		}
		for _, e := range data {
			// A multi-line value is written as a block, which YAML reads with exactly one trailing newline (as a PEM block has).
			want := e.Value
			if v := strings.TrimRight(e.Value, "\n"); strings.Contains(v, "\n") && !strings.HasPrefix(v, " ") {
				want = v + "\n"
			}
			if got := manifest.StringData[e.Key]; got != want {
				t.Errorf("%s: applied %q, want %q", e.Key, got, want)
			}
		}
		if len(manifest.StringData) != len(data) {
			t.Errorf("%d keys applied, want %d", len(manifest.StringData), len(data))
		}
	}
}

// A name that is not a Kubernetes name, a key that is not a Secret key and a type that is not a Service type are refused, which also
// keeps a name from being read as an option.
func TestWhatIsNotAKubernetesNameIsRefused(t *testing.T) {
	bad := []string{"", "-x", "--kubeconfig=/x", "A", "a b", "a;b", "a\nb", "a'b", "$(id)", "a_b", ".a", "a."}
	for _, n := range bad {
		for _, r := range []interface{ Render() (string, error) }{
			Uninstall{Release: n, Namespace: "ns"}, Uninstall{Release: "r", Namespace: n},
			DeleteSecret{Name: n, Namespace: "ns"}, RestartDeployment{Deployment: n, Namespace: "ns"},
			SetServiceType{Namespace: "ns", Service: n, Type: NodePort}, SetServiceType{Namespace: n, Service: "s", Type: NodePort},
			Secret{Name: n, Namespace: "ns"}, Secret{Name: "s", Namespace: n},
		} {
			if got, err := r.Render(); err == nil {
				t.Errorf("%q: rendered %q", n, got)
			}
		}
	}
	for _, r := range []interface{ Render() (string, error) }{
		Secret{Name: "s", Namespace: "ns", Entries: []Entry{{"a b", "x"}}},
		Secret{Name: "s", Namespace: "ns", Entries: []Entry{{"", "x"}}},
		Secret{Name: "s", Namespace: "ns", Entries: []Entry{{"a\nb", "x"}}},
		SetServiceType{Namespace: "ns", Service: "s", Type: `NodePort"}}'; id; '`},
		SetServiceType{Namespace: "ns", Service: "s"},
	} {
		if got, err := r.Render(); err == nil {
			t.Errorf("rendered %q", got)
		}
	}
}

// Nothing in the data can end the here-document early: its closing word moves out of the way of any line that is the same as it.
func TestTheClosingWordMovesOutOfTheWayOfTheData(t *testing.T) {
	got, err := Secret{Name: "s", Namespace: "ns1", Entries: []Entry{{"a", "x\nCONTINUUM_SECRET\ny"}}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "kubectl apply --server-side --force-conflicts -f - <<'CONTINUUM_SECRET_X'\n") || !strings.HasSuffix(got, "\nCONTINUUM_SECRET_X") {
		t.Fatalf("closing word did not change:\n%s", got)
	}
}

// A generated Secret command can be run again: create-or-update (`apply`), never a bare `create`, and its values are on standard input,
// never in the arguments.
func TestASecretIsCreateOrUpdateFromStdin(t *testing.T) {
	got, err := Secret{Name: "s", Namespace: "ns1", Entries: []Entry{{"a", "1"}, {"b", `"2"`}, {"pem", "-----BEGIN X-----\nAAA\n-----END X-----\n"}}}.Render()
	if err != nil {
		t.Fatal(err)
	}
	want := "kubectl apply --server-side --force-conflicts -f - <<'CONTINUUM_SECRET'\napiVersion: v1\nkind: Secret\nmetadata:\n  name: s\n  namespace: ns1\ntype: Opaque\nstringData:\n" +
		"  a: \"1\"\n  b: \"\\\"2\\\"\"\n  pem: |\n    -----BEGIN X-----\n    AAA\n    -----END X-----\nCONTINUUM_SECRET"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	if strings.Contains(got, "--from-literal") || strings.Contains(got, "create secret") {
		t.Fatal("a value on the command line, or a bare create")
	}
	// A client-side `kubectl apply` copies the whole manifest - the private key included - into the
	// kubectl.kubernetes.io/last-applied-configuration annotation; a server-side one keeps no copy.
	if strings.Contains(got, "kubectl apply -f -") || !strings.HasPrefix(got, "kubectl apply --server-side --force-conflicts -f - <<") {
		t.Fatalf("the Secret is applied client-side, which stores its data in last-applied-configuration:\n%s", got)
	}
}
