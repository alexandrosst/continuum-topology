package install

import (
	"reflect"
	"strings"
	"testing"
)

func TestAgentChoicesAreTheValuesOfTheCollectorsTheScopeAndThePurge(t *testing.T) {
	got, err := AgentChoices(2, Collectors{NodeProbe: true, FlowObserver: true, Measurements: true},
		Scope{Namespaces: []string{"shop"}, Exclude: []string{"pay"}, Selector: "ikhnos.io/scope=yes"}, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Value{Set("nodeProbe.enabled", "true"), Set("flowObserver.enabled", "true"), Set("measurements.enabled", "true"),
		Strings("scope.namespaces", []string{"shop"}), Strings("scope.exclude", []string{"pay"}), SetString("scope.selector", "ikhnos.io/scope=yes"),
		Set("identity.purgeOnUninstall", "true")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%v\nwant\n%v", got, want)
	}
	if none, err := AgentChoices(2, Collectors{}, Scope{}, false); err != nil || len(none) != 0 {
		t.Fatalf("nothing chosen is nothing set: %v %v", none, err)
	}
}

func TestAgentChoicesRefuseWhatTheTierCannotHonourAndSayAllOfIt(t *testing.T) {
	_, err := AgentChoices(0, Collectors{NodeProbe: true, FlowObserver: true}, Scope{Namespaces: []string{"Shop"}}, false)
	pe, ok := err.(*ProblemsError)
	if !ok {
		t.Fatalf("want the problems, got %v", err)
	}
	// The collectors first, in the order the page lists them, then the scope: the first is what a caller that tells one tells.
	if len(pe.Problems) != 3 || pe.Problems[0] != "the node probe needs access tier 1 or higher" || pe.Problems[1] != "the traffic observer needs access tier 2 or higher" {
		t.Fatalf("problems: %q", pe.Problems)
	}
	if !strings.Contains(pe.Problems[2], "Shop") && !strings.Contains(strings.ToLower(pe.Problems[2]), "namespace") {
		t.Errorf("the scope problem is missing: %q", pe.Problems[2])
	}
	if _, err := AgentChoices(1, Collectors{Measurements: true}, Scope{}, false); err == nil {
		t.Error("measurements were allowed at tier 1")
	}
}

func TestHelmValuesReadEachKindTheWayAFlagWould(t *testing.T) {
	got, err := HelmValues([]Value{
		Set("access.tier", "1"), Set("nodeProbe.enabled", "true"), Set("server.address", "a.example:8443"), Set("empty", ""),
		SetString("scope.selector", "ikhnos.io/scope=yes"), SetString("looksLikeANumber", "007"),
		Strings("scope.namespaces", []string{"shop", "pay"}), Strings("scope.exclude", nil),
		SetJSON("doc", `{"a":[1,"x"],"b":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"access":           map[string]any{"tier": int64(1)},
		"nodeProbe":        map[string]any{"enabled": true},
		"server":           map[string]any{"address": "a.example:8443"},
		"empty":            "",
		"scope":            map[string]any{"selector": "ikhnos.io/scope=yes", "namespaces": []any{"shop", "pay"}, "exclude": []any{}},
		"looksLikeANumber": "007",
		"doc":              map[string]any{"a": []any{float64(1), "x"}, "b": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%#v\nwant\n%#v", got, want)
	}
}

func TestHelmValuesTheLaterOneWinsAndABadNameIsRefused(t *testing.T) {
	got, err := HelmValues([]Value{Set("a.b", "1"), Set("a.b", "2"), Set("a.c", "x")})
	if err != nil || !reflect.DeepEqual(got, map[string]any{"a": map[string]any{"b": int64(2), "c": "x"}}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, k := range []string{"", "-x", "a=b", "a b", "a..b", ".a"} {
		if _, err := HelmValues([]Value{Set(k, "v")}); err == nil {
			t.Errorf("%q was taken for a value name", k)
		}
	}
	if _, err := HelmValues([]Value{SetJSON("k", "{nope")}); err == nil {
		t.Error("a JSON value that is not JSON was taken")
	}
}
