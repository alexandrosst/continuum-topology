package operatorsapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The operators' routes stand on the operators themselves and the vocabulary they are written in (the store's records, the kernel), on the
// install package and the chart that the commands are written with, on the agents' release naming, and on the HTTP kit. They do not import
// the server (the shell mounts them), FUSION (the central operator is the port operators.Central), the estate (the shell hands over which release an
// agent runs as) or the telemetry intents.
var allowedRepoImports = map[string]bool{
	"continuum/internal/agents":     true,
	"continuum/internal/chart":      true,
	"continuum/internal/httpkit":    true,
	"continuum/internal/install":    true,
	"continuum/internal/installcfg": true,
	"continuum/internal/kernel":     true,
	"continuum/internal/operators":  true,
	"continuum/internal/store":      true,
}

func TestTheOperatorsAPIImportsOnlyTheOperatorsTheInstallCommandsAndTheKit(t *testing.T) {
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
				t.Errorf("%s imports %s: the operators API may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
