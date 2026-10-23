package connectivity

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Connectivity stands on the kernel, the store, the agents (the consent that limits what is measured) and observed (what an address
// leads to), and on the vocabularies of the measurements, the links and the model. It does not import the server, so not the hub that
// keeps the live picture of it and decides when to push.
var allowedRepoImports = map[string]bool{
	"ikhnos/gen/ikhnosv1":       true,
	"ikhnos/internal/kernel":    true,
	"ikhnos/internal/store":     true,
	"ikhnos/internal/agents":    true,
	"ikhnos/internal/observed":  true,
	"ikhnos/internal/measure":   true,
	"ikhnos/internal/link":      true,
	"ikhnos/internal/model":     true,
	"ikhnos/internal/interpret": true,
}

func TestConnectivityImportsNothingOfTheServerOrAModuleAboveIt(t *testing.T) {
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
				t.Errorf("%s imports %s: connectivity may not stand on it", f, p)
			}
		}
	}
}
