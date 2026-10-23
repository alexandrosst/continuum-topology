package fusionpages

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The rules of the gateway stand on the kernel (the secrets of a ticket) and on nothing else of this repository: they are rules, and the
// cookies, the origin check, the limiters and the proxy that apply them are the server's.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/kernel": true,
}

func TestFusionpagesImportsNothingButTheKernel(t *testing.T) {
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
				t.Errorf("%s imports %s: fusionpages may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
