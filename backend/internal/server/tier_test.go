package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Whether a tier is allowed is decided in tier.go and nowhere else. This reads the package's own source: no comparison or
// min/max outside tier.go may involve ImplementedTier, MaxTier, TierCap or InstalledTier, so a place that did its own
// arithmetic (the way six places once did) fails here. Two readings are not a bound on a grant and are left alone: an approved
// tier against the installed one (does the approval sit below the install), and an installed tier against 0 (did the install
// ever say).
func TestTheTierBoundsAreDecidedInOnePlace(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 20 {
		t.Fatalf("source files not found: %v %d", err, len(files))
	}
	mentionsBound := func(n ast.Node) (hit bool) {
		ast.Inspect(n, func(x ast.Node) bool {
			switch v := x.(type) {
			case *ast.Ident:
				hit = hit || v.Name == "ImplementedTier" || v.Name == "MaxTier"
			case *ast.SelectorExpr:
				hit = hit || v.Sel.Name == "TierCap" || v.Sel.Name == "InstalledTier"
			}
			return true
		})
		return hit
	}
	displayOnly := func(b *ast.BinaryExpr) bool { // ag.AccessTier < ag.InstalledTier
		l, ok1 := b.X.(*ast.SelectorExpr)
		r, ok2 := b.Y.(*ast.SelectorExpr)
		return ok1 && ok2 && l.Sel.Name == "AccessTier" && r.Sel.Name == "InstalledTier"
	}
	againstZero := func(b *ast.BinaryExpr) bool { // fresh.InstalledTier > 0: "did the install ever say", not a bound
		lit, ok := b.Y.(*ast.BasicLit)
		return ok && lit.Value == "0"
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "tier.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BinaryExpr:
				switch v.Op {
				case token.LSS, token.GTR, token.LEQ, token.GEQ:
					if mentionsBound(v) && !displayOnly(v) && !againstZero(v) {
						t.Errorf("%s: a tier bound is compared here; ask CheckTier, GrantableTier or ReportedTier in tier.go", fset.Position(v.Pos()))
					}
				}
			case *ast.CallExpr:
				if id, ok := v.Fun.(*ast.Ident); ok && (id.Name == "min" || id.Name == "max") && mentionsBound(v) {
					t.Errorf("%s: a tier bound is worked out here; ask tier.go", fset.Position(v.Pos()))
				}
			}
			return true
		})
	}
}
