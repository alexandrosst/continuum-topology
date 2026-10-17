package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecideFailsClosed(t *testing.T) {
	for _, role := range allRoles {
		if Decide(Standing(role), Action(0), Resource{}).Allowed || Decide(Standing(role), Action(99), Resource{}).Allowed {
			t.Errorf("%q was allowed an action that does not exist", role)
		}
	}
	for _, act := range []Action{ActRead, ActEdit, ActAdminister, ActOwn, ActGrantRole} {
		if Decide(Principal{}, act, Resource{Role: RoleViewer}).Allowed {
			t.Errorf("a caller with no role was allowed action %d", act)
		}
	}
	// A role the server has never heard of is no one: not even a reader.
	for _, role := range []string{"root", "Owner", "ADMIN"} {
		if Allows(Standing(role), ActRead) {
			t.Errorf("%q may read", role)
		}
	}
}

func TestEachActionAdmitsExactlyTheRolesAtOrAboveItsLevel(t *testing.T) {
	want := map[Action][]string{
		ActRead:       {RoleViewer, RoleEditor, RoleAdmin, RoleOwner},
		ActEdit:       {RoleEditor, RoleAdmin, RoleOwner},
		ActAdminister: {RoleAdmin, RoleOwner},
		ActOwn:        {RoleOwner},
	}
	for act, roles := range want {
		for _, role := range allRoles {
			in := false
			for _, r := range roles {
				in = in || r == role
			}
			if got := Allows(Standing(role), act); got != in {
				t.Errorf("%q for action %d: %v, want %v", role, act, got, in)
			}
		}
	}
}

func TestADecisionAboutAccessSaysWhetherTheChangeWidens(t *testing.T) {
	for _, role := range allRoles {
		narrow := Decide(Standing(role), ActChangeAccess, Resource{Widening: false})
		if !narrow.Allowed || narrow.Widens {
			t.Errorf("%q narrowing: %+v, want allowed and not widening", role, narrow)
		}
		wide := Decide(Standing(role), ActChangeAccess, Resource{Widening: true})
		if !wide.Widens || wide.Allowed != (role == RoleAdmin || role == RoleOwner) {
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
