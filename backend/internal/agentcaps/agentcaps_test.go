package agentcaps

import (
	"sort"
	"strings"
	"testing"
)

func TestEveryNameInTheRegistryIsWellformedAndDescribed(t *testing.T) {
	names := Names()
	if len(names) != 6 {
		t.Fatalf("%d names, want the six of the first protocol version: %v", len(names), names)
	}
	for _, n := range names {
		if !Wellformed(n) {
			t.Errorf("%q is in the registry and is not well formed", n)
		}
		d, ok := Describe(n)
		if !ok || strings.TrimSpace(d) == "" {
			t.Errorf("%q has no description", n)
		}
	}
}

func TestNamesAreSortedAndUnique(t *testing.T) {
	names := Names()
	if !sort.StringsAreSorted(names) {
		t.Fatalf("not sorted: %v", names)
	}
	for i := 1; i < len(names); i++ {
		if names[i] == names[i-1] {
			t.Fatalf("%q twice", names[i])
		}
	}
}

func TestTheConstantsAreTheRegistryKeys(t *testing.T) {
	for _, n := range []string{ConfigDigest, Diagnostics, ReleaseInfo, SelfStats, LinkCapabilities, ChunkedSync} {
		if _, ok := Describe(n); !ok {
			t.Errorf("the constant %q is not in the registry", n)
		}
	}
}

func TestWhatAnAgentMayNotCallACapability(t *testing.T) {
	good := []string{"a", "z", "a9z", "config-digest", "x1", "a-b-c-d", strings.Repeat("a", MaxNameLen)}
	for _, n := range good {
		if !Wellformed(n) {
			t.Errorf("%q should be accepted", n)
		}
	}
	bad := []string{
		"", "-a", "a-", "a--b", "1a", "A", "Config-Digest", "a_b", "a b", "a.b", "é", "a\x00", "a\n",
		strings.Repeat("a", MaxNameLen+1),
	}
	for _, n := range bad {
		if Wellformed(n) {
			t.Errorf("%q should be refused", n)
		}
	}
}

func TestDescribeDoesNotInventNames(t *testing.T) {
	if d, ok := Describe("warp-drive"); ok || d != "" {
		t.Fatalf("an unknown name was described: %q", d)
	}
}

func TestCleanKeepsWhatIsWellformedOnceSortedAndBounded(t *testing.T) {
	got := Clean([]string{"self-stats", "config-digest", "self-stats", "Bad Name", "", "from-the-future", "a--b"})
	want := []string{"config-digest", "from-the-future", "self-stats"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := Clean(nil); len(got) != 0 {
		t.Fatalf("nothing in, %v out", got)
	}

	many := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		many = append(many, "n-"+strings.Repeat("a", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if got := Clean(many); len(got) != MaxNames {
		t.Fatalf("%d names kept of 100 distinct ones, want %d", len(got), MaxNames)
	}

	// The first MaxNames listed are the ones kept, not the first MaxNames alphabetically.
	late := []string{}
	for i := 0; i < MaxNames; i++ {
		late = append(late, "z"+strings.Repeat("a", i))
	}
	late = append(late, "aaa")
	for _, n := range Clean(late) {
		if n == "aaa" {
			t.Fatal("a name past the limit was kept because it sorts early")
		}
	}

	// Rubbish costs a bounded amount: nothing past maxScan entries is looked at.
	junk := make([]string, 0, maxScan+10)
	for i := 0; i < maxScan; i++ {
		junk = append(junk, "BAD")
	}
	junk = append(junk, "good-name")
	if got := Clean(junk); len(got) != 0 {
		t.Fatalf("a name after %d entries of rubbish was kept: %v", maxScan, got)
	}

	// A long name is cut, not refused, and what is left is judged as it stands.
	long := Clean([]string{strings.Repeat("a", 10000)})
	if len(long) != 1 || len(long[0]) != MaxNameLen {
		t.Fatalf("a 10000 character name became %v", long)
	}
	if got := Clean([]string{strings.Repeat("a", MaxNameLen-1) + "-" + "b"}); len(got) != 0 {
		t.Fatalf("a name cut to end in a hyphen was kept: %v", got)
	}
}
