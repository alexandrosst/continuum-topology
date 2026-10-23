package agents

import (
	"strings"
	"testing"
)

func TestConsentNarrowsOnlyWhenSomethingIsTakenAway(t *testing.T) {
	for name, c := range map[string]struct {
		cur, next Consent
		want      bool
	}{
		"nothing to something paused": {Consent{}, Consent{Paused: []string{"flow"}}, true},
		"something excluded":          {Consent{}, Consent{Excluded: []string{"shop"}}, true},
		"swapping one for another":    {Consent{Excluded: []string{"shop"}}, Consent{Excluded: []string{"batch"}}, true},
		"adding to what was there":    {Consent{Paused: []string{"flow"}}, Consent{Paused: []string{"flow", "probes"}}, true},
		"unchanged":                   {Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, false},
		"only widening":               {Consent{Paused: []string{"flow", "probes"}, Excluded: []string{"shop", "batch"}}, Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, false},
		"clearing everything":         {Consent{Paused: []string{"flow"}, Excluded: []string{"shop"}}, Consent{}, false},
	} {
		if got := ConsentNarrows(c.cur, c.next); got != c.want {
			t.Errorf("%s: consentNarrows = %v, want %v", name, got, c.want)
		}
	}
}

func TestUpgradeAndTeardownCommandsGuessTheNamespaceWhenNeverReported(t *testing.T) {
	helm, secret, guessed, err := TeardownCommands("", "")
	if err != nil {
		t.Fatal(err)
	}
	if !guessed || !strings.Contains(helm, "helm uninstall ikhnos-agent ") || !strings.Contains(helm, "--namespace ikhnos-system") || !strings.Contains(secret, "--namespace ikhnos-system") {
		t.Fatalf("teardown with nothing reported: %q %q %v", helm, secret, guessed)
	}
	helm, secret, guessed, err = TeardownCommands("apps-team", "")
	if err != nil {
		t.Fatal(err)
	}
	if !guessed || !strings.Contains(helm, "helm uninstall ikhnos-agent ") || !strings.Contains(helm, "--namespace apps-team") || !strings.Contains(secret, "--namespace apps-team") {
		t.Fatalf("teardown with a known namespace but no release: %q %q %v", helm, secret, guessed)
	}
	helm, secret, guessed, err = TeardownCommands("apps-team", "obs-agent")
	if err != nil {
		t.Fatal(err)
	}
	if guessed || !strings.Contains(helm, "helm uninstall obs-agent ") || !strings.Contains(helm, "--namespace apps-team") || !strings.Contains(secret, "--namespace apps-team") {
		t.Fatalf("teardown with a known namespace and release: %q %q %v", helm, secret, guessed)
	}
	// The identity Secret's own name never depends on the release: every chart object has a fixed name.
	if !strings.Contains(secret, "ikhnos-agent-identity") {
		t.Fatalf("secret name should stay fixed regardless of release name: %q", secret)
	}
}
