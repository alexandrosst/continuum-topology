package server

import (
	"context"
	"testing"
)

// What the server lets a role do is decided in policy.go. These pin the answers - who may give or take away which role, who may
// widen an agent's access, and what each level of the route guard means - for every role, including ones that do not exist.

var allRoles = []string{RoleViewer, RoleEditor, RoleAdmin, RoleOwner, "", "root", "Owner"}

func TestWhoMayGiveOrTakeAwayWhichRole(t *testing.T) {
	for _, by := range allRoles {
		for _, target := range allRoles {
			want := false
			switch by {
			case RoleOwner:
				want = target == RoleViewer || target == RoleEditor || target == RoleAdmin || target == RoleOwner
			case RoleAdmin:
				want = target == RoleEditor || target == RoleViewer
			}
			if got := mayGrantRole(by, target); got != want {
				t.Errorf("%q giving %q: %v, want %v", by, target, got, want)
			}
		}
	}
}

func TestOnlyAnAdministratorOrAboveMayWidenAnAgentsAccess(t *testing.T) {
	for _, role := range allRoles {
		want := role == RoleAdmin || role == RoleOwner
		if got := mayWidenAccess(ActingAs(context.Background(), role)); got != want {
			t.Errorf("%q widening: %v, want %v", role, got, want)
		}
	}
	// A context that carries no caller at all is not an administrator.
	if mayWidenAccess(context.Background()) {
		t.Error("a context with no caller may widen")
	}
}

func TestEachLevelOfTheRouteGuardAdmitsTheRolesAtOrAboveIt(t *testing.T) {
	for level, min := range map[need]string{memberRole: RoleViewer, editorRole: RoleEditor, adminRole: RoleAdmin, ownerRole: RoleOwner} {
		for _, role := range allRoles {
			want := roleRank[role] >= roleRank[min]
			if got := roleAdmitted(role, level); got != want {
				t.Errorf("%q at %s: %v, want %v", role, level, got, want)
			}
		}
	}
}

// The adapters below are the only place these tests name the code under test.
func mayGrantRole(by, target string) bool {
	return Decide(Standing(by), ActGrantRole, Resource{Role: target}).Allowed
}
func mayWidenAccess(ctx context.Context) bool {
	return Decide(CallerFrom(ctx), ActChangeAccess, Resource{Widening: true}).Allowed
}
func roleAdmitted(role string, n need) bool { return Allows(Standing(role), n.action()) }
