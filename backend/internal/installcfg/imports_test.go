package installcfg

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The install settings stand on the kernel (the image configuration), the charts and the command writers, and on nothing else of this
// repository: not the server, and not a module. They are the server's configuration, which every module that prints a command is given.
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/kernel":  true,
	"ikhnos/internal/chart":   true,
	"ikhnos/internal/install": true,
}

func TestInstallcfgImportsOnlyTheKernelTheChartsAndTheCommandWriters(t *testing.T) {
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
				t.Errorf("%s imports %s: installcfg may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
