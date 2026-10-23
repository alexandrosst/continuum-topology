package kernel

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The kernel is what every module stands on, so it may import nothing a module could be: of this repository's packages only the
// store, the CA, the sealer of its secrets and the two libraries its settings use (link and measure), none of which knows the server. A package that wants to be reached from the kernel is not under it. (The compiler already refuses a cycle with
// a package that imports the kernel; this refuses the import that would be allowed because that package does not yet.)
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/store":   true,
	"ikhnos/internal/pki":     true,
	"ikhnos/internal/link":    true,
	"ikhnos/internal/measure": true,
	"ikhnos/internal/sealing": true,
}

func TestTheKernelImportsNoModule(t *testing.T) {
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
			if strings.HasPrefix(p, "ikhnos/") && !allowedRepoImports[p] && !strings.HasPrefix(p, "ikhnos/gen/") {
				t.Errorf("%s imports %s: the kernel may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
