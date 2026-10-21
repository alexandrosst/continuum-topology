package decisionloop

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The decision loop stands on the kernel, on accounts (who may decide, and the names the server acts under), on the HTTP kit its routes are written with, on the store, and on the two
// libraries it computes with: the capacity advice and the twin it reads. It does not import the server or another module: what it needs
// of agents and of the estate it is given as a port (Access, Load), so that the line between them is the compiler's and not a habit.
var allowedRepoImports = map[string]bool{
	"continuum/internal/httpkit":  true,
	"continuum/internal/kernel":   true,
	"continuum/internal/wire":     true,
	"continuum/internal/accounts": true,
	"continuum/internal/store":    true,
	"continuum/internal/advice":   true,
	"continuum/internal/twin":     true,
}

func TestTheDecisionLoopImportsNoOtherModuleOfTheServer(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		af, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, im := range af.Imports {
			p, _ := strconv.Unquote(im.Path.Value)
			if strings.HasPrefix(p, "continuum/") && !allowedRepoImports[p] {
				t.Errorf("%s imports %s: the decision loop may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
