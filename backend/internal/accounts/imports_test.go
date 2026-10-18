package accounts

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Accounts stands on the kernel and the store and on nothing else of this repository: not the server, and not another module. What it
// needs from another module it is given (a callback on the Core), so that the line between them is the compiler's and not a habit.
var allowedRepoImports = map[string]bool{
	"continuum/internal/kernel": true,
	"continuum/internal/store":  true,
}

func TestAccountsImportsOnlyTheKernelAndTheStore(t *testing.T) {
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
				t.Errorf("%s imports %s: accounts may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
