package accounts

import "testing"

// The roles the page is told are the roles the policy ranks, weakest first, and the modes of registration are the ones regMode reads.
func TestTheWordsOfTheRolesAndOfRegistrationAreTheOnesTheCodeUses(t *testing.T) {
	words := Role("").WireWords()
	if len(words) != len(roleRank) {
		t.Errorf("%d words for %d ranked roles", len(words), len(roleRank))
	}
	last := 0
	for _, w := range words {
		if roleRank[w] <= last {
			t.Errorf("%q does not rank above the role before it", w)
		}
		last = roleRank[w]
		if !validRole(w) {
			t.Errorf("%q is a word of Role but not a valid role", w)
		}
	}
	if got := Registration("").WireWords(); len(got) != 3 || got[0] != RegOpen || got[1] != RegInvite || got[2] != RegClosed {
		t.Errorf("registration words = %q", got)
	}
}
