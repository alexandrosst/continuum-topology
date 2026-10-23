package install

import (
	"reflect"
	"strings"
	"testing"
)

func TestScopeProblems(t *testing.T) {
	long := strings.Repeat("a", 64)
	for _, c := range []struct {
		name  string
		scope Scope
		want  []string
	}{
		{"nothing", Scope{}, nil},
		{"good names", Scope{Namespaces: []string{"shop", "pay-ments"}, Exclude: []string{"a1"}}, nil},
		{"a bad name", Scope{Namespaces: []string{"shop", "Bad_Name"}}, []string{`"Bad_Name" is not a valid namespace name`}},
		{"a bad exclude", Scope{Exclude: []string{"-x"}}, []string{`"-x" is not a valid namespace name`}},
		{"a name over 63", Scope{Namespaces: []string{long}}, []string{`"` + long + `" is not a valid namespace name`}},
		{"a selectable label", Scope{Selector: "ikhnos.io/scope=yes"}, nil},
		{"a selectable label with spaces around", Scope{Selector: "  app=core "}, nil},
		{"a key alone", Scope{Selector: "ikhnos.io/scope"}, nil},
		{"a label the agent does not read", Scope{Selector: "team=a"}, []string{`The agent only reads a fixed list of labels, so select on one starting with ikhnos.io/ (for example ikhnos.io/scope=yes), not "team"`}},
		{"not a label key", Scope{Selector: "-bad=a"}, []string{`"-bad" is not a valid label key`}},
		{"two equals", Scope{Selector: "ikhnos.io/x=a=b"}, []string{"Write the label as key=value"}},
		{"a value that is not a label value", Scope{Selector: "app=a b"}, []string{"Write the label as key=value"}},
		{"a hostile selector", Scope{Selector: "app=$(id)"}, []string{"Write the label as key=value"}},
	} {
		if got := c.scope.Problems(); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestScopeValues(t *testing.T) {
	if v := (Scope{}).Values(); v != nil {
		t.Errorf("no scope states nothing, got %v", v)
	}
	// A scope with a problem is not printed at all, whatever else it holds.
	if v := (Scope{Namespaces: []string{"ok", "Bad Name"}}).Values(); v != nil {
		t.Errorf("a scope the agent would refuse is not printed, got %v", v)
	}
	got := Flags(Scope{Namespaces: []string{"a", "b"}, Exclude: []string{"hr"}, Selector: " ikhnos.io/scope=yes "}.Values())
	want := "--set scope.namespaces='{a,b}' --set scope.exclude='{hr}' --set-string scope.selector=ikhnos.io/scope=yes"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := Flags(Scope{Exclude: []string{"hr"}}.Values()); got != "--set scope.exclude='{hr}'" {
		t.Errorf("only what is set is stated, got %s", got)
	}
}
