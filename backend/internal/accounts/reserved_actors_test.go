package accounts

import (
	"strings"
	"testing"
)

func TestReservedActorWordsCannotBeUsernames(t *testing.T) {
	for _, name := range []string{"system", "anonymous", "renewal", "System", "ANONYMOUS", "Renewal", "sYsTeM"} {
		if usernameProblem(name) == "" {
			t.Errorf("%q can be a username", name)
		}
	}
	for _, name := range []string{"alice", "sysadmin", "system1", "anon", "systemd", "renewals", "a.b-c_d@e"} {
		if p := usernameProblem(name); p != "" {
			t.Errorf("%q was refused: %s", name, p)
		}
	}
	for _, name := range []string{"", "ab", "-no", "has space", "agent:ag-1", strings.Repeat("a", 65)} {
		if usernameProblem(name) == "" {
			t.Errorf("%q should not be a username", name)
		}
	}
}
