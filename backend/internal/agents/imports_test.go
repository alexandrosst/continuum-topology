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
// (the names the server acts under), observed (what an address leads to) and telemetry (the intents that end with an agent). They do not
// import the operators, who renew the certificate on the stream through a port the agents own (TelemetryCerts), nor the decision loop,
// which asks them about a change to access through a port of its own (decisionloop.Access) and which the estate answers; nor the server,
// so not the hub that keeps the live picture of them, and not connectivity, which stands on them.
var allowedRepoImports = map[string]bool{
	"ikhnos/gen/ikhnosv1":       true,
	"ikhnos/internal/kernel":    true,
	"ikhnos/internal/store":     true,
	"ikhnos/internal/pki":       true,
	"ikhnos/internal/approval":  true,
	"ikhnos/internal/agentcaps": true,
	"ikhnos/internal/install":   true,
	"ikhnos/internal/geoip":     true,
	"ikhnos/internal/interpret": true,
	"ikhnos/internal/model":     true,
	"ikhnos/internal/accounts":  true,
	"ikhnos/internal/observed":  true,
	"ikhnos/internal/telemetry": true,
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
			if strings.HasPrefix(p, "ikhnos/") && !allowedRepoImports[p] {
				t.Errorf("%s imports %s: the agents may not stand on it", f, p)
			}
		}
	}
}
