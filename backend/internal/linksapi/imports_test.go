package linksapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The links API stands on the kernel, on connectivity (the links, their notes and their credentials), on the data API's client library and
// its HTTP plumbing (the caller of a data route), and on the HTTP kit, and on nothing else of this repository: not the server, not the
// hub (it is handed one), and not another module.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/connectivity": true,
	"ikhnos/internal/fusionapi":    true,
	"ikhnos/internal/fusionhttp":   true,
	"ikhnos/internal/httpkit":      true,
	"ikhnos/internal/kernel":       true,
	"ikhnos/internal/wire":         true,
}

func TestLinksapiImportsNoOtherModuleOfTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: linksapi may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
