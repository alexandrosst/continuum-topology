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

// A generated Secret command can be run again: create-or-update, never a bare `create`.
func TestApplySecretCommandIsCreateOrUpdate(t *testing.T) {
	got := applySecretCommand("s", "ns1", "a=1", `b="2"`)
	want := "kubectl create secret generic s --namespace ns1 \\\n  --from-literal=a=1 \\\n  --from-literal=b=\"2\" \\\n  --dry-run=client -o yaml | kubectl apply -f -"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.HasSuffix(got, "| kubectl apply -f -") {
		t.Fatal("not an apply")
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
