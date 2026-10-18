package agents

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The agents stand on the kernel, the store, the PKI and the approval and access vocabularies, and on the modules below them: accounts
// (the names the server acts under), observed (what an address leads to), operators (the certificate renewed on the stream),
// telemetry (the intents that end with an agent) and the decision loop (the shape of a change to an agent's access). They do not import
// the server, so not the hub that keeps the live picture of them, and not connectivity, which stands on them.
var allowedRepoImports = map[string]bool{
	"continuum/gen/continuumv1":       true,
	"continuum/internal/kernel":       true,
	"continuum/internal/store":        true,
	"continuum/internal/pki":          true,
	"continuum/internal/approval":     true,
	"continuum/internal/agentcaps":    true,
	"continuum/internal/install":      true,
	"continuum/internal/geoip":        true,
	"continuum/internal/interpret":    true,
	"continuum/internal/model":        true,
	"continuum/internal/accounts":     true,
	"continuum/internal/observed":     true,
	"continuum/internal/operators":    true,
	"continuum/internal/telemetry":    true,
	"continuum/internal/decisionloop": true,
}

func TestAgentsImportNothingOfTheServerOrAModuleAboveThem(t *testing.T) {
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
				t.Errorf("%s imports %s: the agents may not stand on it", f, p)
			}
		}
	}
}
