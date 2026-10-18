package kernel

import (
	"reflect"
	"strings"
	"testing"
)

func TestLinkPairsAreCanonicalAndChecked(t *testing.T) {
	got, err := NormalizeLinks([]LinkSetting{{A: "cl-b", B: "cl-a"}, {A: "cl-a", B: "cl-b"}, {A: " cl-c ", B: "cl-a"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []LinkSetting{{A: "cl-a", B: "cl-b"}, {A: "cl-a", B: "cl-c"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for name, in := range map[string][]LinkSetting{
		"empty":    {{A: "cl-a"}},
		"itself":   {{A: "cl-a", B: "cl-a"}},
		"too long": {{A: strings.Repeat("x", 65), B: "cl-a"}},
	} {
		if _, err := NormalizeLinks(in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	many := make([]LinkSetting, maxLinks+1)
	for i := range many {
		many[i] = LinkSetting{A: "cl-a", B: "cl-" + strings.Repeat("z", i+1)}
	}
	if _, err := NormalizeLinks(many); err == nil {
		t.Error("more than the limit was accepted")
	}
}
