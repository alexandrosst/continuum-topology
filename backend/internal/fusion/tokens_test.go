package fusion

import (
	"strings"
	"testing"
)

func TestScopeListsAreCleanedAndBounded(t *testing.T) {
	got, err := cleanScopeList("namespace", []string{" payments ", "payments", "", "kube-system"}, namespaceRe)
	if err != nil || len(got) != 2 || got[0] != "payments" || got[1] != "kube-system" {
		t.Fatalf("trimmed and deduplicated: %v %v", got, err)
	}
	// No list is no limit; a list that cleans down to nothing is a mistake, not "every namespace".
	if got, err := cleanScopeList("namespace", nil, namespaceRe); err != nil || got != nil {
		t.Fatalf("an absent list: %v %v", got, err)
	}
	if _, err := cleanScopeList("namespace", []string{" ", ""}, namespaceRe); err == nil || !strings.Contains(err.Error(), "at least one") {
		t.Fatalf("a list of nothing: %v", err)
	}
	if _, err := cleanScopeList("namespace", []string{"not valid!"}, namespaceRe); err == nil {
		t.Fatal("a name that is not a namespace was accepted")
	}
	many := make([]string, maxScopeEntries+1)
	for i := range many {
		many[i] = "ns" + strings.Repeat("a", i%5) + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	if _, err := cleanScopeList("namespace", many, namespaceRe); err == nil {
		t.Fatal("more than the cap was accepted")
	}
}
