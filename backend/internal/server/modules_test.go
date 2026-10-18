package server

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

// The server is one Go package that is to be cut into modules (see docs/server-modules.md). Until it is, nothing in the compiler
// stops one part from reaching into another's internals, and thirty thousand lines have been written that way. These tests draw the
// line without moving any code:
//
//   - testdata/modules.txt says which module every source file belongs to, and a file that is in no module, or in two, fails.
//   - testdata/module_edges.txt lists every place one module uses something that is declared in another (a function, a type, a
//     field of a struct, a method). It is read from the type-checked source, not from the names. The list may only get shorter: a
//     use that is not in it fails, and so does a line for a use that is gone, so each module moved out leaves the list smaller and
//     a new reach across a boundary shows in review as a line that was not there before.
//   - testdata/module_store.txt lists the store methods that more than one module calls, by module: what two modules share in the
//     database, which is also what has to be settled before either can move.
//
// Every module may use the kernel, and the shell may use every module, so those two are not listed. What is listed is everything
// else, including a module using the shell, which is a cut still to be made. The kernel is already a package (internal/kernel), so
// that it uses no module is the compiler's to say; the one file of it here is the bridge (kernel_bridge.go).
//
// To accept a change on purpose: go test ./internal/server -run 'TestModule' -update

const (
	modulesFile     = "testdata/modules.txt"
	moduleEdgesFile = "testdata/module_edges.txt"
	moduleStoreFile = "testdata/module_store.txt"
	storePackage    = "continuum/internal/store"
	serverPackage   = "continuum/internal/server"
)

var moduleNames = map[string]bool{
	"kernel": true, "shell": true, "accounts": true, "agents": true, "estate": true,
	"telemetry": true, "operators": true, "connectivity": true, "fusion": true, "advice": true,
}

// movedOut are the modules that are packages of their own now. They keep their name here for the lists, but no file of this package is
// theirs: a file assigned to one of them is a file that should have moved with it.
var movedOut = map[string]bool{"accounts": true, "advice": true, "operators": true, "telemetry": true}

// moduleOfFile reads testdata/modules.txt: one line per file, the file name and then its module.
func moduleOfFile(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(modulesFile)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("%s: %q is not a file name and a module", modulesFile, line)
		}
		if !moduleNames[f[1]] {
			t.Fatalf("%s: %s is in %q, which is not a module", modulesFile, f[0], f[1])
		}
		if _, dup := m[f[0]]; dup {
			t.Fatalf("%s: %s is listed twice", modulesFile, f[0])
		}
		m[f[0]] = f[1]
	}
	return m
}

func TestEverySourceFileBelongsToOneModule(t *testing.T) {
	mod := moduleOfFile(t)
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".go") && !strings.HasSuffix(n, "_test.go") {
			onDisk[n] = true
			if _, ok := mod[n]; !ok {
				t.Errorf("%s is in no module: add it to %s", n, modulesFile)
			}
		}
	}
	for n := range mod {
		if !onDisk[n] {
			t.Errorf("%s is listed in %s and is not a source file here any more: remove the line", n, modulesFile)
		}
	}
	// A module with no file is a module that was meant to exist and does not: the cut has gone wrong.
	used := map[string]int{}
	for _, m := range mod {
		used[m]++
	}
	for name := range moduleNames {
		switch {
		case movedOut[name] && used[name] > 0:
			t.Errorf("module %s is a package of its own now, and %d file(s) here are still assigned to it", name, used[name])
		case !movedOut[name] && used[name] == 0:
			t.Errorf("module %s has no file", name)
		}
	}
}

// A use is one place where a file of one module names something declared in a file of another.
type moduleUse struct {
	from, to string // modules
	what     string // "func NewSecret", "field Hub.mu", "method Core.audit", "type Consent"
	file     string // the file that uses it, to say where in a failure
}

var (
	typedOnce sync.Once
	typedUses []moduleUse
	typedErr  error
	typedStor []moduleUse // store methods used: from is the module, what is the method, to is "store"
)

