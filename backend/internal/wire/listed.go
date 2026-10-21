package wire

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

// RequireWordsComplete fails the test of a package when the constants it declares of a words type (see Words) are not the words the
// type lists: a constant that is not listed would be written by the server and missing from the page's union, and a word that has no
// constant would be a string literal somewhere, which nothing ties to the list. dir is the package's directory (the test's working
// directory) and each argument is a value of one of its words types.
func RequireWordsComplete(t *testing.T, dir string, types ...Words) {
	t.Helper()
	problems, err := wordsProblems(dir, types)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

func wordsProblems(dir string, types []Words) ([]string, error) {
	var out []string
	files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
	fset := token.NewFileSet()
	consts := map[string]map[string]string{} // type name → word → the constant
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, d := range af.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, sp := range gd.Specs {
				vs := sp.(*ast.ValueSpec)
				id, ok := vs.Type.(*ast.Ident)
				if !ok {
					continue
				}
				for i, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING || i >= len(vs.Names) {
						continue
					}
					w, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, err
					}
					if consts[id.Name] == nil {
						consts[id.Name] = map[string]string{}
					}
					consts[id.Name][w] = vs.Names[i].Name
				}
			}
		}
	}
	for _, w := range types {
		name := reflect.TypeOf(w).Name()
		listed := map[string]bool{}
		for _, word := range w.WireWords() {
			listed[word] = true
			if consts[name][word] == "" {
				out = append(out, fmt.Sprintf("%s lists %q, which no constant of %s holds: declare it, so the code that writes it cannot misspell it", name, word, name))
			}
		}
		for word, c := range consts[name] {
			if !listed[word] {
				out = append(out, fmt.Sprintf("%s is a %s but %q is not among its words: add it, or the page's union does not have it", c, name, word))
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
