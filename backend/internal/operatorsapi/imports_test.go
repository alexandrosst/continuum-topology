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
// install package and the chart that the commands are written with, on the agents' vocabulary (what an exporter reports) and the connectivity
// documents, on the telemetry intents that name an operator, and on the HTTP kit. They do not import the server (the shell mounts them), FUSION (the
// central operator is the port operators.Central) or the estate (the hub is the port SenderHub, and which release an agent runs as is handed over).
var allowedRepoImports = map[string]bool{
	"ikhnos/internal/agents":       true,
	"ikhnos/internal/chart":        true,
	"ikhnos/internal/connectivity": true,
	"ikhnos/internal/httpkit":      true,
	"ikhnos/internal/install":      true,
	"ikhnos/internal/installcfg":   true,
	"ikhnos/internal/kernel":       true,
	"ikhnos/internal/wire":         true,
	"ikhnos/internal/destination":  true,
	"ikhnos/internal/operators":    true,
	"ikhnos/internal/store":        true,
	"ikhnos/internal/telemetry":    true,
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
			if strings.HasPrefix(p, "ikhnos/") && !allowedRepoImports[p] {
				t.Errorf("%s imports %s: the operators API may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