// typedServer type-checks this package from its source and reads off every use. The compiler's export data for the imports comes
// from `go list -export`, so no module outside the standard library is needed.
func typedServer(t *testing.T) ([]moduleUse, []moduleUse) {
	t.Helper()
	if testing.Short() {
		t.Skip("type-checks the whole package")
	}
	typedOnce.Do(func() { typedUses, typedStor, typedErr = readUses(moduleOfFile(t)) })
	if typedErr != nil {
		t.Fatal(typedErr)
	}
	return typedUses, typedStor
}

func readUses(modOf map[string]string) (pkgUses, storeUses []moduleUse, err error) {
	type listed struct {
		ImportPath string
		Export     string
		GoFiles    []string
		Dir        string
	}
	out, err := exec.Command("go", "list", "-export", "-deps", "-json=ImportPath,Export,GoFiles,Dir", ".").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, nil, fmt.Errorf("go list: %v\n%s", err, ee.Stderr)
		}
		return nil, nil, fmt.Errorf("go list: %v", err)
	}
	export := map[string]string{}
	var self listed
	for dec := json.NewDecoder(strings.NewReader(string(out))); ; {
		var p listed
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, err
		}
		export[p.ImportPath] = p.Export
		if p.ImportPath == serverPackage {
			self = p
		}
	}
	if len(self.GoFiles) == 0 {
		return nil, nil, fmt.Errorf("go list did not find %s", serverPackage)
	}

	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range self.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(self.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, nil, err
		}
		files = append(files, f)
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		p, ok := export[path]
		if !ok || p == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(p)
	})
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	var first error
	conf := types.Config{Importer: imp, Error: func(e error) {
		if first == nil {
			first = e
		}
	}}
	pkg, _ := conf.Check(serverPackage, fset, files, info)
	if first != nil {
		return nil, nil, first
	}

	// The struct a field belongs to, for every field of a type declared at the top of a file.
	owner := map[types.Object]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				ts := sp.(*ast.TypeSpec)
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, fld := range st.Fields.List {
					if len(fld.Names) == 0 {
						if id := embeddedName(fld.Type); id != nil {
							if o := info.Defs[id]; o != nil {
								owner[o] = ts.Name.Name
							}
						}
						continue
					}
					for _, n := range fld.Names {
						if o := info.Defs[n]; o != nil {
							owner[o] = ts.Name.Name
						}
					}
				}
			}
		}
	}

	for _, f := range files {
		fromFile := filepath.Base(fset.Position(f.Pos()).Filename)
		from := modOf[fromFile]
		ast.Inspect(f, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			obj := info.Uses[id]
			if obj == nil || obj.Pkg() == nil {
				return true
			}
			if obj.Pkg().Path() == storePackage {
				if fn, ok := obj.(*types.Func); ok {
					if sig := fn.Type().(*types.Signature); sig.Recv() != nil && recvName(sig) == "Store" {
						storeUses = append(storeUses, moduleUse{from: from, to: "store", what: fn.Name(), file: fromFile})
					}
				}
				return true
			}
			if obj.Pkg() != pkg {
				return true
			}
			what := ""
			switch o := obj.(type) {
			case *types.Func:
				o = o.Origin()
				obj = o
				if sig := o.Type().(*types.Signature); sig.Recv() != nil {
					what = "method " + recvName(sig) + "." + o.Name()
				} else {
					what = "func " + o.Name()
				}
			case *types.TypeName:
				if o.Parent() != pkg.Scope() {
					return true
				}
				what = "type " + o.Name()
			case *types.Var:
				o = o.Origin()
				obj = o
				if o.IsField() {
					ow, ok := owner[o]
					if !ok {
						return true // a field of a struct written inline in a function
					}
					what = "field " + ow + "." + o.Name()
				} else if o.Parent() == pkg.Scope() {
					what = "var " + o.Name()
				} else {
					return true
				}
			case *types.Const:
				if o.Parent() != pkg.Scope() {
					return true
				}
				what = "const " + o.Name()
			default:
				return true
			}
			toFile := filepath.Base(fset.Position(obj.Pos()).Filename)
			to, ok := modOf[toFile]
			if !ok || to == from {
				return true
			}
			pkgUses = append(pkgUses, moduleUse{from: from, to: to, what: what, file: fromFile})
			return true
		})
	}
	return pkgUses, storeUses, nil
}

