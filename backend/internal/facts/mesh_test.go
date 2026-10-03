package facts

import "testing"

func TestPortExcludedMatchesASinglePort(t *testing.T) {
	entries := []string{"out:5432", "in:8080"}
	if !PortExcluded(entries, "out", 5432) {
		t.Fatal("expected out:5432 to match port 5432 in the out direction")
	}
	if PortExcluded(entries, "in", 5432) {
		t.Fatal("out:5432 must not match the in direction")
	}
	if PortExcluded(entries, "out", 5433) {
		t.Fatal("out:5432 must not match a different port")
	}
}

func TestPortExcludedMatchesARange(t *testing.T) {
	entries := []string{"in:8000-8100"}
	if !PortExcluded(entries, "in", 8000) || !PortExcluded(entries, "in", 8100) || !PortExcluded(entries, "in", 8050) {
		t.Fatal("expected 8000-8100 to match its own endpoints and everything between them")
	}
	if PortExcluded(entries, "in", 7999) || PortExcluded(entries, "in", 8101) {
		t.Fatal("expected 8000-8100 not to match just outside its bounds")
	}
}

func TestPortExcludedIgnoresMalformedEntries(t *testing.T) {
	entries := []string{"out:not-a-port", "sideways:1234", "out:", ""}
	if PortExcluded(entries, "out", 1234) {
		t.Fatal("a malformed entry must never match, not even by accident")
	}
}

func TestPortExcludedOnEmptyList(t *testing.T) {
	if PortExcluded(nil, "out", 5432) {
		t.Fatal("no entries at all means nothing is excluded")
	}
}
