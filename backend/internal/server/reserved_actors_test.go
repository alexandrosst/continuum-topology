package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"continuum/internal/store"
)

// The audit trail names who did a thing. For a person that is their username, and for the server itself it is a fixed word:
// "system" for what the server does on its own, "anonymous" for someone who is not signed in, "renewal" for the operator
// certificate renewal. If a person could register under one of those words, a row they caused would read as the server's. So the
// words are reserved: no new account may take them, in any case. (An agent's rows read "agent:<id>"; a username cannot hold a
// colon, so that form needs no reservation.)

func TestReservedActorWordsCannotBeUsernames(t *testing.T) {
	for _, name := range []string{"system", "anonymous", "renewal", "System", "ANONYMOUS", "Renewal", "sYsTeM"} {
		if usernameProblem(name) == "" {
			t.Errorf("%q can be a username", name)
		}
	}
	for _, name := range []string{"alice", "sysadmin", "system1", "anon", "systemd", "renewals", "a.b-c_d@e"} {
		if p := usernameProblem(name); p != "" {
			t.Errorf("%q was refused: %s", name, p)
		}
	}
	for _, name := range []string{"", "ab", "-no", "has space", "agent:ag-1", strings.Repeat("a", 65)} {
		if usernameProblem(name) == "" {
			t.Errorf("%q should not be a username", name)
		}
	}
}

func TestRegistrationRefusesAReservedUsername(t *testing.T) {
	a := newAdminRig(t)
	for _, name := range []string{"system", "Anonymous", "RENEWAL"} {
		r := a.do("POST", "/api/v1/auth/register", map[string]string{"username": name, "password": goodPW, "org": "Spoof Lab"})
		if r.Code != 400 {
			t.Errorf("registering %q: %d %s, want 400", name, r.Code, r.Body.String())
		}
		if _, err := a.st.GetUserByName(a.ctx, strings.ToLower(name)); err == nil {
			t.Errorf("an account named %q was created", name)
		}
	}
	a.register(t, "systematic", "Fine Lab") // a name that only starts like one is fine
}

func TestRecoveryDoesNotCreateAReservedUsernameButStillResetsOneThatExists(t *testing.T) {
	a := newAdminRig(t)
	if _, err := a.base.RecoverPassword(a.ctx, "system"); err == nil {
		t.Fatal("recovery created an account named system")
	}
	if _, err := a.st.GetUserByName(a.ctx, "system"); err == nil {
		t.Fatal("an account named system exists")
	}
	// An account that already has such a name (made before the words were reserved) is not locked out: a server fix must not
	// strand a person, and forward-only means nothing is renamed or removed here.
	if err := a.st.CreateUser(a.ctx, store.User{ID: "u-old", Username: "renewal", PasswordHash: "x", CreatedAt: a.base.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.base.RecoverPassword(a.ctx, "renewal"); err != nil {
		t.Fatalf("an existing account could not be recovered: %v", err)
	}
}

// Every word the server writes as an actor must be reserved. This reads the package's own source: each audit call whose actor is
// a string literal must use a reserved word or an "agent:" form, so a new server-side actor name cannot be added without a person
// being unable to register it.
func TestEveryLiteralActorTheServerWritesIsReserved(t *testing.T) {
	// the index of the actor argument for each call that writes an audit row
	actorArg := map[string]int{"audit": 1, "auditOrg": 2, "auditRow": 1}
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 20 {
		t.Fatalf("source files not found: %v %d", err, len(files))
	}
	fset := token.NewFileSet()
	seen := map[string]int{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
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
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			i, ok := actorArg[sel.Sel.Name]
			if !ok || i >= len(call.Args) {
				return true
			}
			lit, ok := call.Args[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			seen[v]++
			if v == "" || strings.HasPrefix(v, "agent:") {
				return true // "" is the server's own trail's org, never an actor; agent: forms hold a colon
			}
			if !reservedActors[strings.ToLower(v)] {
				t.Errorf("%s: the server writes %q as an actor, and a person could register that name", fset.Position(lit.Pos()), v)
			}
			return true
		})
	}
	if seen["system"] == 0 || seen["anonymous"] == 0 || seen["renewal"] == 0 {
		t.Fatalf("the scan no longer finds the actors it was written for: %v", seen)
	}
}
