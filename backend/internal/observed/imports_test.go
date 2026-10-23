package observed

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Observed is pure: it stands on the vocabulary of the wire, the facts an agent sends and the model the topology is made of, and on
// nothing of the server or of another module (not the kernel either: it has no organisation, no clock of its own and no store).
var allowedRepoImports = map[string]bool{
	"ikhnos/gen/ikhnosv1":       true,
	"ikhnos/internal/facts":     true,
	"ikhnos/internal/flow/wire": true,
	"ikhnos/internal/interpret": true,
	"ikhnos/internal/model":     true,
	"ikhnos/internal/netid":     true,
	"ikhnos/internal/probe":     true,
}

func TestObservedImportsNothingOfTheServerOrAnotherModule(t *testing.T) {
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
				t.Errorf("%s imports %s: observed may not stand on it", f, p)
			}
		}
	}
}
