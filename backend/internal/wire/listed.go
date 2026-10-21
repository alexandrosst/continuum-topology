package wire

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// RequireDocsListed fails the test of a package for every struct it declares whose name ends in Doc, Request or Response and that is not
// among the roots, or reachable from one: a document written for the wire and left out of the list is a shape the page would write by
// hand again. dir is the package's directory (the test's working directory); roots are what its WireTypes returns.
func RequireDocsListed(t *testing.T, dir string, roots []Root) {
	t.Helper()
	listed := map[string]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Array || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt == timeType || custom(rt) {
			return
		}
		if rt.Name() != "" {
			key := rt.PkgPath() + "." + rt.Name()
			if listed[key] {
				return
			}
			listed[key] = true
		}
		for _, f := range fields(rt) {
			walk(f.typ)
		}
	}
	for _, r := range roots {
		walk(reflect.TypeOf(r.Value))
	}
	abs, _ := filepath.Abs(dir)
	suffix := "/" + filepath.Base(abs) + "."
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts := sp.(*ast.TypeSpec)
				if _, isStruct := ts.Type.(*ast.StructType); !isStruct {
					continue
				}
				n := ts.Name.Name
				if !(strings.HasSuffix(n, "Doc") || strings.HasSuffix(n, "Request") || strings.HasSuffix(n, "Response")) {
					continue
				}
				if !hasKeySuffix(listed, suffix+n) {
					t.Errorf("%s declares %s, which WireTypes does not list: add it, so the page is given its shape", filepath.Base(f), n)
				}
			}
		}
	}
}

func hasKeySuffix(m map[string]bool, suffix string) bool {
	for k := range m {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}
