package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Every command this server prints for a person to paste into a shell is described as a value and written by internal/install, which is
// where it is quoted and checked. This test fails if a command is written by hand again in the server: a string that begins as a helm or
// kubectl command, a `--set key=value`, or a here-document.
//
// It reads the string literals of the server's own code (not its tests) and is a tripwire, not a proof: a command assembled from pieces
// that are each harmless would pass. What it holds is the habit. Advice written in a sentence ("check the pods with `kubectl get pods`")
// is not a command and is not caught: the literal has to begin with the command, or hold an assignment of a Helm value.
var handWrittenCommand = regexp.MustCompile("(?m)" +
	"(^[\\s`]*(helm|kubectl)\\s+(install|upgrade|uninstall|apply|create|delete|patch|rollout|replace|scale)\\b)" + // a command that changes something
	"|(--set(-string|-json)?\\s+[A-Za-z%][-A-Za-z0-9_.%]*=)" + // a Helm value assigned
	"|(<<-?'?\"?[A-Z_]{3,})") // a here-document

func TestNoCommandIsWrittenByHandOutsideTheInstallPackage(t *testing.T) {
	var found []string
	for _, dir := range []string{".", "../../cmd/server"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in %s: %v", dir, err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, file, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
					if v, err := strconv.Unquote(l.Value); err == nil && handWrittenCommand.MatchString(v) {
						found = append(found, fset.Position(l.Pos()).String()+": "+excerpt(v))
					}
				}
				return true
			})
		}
	}
	if len(found) > 0 {
		t.Fatalf("a command is written by hand, which internal/install exists to do (describe it as an install.Helm, Secret or the like):\n%s", strings.Join(found, "\n"))
	}
}

// The tripwire catches the way a command is written and lets advice through.
func TestTheHandWrittenCommandPatternSeparatesCommandsFromAdvice(t *testing.T) {
	for _, s := range []string{
		"helm upgrade %s %s --namespace %s", "helm install continuum-agent", "  helm uninstall %s", "`helm upgrade x`", "kubectl delete secret %s",
		"kubectl apply -f -", "kubectl rollout restart deployment/%s", "--set access.tier=%d", "--set %s", "--set-json operator=%s", "--set telemetry.resource.orgId=%s",
		"cat <<'EOF'", "kubectl apply -f - <<'CONTINUUM_SECRET'\napiVersion: v1",
	} {
		if s == "--set %s" {
			continue // a value written elsewhere, joined here: not an assignment
		}
		if !handWrittenCommand.MatchString(s) {
			t.Errorf("%q is a command and is not caught", s)
		}
	}
	for _, s := range []string{
		"Check the pods (kubectl -n %s get pods) - most often an image that cannot be pulled",
		"Details: kubectl -n %s get pods", "run `helm upgrade --reset-then-reuse-values` to widen it", "the expected cluster is the UID (kubectl get namespace kube-system)",
		"Look at the operator first: `kubectl -n continuum-system get pods` in its cluster. ", "set it with --set or in values.yaml", "heredoc", "a <b",
	} {
		if handWrittenCommand.MatchString(s) {
			t.Errorf("%q is advice and is caught", s)
		}
	}
}

func excerpt(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}
