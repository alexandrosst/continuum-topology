package server

import (
	"context"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/agents"
	"ikhnos/internal/httpkit"
)

// What the server lets a role do is decided in policy.go. These pin the answers - who may give or take away which role, who may
// widen an agent's access, and what each level of the route guard means - for every role, including ones that do not exist.

var allRoles = []string{accounts.RoleViewer, accounts.RoleEditor, accounts.RoleAdmin, accounts.RoleOwner, "", "root", "Owner"}

func TestWhoMayGiveOrTakeAwayWhichRole(t *testing.T) {
	for _, by := range allRoles {
		for _, target := range allRoles {
			want := false
			switch by {
			case accounts.RoleOwner:
				want = target == accounts.RoleViewer || target == accounts.RoleEditor || target == accounts.RoleAdmin || target == accounts.RoleOwner
			case accounts.RoleAdmin:
				want = target == accounts.RoleEditor || target == accounts.RoleViewer
			}
			if got := mayGrantRole(by, target); got != want {
				t.Errorf("%q giving %q: %v, want %v", by, target, got, want)
			}
		}
	}
}

func TestOnlyAnAdministratorOrAboveMayWidenAnAgentsAccess(t *testing.T) {
	for _, role := range allRoles {
		want := role == accounts.RoleAdmin || role == accounts.RoleOwner
		if got := mayWidenAccess(agents.ActingAs(context.Background(), role)); got != want {
			t.Errorf("%q widening: %v, want %v", role, got, want)
		}
	}
	// A context that carries no caller at all is not an administrator.
	if mayWidenAccess(context.Background()) {
		t.Error("a context with no caller may widen")
	}
}

// The order of the roles, weakest first, stated here again so that a change to the one the code holds fails this test.
var pinnedRank = map[string]int{accounts.RoleViewer: 1, accounts.RoleEditor: 2, accounts.RoleAdmin: 3, accounts.RoleOwner: 4}

func TestEachLevelOfTheRouteGuardAdmitsTheRolesAtOrAboveIt(t *testing.T) {
	for level, min := range map[httpkit.Need]string{httpkit.MemberRole: accounts.RoleViewer, httpkit.EditorRole: accounts.RoleEditor, httpkit.AdminRole: accounts.RoleAdmin, httpkit.OwnerRole: accounts.RoleOwner} {
		for _, role := range allRoles {
			want := pinnedRank[role] >= pinnedRank[min]
			if got := roleAdmitted(role, level); got != want {
				t.Errorf("%q at %s: %v, want %v", role, level, got, want)
			}
		}
	}
}

// The adapters below are the only place these tests name the code under test.
func mayGrantRole(by, target string) bool {
	return accounts.Decide(accounts.Standing(by), accounts.ActGrantRole, accounts.Resource{Role: target}).Allowed
}
func mayWidenAccess(ctx context.Context) bool {
	return accounts.Decide(accounts.CallerFrom(ctx), accounts.ActChangeAccess, accounts.Resource{Widening: true}).Allowed
}
func roleAdmitted(role string, n httpkit.Need) bool {
	return accounts.Allows(accounts.Standing(role), n.Action())
}
