package settingsapi

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The settings' routes stand on the kernel (the settings are its), the accounts (who may see the decider's address) and the HTTP kit
// they are written with, and on nothing else of this repository.
var allowedRepoImports = map[string]bool{
	"continuum/internal/kernel":   true,
	"continuum/internal/wire":     true,
	"continuum/internal/accounts": true,
	"continuum/internal/httpkit":  true,
}

func TestTheSettingsAPIImportsOnlyTheKernelTheAccountsAndTheKit(t *testing.T) {
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
				t.Errorf("%s imports %s: the settings API may import only %v", f, p, allowedRepoImports)
			}
		}
	}
}
