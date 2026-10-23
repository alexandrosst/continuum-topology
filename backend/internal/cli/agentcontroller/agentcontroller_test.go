package agentcontroller

import (
	"reflect"
	"testing"
)

func TestTheDefaultsAreOnlyWhatWasGiven(t *testing.T) {
	if got := Defaults("", "", ""); len(got) != 0 {
		t.Fatalf("%v", got)
	}
	got := Defaults("ghcr.io/acme/ikhnos", "1.2.3", "ghcr.io/acme/link")
	want := map[string]any{
		"image":     map[string]any{"repository": "ghcr.io/acme/ikhnos", "tag": "1.2.3"},
		"telemetry": map[string]any{"clusterLinks": map[string]any{"probe": map[string]any{"image": map[string]any{"repository": "ghcr.io/acme/link"}}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%v", got)
	}
	if got := Defaults("r", "", ""); !reflect.DeepEqual(got, map[string]any{"image": map[string]any{"repository": "r"}}) {
		t.Fatalf("%v", got)
	}
}

func TestWithoutANamespaceItStopsAndSays(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "")
	if code := Main(nil); code != 1 {
		t.Fatalf("exit %d", code)
	}
}

func TestHelpIsShownAndIsNotAFailure(t *testing.T) {
	if code := Main([]string{"-h"}); code != 0 && code != 2 {
		t.Fatalf("exit %d", code)
	}
}

func TestATierThatDoesNotExistStopsIt(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "ikhnos-system")
	if code := Main([]string{"--max-access-tier=3"}); code != 1 {
		t.Fatalf("exit %d", code)
	}
}
