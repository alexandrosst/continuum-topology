package server

import (
	"strings"
	"testing"

	"continuum/internal/store"
)

// What an agent reports about itself is pasted into commands, so only plain Kubernetes names count.
func TestReleaseTargetIgnoresNamesThatAreNotKubernetesNames(t *testing.T) {
	ns, rel, guessed := releaseTarget("prod-agents", "my-agent")
	if ns != "prod-agents" || rel != "my-agent" || guessed {
		t.Fatalf("valid names changed: %q %q %v", ns, rel, guessed)
	}
	for _, bad := range []string{"x; rm -rf /", "Has Caps", "a b", "$(id)", "-lead", "trail-", "a\nb"} {
		ns, rel, guessed = releaseTarget(bad, bad)
		if ns != "continuum-system" || rel != "continuum-agent" || !guessed {
			t.Fatalf("%q was used: %q %q %v", bad, ns, rel, guessed)
		}
	}
}

// A generated Secret command can be run again: create-or-update (`apply`), never a bare `create`, and its values are
// on standard input, never in the arguments.
func TestApplySecretCommandIsCreateOrUpdateFromStdin(t *testing.T) {
	got := applySecretCommand("s", "ns1", secretKV("a", "1"), secretKV("b", `"2"`), secretKV("pem", "-----BEGIN X-----\nAAA\n-----END X-----\n"))
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

// Nothing in the data can end the here-document early: its closing word moves out of the way.
func TestApplySecretCommandPicksAClosingWordTheDataDoesNotUse(t *testing.T) {
	got := applySecretCommand("s", "ns1", secretKV("a", "x\nCONTINUUM_SECRET\ny"))
	if !strings.HasPrefix(got, "kubectl apply --server-side --force-conflicts -f - <<'CONTINUUM_SECRET_X'\n") || !strings.HasSuffix(got, "\nCONTINUUM_SECRET_X") {
		t.Fatalf("closing word did not change:\n%s", got)
	}
}

// A value that is not a shell word is quoted whole, so an address or a chart reference from a setting can only ever be
// one argument.
func TestSetFlagQuotesWhatIsNotAShellWord(t *testing.T) {
	for _, c := range []struct{ key, val, want string }{
		{"a.b", "host:4317", "--set a.b=host:4317"},
		{"a.b", "", "--set a.b="},
		{"a.b", "x; rm -rf /", "--set 'a.b=x; rm -rf /'"},
		{"a.b", "it's $(id)", `--set 'a.b=it'\''s $(id)'`},
		{"a.b", "a\nb", "--set 'a.b=a\nb'"},
	} {
		if got := setFlag(c.key, c.val); got != c.want {
			t.Errorf("setFlag(%q, %q) = %s, want %s", c.key, c.val, got, c.want)
		}
	}
}

// The regional operator runs the upstream collector image its chart names. A configured registry holds the
// `continuum` image, not an operator one, so the command must not point the operator's image at it.
func TestOperatorInstallCommandNeverOverridesTheImage(t *testing.T) {
	a := newAdminRig(t)
	op := store.Operator{ID: "op-abc123", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "c:4317"}}
	img := ImageConfig{Registry: "ghcr.io/me", Tag: "0.2.0-dev", Digest: "sha256:" + strings.Repeat("a", 64)}
	got, _ := a.a.operatorInstallCommand(img, "", op, OperatorTLSBundle{ReceiverCertPEM: []byte("x")}, "")
	if strings.Contains(got, "image.") {
		t.Fatalf("operator install command sets an image: %s", got)
	}
}
