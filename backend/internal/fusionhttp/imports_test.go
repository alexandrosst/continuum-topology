package fusionhttp

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The routes' common plumbing stands on the kernel, on accounts (who a person is), on FUSION's control and its tokens, on the data API's
// client library and on the HTTP kit, and on nothing else of this repository: not the server, and not another module.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/accounts":  true,
	"ikhnos/internal/fusion":    true,
	"ikhnos/internal/fusionapi": true,
	"ikhnos/internal/httpkit":   true,
	"ikhnos/internal/kernel":    true,
	"ikhnos/internal/wire":      true,
	"ikhnos/internal/store":     true,
}

func TestFusionhttpImportsNoOtherModuleOfTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: fusionhttp may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
