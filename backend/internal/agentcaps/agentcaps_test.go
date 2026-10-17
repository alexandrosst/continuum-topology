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
