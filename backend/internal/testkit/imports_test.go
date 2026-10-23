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
	"ikhnos/gen/ikhnosv1":          true,
	"ikhnos/internal/accounts":     true,
	"ikhnos/internal/agents":       true,
	"ikhnos/internal/approval":     true,
	"ikhnos/internal/connectivity": true,
	"ikhnos/internal/facts":        true,
	"ikhnos/internal/graph":        true,
	"ikhnos/internal/kernel":       true,
	"ikhnos/internal/observed":     true,
	"ikhnos/internal/pki":          true,
	"ikhnos/internal/store":        true,
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
			if strings.HasPrefix(p, "ikhnos/") && !allowedRepoImports[p] {
				t.Errorf("%s imports %s: the test kit may not stand on it", f, p)
			}
		}
	}
}
