package httpkit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The HTTP kit stands on the kernel and on the accounts' vocabulary of who is calling (the leaf every module already stands on), and on
// nothing else of this repository: not the server, and not another module. What a handler needs from a module it is given, so that the
// line between them is the compiler's and not a habit.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/kernel":   true,
	"ikhnos/internal/accounts": true,
}

func TestTheKitImportsOnlyTheKernelAndTheAccounts(t *testing.T) {
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
				t.Errorf("%s imports %s: the kit may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
