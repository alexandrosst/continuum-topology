package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"ikhnos/internal/accounts"
)

func TestDecideFailsClosed(t *testing.T) {
	for _, role := range allRoles {
		if accounts.Decide(accounts.Standing(role), accounts.Action(0), accounts.Resource{}).Allowed || accounts.Decide(accounts.Standing(role), accounts.Action(99), accounts.Resource{}).Allowed {
			t.Errorf("%q was allowed an action that does not exist", role)
		}
	}
	for _, act := range []accounts.Action{accounts.ActRead, accounts.ActEdit, accounts.ActAdminister, accounts.ActOwn, accounts.ActGrantRole} {
		if accounts.Decide(accounts.Principal{}, act, accounts.Resource{Role: accounts.RoleViewer}).Allowed {
			t.Errorf("a caller with no role was allowed action %d", act)
		}
	}
	// A role the server has never heard of is no one: not even a reader.
	for _, role := range []string{"root", "Owner", "ADMIN"} {
		if accounts.Allows(accounts.Standing(role), accounts.ActRead) {
			t.Errorf("%q may read", role)
		}
	}
}

func TestEachActionAdmitsExactlyTheRolesAtOrAboveItsLevel(t *testing.T) {
	want := map[accounts.Action][]string{
		accounts.ActRead:       {accounts.RoleViewer, accounts.RoleEditor, accounts.RoleAdmin, accounts.RoleOwner},
		accounts.ActEdit:       {accounts.RoleEditor, accounts.RoleAdmin, accounts.RoleOwner},
		accounts.ActAdminister: {accounts.RoleAdmin, accounts.RoleOwner},
		accounts.ActOwn:        {accounts.RoleOwner},
	}
	for act, roles := range want {
		for _, role := range allRoles {
			in := false
			for _, r := range roles {
				in = in || r == role
			}
			if got := accounts.Allows(accounts.Standing(role), act); got != in {
				t.Errorf("%q for action %d: %v, want %v", role, act, got, in)
			}
		}
	}
}

func TestADecisionAboutAccessSaysWhetherTheChangeWidens(t *testing.T) {
	for _, role := range allRoles {
		narrow := accounts.Decide(accounts.Standing(role), accounts.ActChangeAccess, accounts.Resource{Widening: false})
		if !narrow.Allowed || narrow.Widens {
			t.Errorf("%q narrowing: %+v, want allowed and not widening", role, narrow)
		}
		wide := accounts.Decide(accounts.Standing(role), accounts.ActChangeAccess, accounts.Resource{Widening: true})
		if !wide.Widens || wide.Allowed != (role == accounts.RoleAdmin || role == accounts.RoleOwner) {
			t.Errorf("%q widening: %+v", role, wide)
		}
	}
}

// How a role ranks against another is decided in policy.go: nothing else may read roleRank, so a place that compared roles by
// hand (the way eleven once did) fails here.
func TestRolesAreRankedInOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 20 {
		t.Fatalf("source files not found: %v %d", err, len(files))
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "policy.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "roleRank" {
				t.Errorf("%s: roles are ranked here; ask Decide or Allows in policy.go", fset.Position(id.Pos()))
			}
			return true
		})
	}
}
