package accountsapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The accounts' routes stand on the accounts, the HTTP kit they are written with, the kernel and the store, and on nothing else of this
// repository: not the server, and not another module. What only the server knows is handed in as the Edge.
var allowedRepoImports = map[string]bool{
	"continuum/internal/kernel":   true,
	"continuum/internal/accounts": true,
	"continuum/internal/httpkit":  true,
	"continuum/internal/store":    true,
}

func TestTheAccountsAPIImportsOnlyTheKernelTheAccountsTheKitAndTheStore(t *testing.T) {
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
				t.Errorf("%s imports %s: the accounts API may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
