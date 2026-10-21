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
	"continuum/gen/continuumv1":       true,
	"continuum/internal/accounts":     true,
	"continuum/internal/agentcaps":    true,
	"continuum/internal/agents":       true,
	"continuum/internal/connectivity": true,
	"continuum/internal/decisionloop": true,
	"continuum/internal/facts":        true,
	"continuum/internal/flow/wire":    true,
	"continuum/internal/geoip":        true,
	"continuum/internal/graph":        true,
	"continuum/internal/history":      true,
	"continuum/internal/httpkit":      true,
	"continuum/internal/install":      true,
	"continuum/internal/installcfg":   true,
	"continuum/internal/interpret":    true,
	"continuum/internal/kernel":       true,
	"continuum/internal/wire":         true,
	"continuum/internal/link":         true,
	"continuum/internal/measure":      true,
	"continuum/internal/model":        true,
	"continuum/internal/observed":     true,
	"continuum/internal/operators":    true,
	"continuum/internal/store":        true,
	"continuum/internal/twin":         true,
	"continuum/internal/workspace":    true,
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
			if strings.HasPrefix(p, "continuum/") && !allowedRepoImports[p] {
				t.Errorf("%s imports %s: the estate may not stand on it", f, p)
			}
		}
	}
}
