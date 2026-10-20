package telemetry

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Telemetry stands on the kernel, the store and the shape of a destination, and on nothing else of this repository: not the server, and
// not another module. An intent may point at an operator, and asks the operators through a port it owns (Operators); the operators do not
// import telemetry, and telemetry no longer imports them.
var allowedRepoImports = map[string]bool{
	"continuum/internal/httpkit":     true,
	"continuum/internal/kernel":      true,
	"continuum/internal/store":       true,
	"continuum/internal/destination": true,
}

func TestTelemetryImportsOnlyTheKernelTheStoreAndTheDestinationDocument(t *testing.T) {
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
				t.Errorf("%s imports %s: telemetry may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