func embeddedName(e ast.Expr) *ast.Ident {
	switch x := e.(type) {
	case *ast.Ident:
		return x
	case *ast.StarExpr:
		return embeddedName(x.X)
	case *ast.SelectorExpr:
		return x.Sel
	case *ast.IndexExpr:
		return embeddedName(x.X)
	case *ast.IndexListExpr:
		return embeddedName(x.X)
	}
	return nil
}

func recvName(sig *types.Signature) string {
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	if n, ok := t.(*types.Named); ok {
		return n.Obj().Name()
	}
	return t.String()
}

// inEdgeList says whether a use is one of the lines the files hold: every module may use the kernel and the shell may use any module.
func inEdgeList(u moduleUse) bool { return u.to != "kernel" && u.from != "shell" }

const edgesHeader = `# Every place one module uses something declared in another, as "using module > declaring module", then what is used.
# Not listed: any module using the kernel, and the shell using any module (see modules_test.go). The list may only get shorter.
# Regenerate on purpose with: go test ./internal/server -run TestModule -update
`

const storeHeader = `# The store methods that more than one module calls, and the modules that call them. A method used by one module is that
# module's own and is not listed. The list may only get shorter: moving a module out settles what it shares.
# Regenerate on purpose with: go test ./internal/server -run TestModule -update
`

func edgeLines(uses []moduleUse) (lines []string, example map[string]string) {
	example = map[string]string{}
	seen := map[string]bool{}
	for _, u := range uses {
		if !inEdgeList(u) {
			continue
		}
		l := u.from + " > " + u.to + "\t" + u.what
		if !seen[l] {
			seen[l] = true
			lines = append(lines, l)
			example[l] = u.file
		}
	}
	sort.Strings(lines)
	return lines, example
}

func storeLines(uses []moduleUse) []string {
	by := map[string]map[string]bool{}
	for _, u := range uses {
		if by[u.what] == nil {
			by[u.what] = map[string]bool{}
		}
		by[u.what][u.from] = true
	}
	var lines []string
	for m, mods := range by {
		if len(mods) < 2 {
			continue
		}
		for from := range mods {
			lines = append(lines, m+"\t"+from)
		}
	}
	sort.Strings(lines)
	return lines
}

// compareList checks got against the golden file: what is only in got is new, what is only in the file is gone.
func compareList(t *testing.T, golden, header, newThing string, got []string, example map[string]string) {
	t.Helper()
	text := header + strings.Join(got, "\n") + "\n"
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	want := map[string]bool{}
	for _, l := range strings.Split(string(raw), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			want[l] = true
		}
	}
	have := map[string]bool{}
	var added, gone []string
	for _, l := range got {
		have[l] = true
		if !want[l] {
			if e := example[l]; e != "" {
				l += "   (in " + e + ")"
			}
			added = append(added, l)
		}
	}
	for l := range want {
		if !have[l] {
			gone = append(gone, l)
		}
	}
	sort.Strings(gone)
	if len(added) > 0 {
		t.Errorf("%d %s that %s does not list. %s", len(added), newThing, golden, strings.Join(append([]string{"If this is a step of the move, accept it with -update; if not, it is a reach across a boundary:"}, added...), "\n  "))
	}
	if len(gone) > 0 {
		t.Errorf("%d line(s) of %s are no longer true; the boundary got cleaner. Remove them with -update:\n  %s",
			len(gone), golden, strings.Join(gone, "\n  "))
	}
}

func TestModulesReachIntoEachOtherOnlyWhereTheListSays(t *testing.T) {
	uses, _ := typedServer(t)
	lines, example := edgeLines(uses)
	if len(lines) < 100 {
		t.Fatalf("only %d cross-module uses were read: the reading has stopped seeing them", len(lines))
	}
	compareList(t, moduleEdgesFile, edgesHeader, "use(s) across a module boundary", lines, example)
}

func TestModulesShareStoreMethodsOnlyWhereTheListSays(t *testing.T) {
	_, st := typedServer(t)
	if len(st) < 100 {
		t.Fatalf("only %d store calls were read: the reading has stopped seeing them", len(st))
	}
	compareList(t, moduleStoreFile, storeHeader, "store method(s) shared between modules", storeLines(st), map[string]string{})
}
