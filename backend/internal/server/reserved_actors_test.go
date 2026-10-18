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

	"continuum/internal/accounts"
	"continuum/internal/store"
)

// The audit trail names who did a thing. For a person that is their username, and for the server itself it is a fixed word:
// "system" for what the server does on its own, "anonymous" for someone who is not signed in, "renewal" for the operator
// certificate renewal. If a person could register under one of those words, a row they caused would read as the server's. So the
// words are reserved: no new account may take them, in any case. (An agent's rows read "agent:<id>"; a username cannot hold a
// colon, so that form needs no reservation.)

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
	if _, err := a.base.Accounts.RecoverPassword(a.ctx, "system"); err == nil {
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
	if _, err := a.base.Accounts.RecoverPassword(a.ctx, "renewal"); err != nil {
		t.Fatalf("an existing account could not be recovered: %v", err)
	}
}

// serverCodeDirs are the directories that hold the server's own code: this package, the kernel and each module that is a package.
// A module that leaves this package is added here, so that the rule below keeps covering it.
var serverCodeDirs = []string{".", "../kernel", "../accounts", "../decisionloop", "../operators", "../telemetry", "../fusion", "../observed", "../agents"}

// The words the server uses for itself are defined in accounts/actor.go and nowhere else. This reads the server's own source: no string
// literal outside that file may be a reserved word or an "agent:" form, so a call site that wrote "system" by hand (and a new server
// actor added without being reserved) fails here, and the only way to name the server is an Actor value.
func TestTheServersOwnActorWordsAreWrittenInOnePlace(t *testing.T) {
	var files []string
	for _, d := range serverCodeDirs {
		fs, err := filepath.Glob(filepath.Join(d, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, fs...)
	}
	if len(files) < 40 {
		t.Fatalf("source files not found: %d", len(files))
	}
	reserved := map[string]bool{}
	for _, a := range []accounts.Actor{accounts.AnonymousActor, accounts.SystemActor, accounts.RenewalActor} {
		reserved[strings.ToLower(a.String())] = true
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == filepath.Join("..", "accounts", "actor.go") {
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
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, _ := strconv.Unquote(lit.Value)
			if reserved[strings.ToLower(v)] || strings.HasPrefix(v, "agent:") {
				t.Errorf("%s: %q is an actor word; use the Actor value from actor.go", fset.Position(lit.Pos()), v)
			}
			return true
		})
	}
}
