package estate

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The estate stands on the modules below it in the hub cluster (observed, agents, connectivity), on the accounts' vocabulary of who is
// calling, on the decision loop whose ports it supplies, on the HTTP kit and the install settings its routes are written with, and on the models, vocabularies and stores the twin is built from. It does not
// import the server (the shell is the one that wires it and serves it) and does not import fusion or telemetry.
var allowedRepoImports = map[string]bool{
	"ikhnos/gen/ikhnosv1":          true,
	"ikhnos/internal/accounts":     true,
	"ikhnos/internal/agentcaps":    true,
	"ikhnos/internal/agents":       true,
	"ikhnos/internal/connectivity": true,
	"ikhnos/internal/decisionloop": true,
	"ikhnos/internal/facts":        true,
	"ikhnos/internal/flow/wire":    true,
	"ikhnos/internal/geoip":        true,
	"ikhnos/internal/graph":        true,
	"ikhnos/internal/history":      true,
	"ikhnos/internal/httpkit":      true,
	"ikhnos/internal/install":      true,
	"ikhnos/internal/installcfg":   true,
	"ikhnos/internal/interpret":    true,
	"ikhnos/internal/kernel":       true,
	"ikhnos/internal/wire":         true,
	"ikhnos/internal/link":         true,
	"ikhnos/internal/measure":      true,
	"ikhnos/internal/model":        true,
	"ikhnos/internal/observed":     true,
	"ikhnos/internal/operators":    true,
	"ikhnos/internal/store":        true,
	"ikhnos/internal/twin":         true,
	"ikhnos/internal/workspace":    true,
}

func TestEstateImportsNothingOfTheServer(t *testing.T) {
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
				t.Errorf("%s imports %s: the estate may not stand on it", f, p)
			}
		}
	}
}
