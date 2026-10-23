package server

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const packagesFile = "testdata/packages.txt"

const packagesHeader = `# Every import of one package of internal/ by another, as "importing package > imported package" (internal/ left off), for the
# source of each package without its tests. The shell (internal/server) is left out: it wires the modules together and may use any.
# This is the picture of the whole server as it stands; docs/server-modules.md draws it in words. A new line is a new dependency
# between two parts of the server: accept it on purpose, with the reason in the change, or put the thing where both can reach it.
# Regenerate on purpose with: go test ./internal/server -run TestPackages -update
`

// TestPackagesImportEachOtherOnlyWhereTheListSays pins the whole graph in one place. Each package has a test of its own that refuses an import
// outside its list; this is the sum of them, so a reader sees the layering at once and a change to it shows in the review as a line.
func TestPackagesImportEachOtherOnlyWhereTheListSays(t *testing.T) {
	if testing.Short() {
		t.Skip("reads the module with go list")
	}
	out, err := exec.Command("go", "list", "-json=ImportPath,Imports", "ikhnos/internal/...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	const root = "ikhnos/internal/"
	var lines []string
	byPkg := map[string][]string{}
	for dec := json.NewDecoder(strings.NewReader(string(out))); dec.More(); {
		var p struct {
			ImportPath string
			Imports    []string
		}
		if err := dec.Decode(&p); err != nil {
			t.Fatal(err)
		}
		from := strings.TrimPrefix(p.ImportPath, root)
		if from == "server" {
			continue
		}
		for _, im := range p.Imports {
			if !strings.HasPrefix(im, root) {
				continue
			}
			to := strings.TrimPrefix(im, root)
			if to == "server" {
				t.Errorf("%s imports the server: the server wires the modules, and nothing under it may reach back", from)
			}
			lines = append(lines, from+" > "+to)
			byPkg[from] = append(byPkg[from], to)
		}
	}
	if len(byPkg) < 20 {
		t.Fatalf("only %d packages were read: the reading has stopped seeing them", len(byPkg))
	}
	sort.Strings(lines)
	compareList(t, packagesFile, packagesHeader, "import(s) between packages", lines, map[string]string{})
}

// TestEveryModulePackageIsNamedInTheModulesPage keeps the page from going stale in the way it did: a package that holds its own import
// rules (an imports_test.go) is a part of the picture and must be named there as internal/<name>.
func TestEveryModulePackageIsNamedInTheModulesPage(t *testing.T) {
	raw, err := os.ReadFile("../../docs/server-modules.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	files, _ := filepath.Glob("../*/imports_test.go")
	if len(files) < 15 {
		t.Fatalf("only %d packages with import rules were found", len(files))
	}
	for _, f := range files {
		name := filepath.Base(filepath.Dir(f))
		if !strings.Contains(page, "internal/"+name+"`") && !strings.Contains(page, "internal/"+name+" ") && !strings.Contains(page, "internal/"+name+".") && !strings.Contains(page, "internal/"+name+",") {
			t.Errorf("docs/server-modules.md does not name internal/%s: say where it sits in the picture", name)
		}
	}
}
