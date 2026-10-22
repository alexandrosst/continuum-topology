package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

// Every method of Store either takes the organisation it works in, or is listed here with the reason it does not. A new method that takes
// a bare id of something an organisation owns fails this test until it takes the organisation, so a handler that forgets the check cannot
// compile; and one that is genuinely not an organisation's has to say why, in review, in this list.
var withoutAnOrganisation = map[string]string{
	// The row is inserted with its organisation in it, so there is no id of another organisation's to name.
	"CreateToken": "carries its organisation", "CreateOperator": "carries its organisation", "AddOperatorCert": "carries its organisation",
	"CreateTelemetryIntent": "carries its organisation", "CreateGatewayToken": "carries its organisation", "CreateFusionToken": "carries its organisation",
	"PutLinkCredential": "carries its organisation", "AddAudit": "carries its organisation", "AddDecisions": "carries its organisation",
	"CreateProposal": "carries its organisation", "RecordProposalOutcome": "carries its organisation", "AddMember": "carries its organisation",
	"CreateInvite": "carries its organisation", "CreateOrg": "makes the organisation", "EnrollAgent": "the token it consumes belongs to one",

	// Found by a secret that only its holder has: the secret is the key, and the row says which organisation it is in.
	"AgentByToken": "found by a token's hash", "GetOperatorByHeartbeatHash": "found by a heartbeat secret's hash", "LookupFusionToken": "found by an access token's hash",
	"TouchFusionToken": "by an access token's hash", "PeekInvite": "found by an invitation's hash", "UseInvite": "by an invitation's hash",
	"LookupSession": "found by a session's hash", "TouchSession": "by a session's hash", "DeleteSession": "by a session's hash",
	"LookupAPIToken": "found by a personal token's hash", "TouchAPIToken": "by a personal token's hash",

	// Accounts, and the organisation as a row, are not inside an organisation: a person belongs to several, and an organisation's id is its own.
	"CreateUser": "an account", "GetUser": "an account", "GetUserByName": "an account", "DeleteUser": "an account", "CountUsers": "accounts", "SetPassword": "an account",
	"SetDisabled": "an account", "MarkLogin": "an account", "SetTOTP": "an account", "SetEmail": "an account", "SetEmailOTPEnabled": "an account",
	"AddWebAuthnCredential": "an account's", "RenameWebAuthnCredential": "an account's", "TouchWebAuthnCredential": "an account's", "RemoveWebAuthnCredential": "an account's",
	"CreateSession": "an account's", "DeleteUserSessions": "an account's", "CreateAPIToken": "an account's", "ListAPITokens": "an account's", "RevokeAPIToken": "an account's",
	"GetOrg": "the organisation itself", "ListOrgs": "the organisations themselves", "RenameOrg": "the organisation itself", "DeleteOrg": "the organisation itself",
	"ListMyOrgs": "an account's organisations",

	// The server's own.
	"PurgeFusionTokens": "housekeeping across the server", "PurgeSessions": "housekeeping across the server", "AuditSince": "the audit chain is one for the server",
	"GetMailConfig": "the server's mail settings", "PutMailConfig": "the server's mail settings", "Close": "the store",

	// Named for what it is.
	"FindAgent": "crosses organisations on purpose, for an agent that has just proved who it is",

	// KNOWN GAP: keyed by an agent id (or a key made from one) with no organisation column. Scoping needs a column and a migration; it matters
	// only on a server that hosts several organisations (see docs/server-modules.md, step 25).
	"SaveSnapshot": "gap: no organisation column", "LoadSnapshot": "gap: no organisation column", "DeleteSnapshots": "gap: no organisation column",
}

func TestEveryStoreMethodTakesItsOrganisationOrSaysWhyNot(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "store.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var without []string
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range g.Specs {
			ts, ok := sp.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Store" {
				continue
			}
			for _, m := range ts.Type.(*ast.InterfaceType).Methods.List {
				if len(m.Names) == 0 {
					continue
				}
				takesOrg := false
				for _, p := range m.Type.(*ast.FuncType).Params.List {
					for _, n := range p.Names {
						if n.Name == "org" || n.Name == "orgID" {
							takesOrg = true
						}
					}
				}
				if !takesOrg {
					without = append(without, m.Names[0].Name)
				}
			}
		}
	}
	if len(without) == 0 {
		t.Fatal("found no Store methods: the test reads the wrong file")
	}
	sort.Strings(without)
	seen := map[string]bool{}
	for _, name := range without {
		seen[name] = true
		if withoutAnOrganisation[name] == "" {
			t.Errorf("Store.%s takes no organisation. If it addresses something an organisation owns by its id, give it the organisation (org) and put it in the SQL; if it is not an organisation's, add it to withoutAnOrganisation with the reason", name)
		}
	}
	var stale []string
	for name := range withoutAnOrganisation {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("withoutAnOrganisation lists methods that are gone or take an organisation now, remove them: %s", strings.Join(stale, ", "))
	}
}
