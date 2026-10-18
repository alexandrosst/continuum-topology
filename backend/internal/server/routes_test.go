package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/testkit"
)

// Every route the server mounts, and what it asks of the caller, is written down in testdata/routes.txt, one line each: the access
// (public, a signed-in session, a role in the organisation, a credential of its own) and the pattern. Routes are mounted only
// through Admin.handle (routes.go), which records them, so this is the server's own list and not a reading of its source. A route
// added without a guard, one whose guard is weakened, and one removed all change that file, so each shows in review as a line that
// was not there before.
//
// To accept a change on purpose: go test ./internal/server -run TestEveryRouteAndItsAccessIsPinned -update
func routeTable(t *testing.T) string {
	t.Helper()
	a := newAdminRig(t)
	var lines []string
	for _, r := range a.a.routes {
		lines = append(lines, r.Access+"\t"+r.Pattern)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n") + "\n"
}

func TestEveryRouteAndItsAccessIsPinned(t *testing.T) {
	got := routeTable(t)
	if n := strings.Count(got, "\n"); n < 150 {
		t.Fatalf("the server mounted %d routes: the registry no longer sees them", n)
	}
	const golden = "testdata/routes.txt"
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if string(want) != got {
		t.Errorf("the routes or what they ask of the caller changed. The difference, as lines only in one side:\n%s\nIf this is intended, run: go test ./internal/server -run TestEveryRouteAndItsAccessIsPinned -update", testkit.LineDiff(string(want), got))
	}
}

// A route mounted straight on a mux would not be in the table. This reads the package's own source: nothing outside routes.go
// calls Handle or HandleFunc on the mux or api muxes. (The metrics listener in metrics.go is a separate server with its own
// bearer token and its own mux.)
func TestNoRouteIsMountedOutsideTheRegistry(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 20 {
		t.Fatalf("source files not found: %v %d", err, len(files))
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "routes.go" || f == "metrics.go" {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
				return true
			}
			if recv, ok := sel.X.(*ast.Ident); ok && (recv.Name == "mux" || recv.Name == "api") {
				t.Errorf("%s: a route is mounted on %s directly; mount it with a.handle so it is in the table", fset.Position(call.Pos()), recv.Name)
			}
			return true
		})
	}
}

// The table says what each route asks; this checks that the real guard does it. For every route that asks for a role, someone with
// the next role down is turned away with the role message, and with no session every guarded route answers 401, before any
// handler runs. Routes at the exact role are called with GET only (a GET changes nothing), and must get past the role rule.
func TestEachGuardedRouteTurnsAwayWhoIsBelowItsLevel(t *testing.T) {
	a := newAdminRig(t)
	cookie := map[string]string{}
	for _, role := range []string{accounts.RoleViewer, accounts.RoleEditor, accounts.RoleAdmin, accounts.RoleOwner} {
		_, cookie[role] = a.user(t, "u-"+role, role)
	}
	below := map[string]string{"editor": accounts.RoleViewer, "admin": accounts.RoleEditor, "owner": accounts.RoleAdmin}
	exact := map[string]string{"member": accounts.RoleViewer, "editor": accounts.RoleEditor, "admin": accounts.RoleAdmin, "owner": accounts.RoleOwner}
	const roleRefusal = "does not allow this"
	checked := 0
	for _, r := range a.a.routes {
		method, pattern, _ := strings.Cut(r.Pattern, " ")
		path := strings.ReplaceAll(pattern, "{org}", "org-1")
		for strings.Contains(path, "{") {
			i, j := strings.Index(path, "{"), strings.Index(path, "}")
			path = path[:i] + "x" + path[j+1:]
		}
		switch r.Access {
		case "member", "editor", "admin", "owner", "session", "settled":
			// The sign-in throttle counts failures per address, so each request comes from its own.
			if c := a.do(method, path, nil, fromIP(fmt.Sprintf("10.9.%d.%d", checked/250, checked%250+1))).Code; c != 401 {
				t.Errorf("%s with no session: %d, want 401", r.Pattern, c)
			}
			checked++
		}
		if role, ok := below[r.Access]; ok {
			resp := a.do(method, path, nil, withCookie(cookie[role]))
			if resp.Code != 403 || !strings.Contains(resp.Body.String(), roleRefusal) {
				t.Errorf("%s as %s: %d %s, want the role refusal", r.Pattern, role, resp.Code, strings.TrimSpace(resp.Body.String()))
			}
		}
		if role, ok := exact[r.Access]; ok && method == "GET" {
			if resp := a.do(method, path, nil, withCookie(cookie[role])); strings.Contains(resp.Body.String(), roleRefusal) {
				t.Errorf("%s as %s was turned away by the role rule: %s", r.Pattern, role, strings.TrimSpace(resp.Body.String()))
			}
		}
	}
	if checked < 90 {
		t.Fatalf("only %d guarded routes were checked", checked)
	}
}
