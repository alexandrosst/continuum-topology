package testkit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The test kit may stand on the modules that hold the types the tests build (agents, connectivity, the store, the kernel) and on the
// vocabularies, but not on the estate or the server: both import it from their tests, so it would be a cycle.
var allowedRepoImports = map[string]bool{
	"continuum/gen/continuumv1":       true,
	"continuum/internal/accounts":     true,
	"continuum/internal/agents":       true,
	"continuum/internal/approval":     true,
	"continuum/internal/connectivity": true,
	"continuum/internal/facts":        true,
	"continuum/internal/graph":        true,
	"continuum/internal/kernel":       true,
	"continuum/internal/observed":     true,
	"continuum/internal/pki":          true,
	"continuum/internal/store":        true,
}

func TestTestkitImportsNothingOfTheEstateOrTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: the test kit may not stand on it", f, p)
			}
		}
	}
}
