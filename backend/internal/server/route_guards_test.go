package server

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every HTTP route the server registers, with the guard it is registered under, is written down in testdata/route_guards.golden.
// A route that is added without a guard, one whose guard is weakened, and one that is removed, all change that file, so each shows
// in review as a line that was not there before. This is the pin for the route table; when routes are later registered through one
// typed registry, this test keeps passing unchanged and then retires.
//
// A route is read from the source, because http.ServeMux cannot be asked what it holds. Two forms are recognised:
//
//	route("GET "+o+"/members", memberRole, a.listMembers)   -> guarded, at the level named
//	api.HandleFunc("POST /api/v1/auth/login", a.login)      -> bare: no session guard at all
//
// A bare route is not necessarily a defect (sign-in has no session to check), but each one is listed so that a new one is a decision.
//
// To accept a change on purpose: go test ./internal/server -run TestEveryRouteAndItsGuardIsPinned -update
func TestEveryRouteAndItsGuardIsPinned(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	fset := token.NewFileSet()
	var out bytes.Buffer
	guarded, bare := 0, 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		src := func(n ast.Node) string {
			var b bytes.Buffer
			if err := printer.Fprint(&b, fset, n); err != nil {
				t.Fatal(err)
			}
			return b.String()
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fn := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name == "route" && len(call.Args) == 3 {
					out.WriteString(name + "\t" + src(call.Args[0]) + "\t" + src(call.Args[1]) + "\n")
					guarded++
				}
			case *ast.SelectorExpr:
				recv, ok := fn.X.(*ast.Ident)
				if !ok || (fn.Sel.Name != "Handle" && fn.Sel.Name != "HandleFunc") || (recv.Name != "mux" && recv.Name != "api") || len(call.Args) != 2 {
					return true
				}
				if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "pattern" {
					return true // the route helper's own registration, which is what puts the guard in front of every route() call
				}
				// What stands in front of the handler, when it is a wrapper call (a.fusionData(h)), is part of the pin: it is the guard.
				by := "-"
				if w, ok := call.Args[1].(*ast.CallExpr); ok {
					by = src(w.Fun)
				}
				out.WriteString(name + "\t" + src(call.Args[0]) + "\tBARE " + recv.Name + "." + fn.Sel.Name + " via " + by + "\n")
				bare++
			}
			return true
		})
	}
	if guarded < 100 || bare < 10 {
		t.Fatalf("read %d guarded and %d bare routes: the reader no longer finds the route table", guarded, bare)
	}
	const golden = "testdata/route_guards.golden"
	if *updateGolden {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if !bytes.Equal(want, out.Bytes()) {
		t.Errorf("the routes or their guards changed. The difference, as lines only in one side:\n%s\nIf this is intended, run: go test ./internal/server -run TestEveryRouteAndItsGuardIsPinned -update", lineDiff(string(want), out.String()))
	}
}

// lineDiff lists the lines found in only one of the two texts, "-" for the golden file and "+" for what the source now says.
func lineDiff(want, got string) string {
	count := func(s string) map[string]int {
		m := map[string]int{}
		for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
			m[l]++
		}
		return m
	}
	w, g := count(want), count(got)
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(want, "\n"), "\n") {
		if g[l] < w[l] {
			b.WriteString("- " + l + "\n")
			w[l]--
		}
	}
	for _, l := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
		if w[l] < g[l] {
			b.WriteString("+ " + l + "\n")
			g[l]--
		}
	}
	return b.String()
}
