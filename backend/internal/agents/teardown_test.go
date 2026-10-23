package agents

import (
	"testing"
)

// What an agent reports about itself is pasted into commands, so only plain Kubernetes names count.
func TestReleaseTargetIgnoresNamesThatAreNotKubernetesNames(t *testing.T) {
	ns, rel, guessed := ReleaseTarget("prod-agents", "my-agent")
	if ns != "prod-agents" || rel != "my-agent" || guessed {
		t.Fatalf("valid names changed: %q %q %v", ns, rel, guessed)
	}
	for _, bad := range []string{"x; rm -rf /", "Has Caps", "a b", "$(id)", "-lead", "trail-", "a\nb"} {
		ns, rel, guessed = ReleaseTarget(bad, bad)
		if ns != "ikhnos-system" || rel != "ikhnos-agent" || !guessed {
			t.Fatalf("%q was used: %q %q %v", bad, ns, rel, guessed)
		}
	}
}
