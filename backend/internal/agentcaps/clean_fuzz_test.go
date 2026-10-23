package agentcaps

import (
	"slices"
	"strings"
	"testing"
)

// Clean is given whatever an agent lists in its hello, so it is held to what it promises for any list at all: at most MaxNames, every
// one well formed and no longer than MaxNameLen, no repeats, sorted, nothing that was not in the list (but for the cut), and the
// same result when it is cleaned again. The seeds run with every `go test`; `go test -fuzz=FuzzClean` looks for more.
func FuzzClean(f *testing.F) {
	f.Add("config-digest", "diagnostics", "Not A Name", uint8(0))
	f.Add(strings.Repeat("a-", 30), "a--b", "-a", uint8(1))
	f.Add("a", "a", "a", uint8(200))
	f.Add("", "x"+strings.Repeat("y", 60), "ünïcode", uint8(3))
	f.Fuzz(func(t *testing.T, a, b, c string, many uint8) {
		in := []string{a, b, c, a + b, b + "-" + c}
		// Pad with distinct well-formed names, to get past MaxNames and past the scan limit.
		for i := 0; i < int(many)*3; i++ {
			in = append(in, "pad-"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('a'+(i/26)%26)))
		}
		out := Clean(in)
		if len(out) > MaxNames {
			t.Fatalf("%d names kept, at most %d", len(out), MaxNames)
		}
		if !slices.IsSorted(out) {
			t.Fatalf("not sorted: %q", out)
		}
		seen := map[string]bool{}
		for _, n := range out {
			if !Wellformed(n) || len(n) > MaxNameLen {
				t.Fatalf("%q kept but is not a name", n)
			}
			if seen[n] {
				t.Fatalf("%q kept twice", n)
			}
			seen[n] = true
			found := false
			for _, i := range in {
				if i == n || len(i) > MaxNameLen && i[:MaxNameLen] == n {
					found = true
				}
			}
			if !found {
				t.Fatalf("%q was not in the list", n)
			}
		}
		if again := Clean(out); !slices.Equal(again, out) {
			t.Fatalf("cleaning twice changed the result: %q then %q", out, again)
		}
	})
}
