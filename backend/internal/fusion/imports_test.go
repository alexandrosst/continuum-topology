package fusion

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// FUSION's control stands on the kernel, the store, the PKI the gateway's certificates are made with, accounts (the names the server acts
// under), operators (the central operator is one of them) and the data API's client library, and on nothing else of this repository:
// not the server, and not another module. Operators do not import it.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/httpkit":   true,
	"ikhnos/internal/kernel":    true,
	"ikhnos/internal/wire":      true,
	"ikhnos/internal/store":     true,
	"ikhnos/internal/pki":       true,
	"ikhnos/internal/accounts":  true,
	"ikhnos/internal/operators": true,
	"ikhnos/internal/fusionapi": true,
}

func TestFusionImportsNoOtherModuleOfTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: fusion may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
