package estate

import (
	"reflect"
	"testing"

	"continuum/internal/agents"
	"continuum/internal/decisionloop"
)

// The loop records a change to access in its own shape and the agents judge it in theirs; the hub converts one into the other. With every
// field of the loop's record set, each must arrive: a field added to a proposal cannot be dropped on the way to the check, and one added to
// the agents' side with nothing to set it fails too.
func TestAccessChangeArrivesFieldForField(t *testing.T) {
	n := 1
	in := decisionloop.AccessChange{AgentID: "a1", Tier: &n, Pause: []string{"p"}, Resume: []string{"r"}, Exclude: []string{"e"}, Include: []string{"i"}}
	if v := reflect.ValueOf(in); v.NumField() != 6 {
		t.Fatalf("decisionloop.AccessChange has %d fields, not 6: set the new one above and carry it", v.NumField())
	}
	out := agentsChange(in)
	want := agents.AccessChange{Tier: &n, Pause: []string{"p"}, Resume: []string{"r"}, Exclude: []string{"e"}, Include: []string{"i"}}
	if !reflect.DeepEqual(out, want) {
		t.Fatalf("the change did not arrive as it went:\n got %+v\nwant %+v", out, want)
	}
	if got := reflect.TypeOf(out).NumField(); got != 5 {
		t.Fatalf("agents.AccessChange has %d fields, not 5: nothing sets the new one", got)
	}
}
