package operators

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Operators stand on the kernel, the store, the PKI their certificates are made with and accounts (the names the server acts under), and on nothing else of this
// repository: not the server, and not another module. FUSION manages the central operator and calls this package; this package is
// handed what it needs to know of that (ManagedStatus) and does not import it.
var allowedRepoImports = map[string]bool{
	"continuum/internal/kernel": true,
	"continuum/internal/store":  true,
	"continuum/internal/pki":    true,
	// accounts: the names the server acts under; continuumv1: the renewal call's messages.
	"continuum/internal/accounts": true,
	"continuum/gen/continuumv1":   true,
}

func TestOperatorsImportNoOtherModuleOfTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: operators may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
