package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/pki"
	"continuum/internal/store"
)

// register signs someone up through the API and returns their cookie and the id of the organisation they got.
func (a *adminRig) register(t *testing.T, name, org string, opts ...opt) (cookie, orgID string) {
	t.Helper()
	r := a.do("POST", "/api/v1/auth/register", map[string]string{"username": name, "password": goodPW, "org": org}, opts...)
	if r.Code != 201 {
		t.Fatalf("register %s: %d %s", name, r.Code, r.Body.String())
	}
	orgs := r.json(t)["orgs"].([]any)
	if len(orgs) != 1 {
		t.Fatalf("orgs after register: %v", orgs)
	}
	return r.cookie(), orgs[0].(map[string]any)["id"].(string)
}

func org(id, p string) string { return "/api/v1/orgs/" + id + "/" + p }

func TestSelfServiceRegistrationCreatesAnAccountAndAnOrganisationItOwns(t *testing.T) {
	a := newAdminRig(t)
	if got := a.do("GET", "/api/v1/server", nil).json(t)["registration"]; got != RegOpen {
		t.Fatalf("registration mode = %v", got)
	}
	cookie, id := a.register(t, "alice", "Alice Lab")
	me := a.do("GET", "/api/v1/auth/me", nil, withCookie(cookie)).json(t)
	orgs := me["orgs"].([]any)
	if o := orgs[0].(map[string]any); o["role"] != RoleOwner || o["name"] != "Alice Lab" || o["id"] != id {
		t.Fatalf("orgs = %v", orgs)
	}
	if r := a.do("GET", org(id, "state"), nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("own state: %d %s", r.Code, r.Body.String())
	}
	// The person chose the password themselves, so nothing is forced.
	if me["user"].(map[string]any)["mustChangePassword"] == true {
		t.Error("a self-chosen password must not be marked temporary")
	}
	// Username is unique across the server, case-insensitively; weak passwords and odd names are refused.
	for name, body := range map[string]map[string]string{
		"duplicate":     {"username": "ALICE", "password": goodPW},
		"weak password": {"username": "bob", "password": "short"},
		"bad name":      {"username": "b o b!", "password": goodPW},
		"bad org name":  {"username": "bobby", "password": goodPW, "org": "x"},
	} {
		if c := a.do("POST", "/api/v1/auth/register", body, fromIP("10.7.7.7")).Code; c != 409 && c != 400 {
			t.Errorf("%s accepted: %d", name, c)
		}
	}
	// The organisation's trail names who created it.
	evs, _ := a.st.ListAudit(a.ctx, id, 10)
	var seen bool
	for _, e := range evs {
		seen = seen || (e.Action == "org-created" && e.Actor == "alice")
	}
	if !seen {
		t.Error("organisation creation not attributed")
	}
}

func TestRegistrationModes(t *testing.T) {
	a := newAdminRig(t)
	a.base.RegMode = RegInvite
	if c := a.do("POST", "/api/v1/auth/register", map[string]string{"username": "carol", "password": goodPW}).Code; c != 403 {
		t.Fatalf("invitation-only server accepted a plain sign-up: %d", c)
	}
	_, owner := a.user(t, "boss", RoleOwner)
	tok := a.invite(t, owner, RoleViewer, "carol")
	r := a.do("POST", "/api/v1/auth/register", map[string]string{"username": "carol", "password": goodPW, "invite": tok})
	if r.Code != 201 {
		t.Fatalf("sign-up with an invitation: %d %s", r.Code, r.Body.String())
	}
	orgs := r.json(t)["orgs"].([]any)
	if len(orgs) != 1 || orgs[0].(map[string]any)["id"] != "org-1" || orgs[0].(map[string]any)["role"] != RoleViewer {
		t.Fatalf("orgs = %v", orgs)
	}
	if c := a.do("POST", "/api/v1/orgs", map[string]string{"name": "Mine"}, withCookie(r.cookie())).Code; c != 403 {
		t.Errorf("creating an organisation on an invitation-only server: %d", c)
	}
	a.base.RegMode = RegClosed
	tok2 := a.invite(t, owner, RoleViewer, "dave")
	if c := a.do("POST", "/api/v1/auth/register", map[string]string{"username": "dave", "password": goodPW, "invite": tok2}).Code; c != 403 {
		t.Errorf("closed server accepted a sign-up: %d", c)
	}
}

func (a *adminRig) invite(t *testing.T, cookie, role, label string) string {
	t.Helper()
	r := a.do("POST", "/api/v1/invites", map[string]string{"role": role, "label": label}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("invite as %s: %d %s", role, r.Code, r.Body.String())
	}
	return r.json(t)["token"].(string)
}

func TestNobodyReachesAnotherOrganisation(t *testing.T) {
	a := newAdminRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	bob, bOrg := a.register(t, "bob", "Bob Works")
	if aOrg == bOrg {
		t.Fatal("two people share an organisation id")
	}
	// Everything organisation-scoped, tried by Bob against Alice's organisation. Not being a member looks
	// exactly like there being no such organisation, whatever the route and whatever role it needs.
	routes := []struct{ m, p string }{
		{"GET", "info"}, {"GET", "state"}, {"GET", "model"}, {"GET", "workspace"}, {"PUT", "workspace"}, {"GET", "settings"}, {"PUT", "settings"},
		{"GET", "history"}, {"GET", "history/snapshot?at=2020-01-01T00:00:00Z"}, {"GET", "history/traffic"}, {"POST", "history/record"},
		{"GET", "events"}, {"GET", "storage"}, {"GET", "timeline?kind=service&id=x"}, {"GET", "audit"}, {"GET", "workspace/revisions"}, {"GET", "workspace/at?at=2030-01-01T00:00:00Z"}, {"POST", "decide"}, {"GET", "tokens"}, {"POST", "tokens"}, {"DELETE", "tokens/x"},
		{"POST", "agents/ag-x/approve"}, {"POST", "agents/ag-x/reject"}, {"POST", "agents/ag-x/revoke"},
		{"GET", "members"}, {"POST", "members/u-x/role"}, {"POST", "members/u-x/remove"}, {"POST", "leave"},
		{"GET", "invites"}, {"POST", "invites"}, {"DELETE", "invites/x"}, {"POST", "rename"}, {"POST", "delete"},
	}
	for i, rt := range routes {
		anon := fromIP(fmt.Sprintf("10.20.0.%d", i+1)) // failed sign-ins are throttled per address
		opts := []opt{withCookie(bob), withHeader("If-Match", "0")}
		if c := a.do(rt.m, org(aOrg, rt.p), map[string]any{}, opts...).Code; c != 404 {
			t.Errorf("Bob %s %s in Alice's organisation: %d, want 404", rt.m, rt.p, c)
		}
		if c := a.do(rt.m, org(aOrg, rt.p), map[string]any{}, withHeader("If-Match", "0"), anon).Code; c != 401 {
			t.Errorf("anonymous %s %s: %d, want 401", rt.m, rt.p, c)
		}
		// And the answer for an organisation that does not exist is the same as for one that does.
		if c := a.do(rt.m, org("org-nope", rt.p), map[string]any{}, opts...).Code; c != 404 {
			t.Errorf("Bob %s %s in a made-up organisation: %d, want 404", rt.m, rt.p, c)
		}
	}
	// What one saves, the other cannot see.
	if c := a.do("PUT", org(aOrg, "workspace"), []byte(`{"schemaVersion":3,"clusters":[{"id":"secret"}]}`), withCookie(alice), withHeader("If-Match", "0")).Code; c != 200 {
		t.Fatalf("alice save: %d", c)
	}
	if got := a.do("GET", org(bOrg, "workspace"), nil, withCookie(bob)).json(t); got["rev"].(float64) != 0 || got["data"] != nil {
		t.Fatalf("bob sees %v", got)
	}
	// Alice's agent token shows in her list only, and Bob cannot act on her agent from his own organisation.
	tok := a.do("POST", org(aOrg, "tokens"), map[string]any{"name": "edge-a", "tier": 1}, withCookie(alice))
	if tok.Code != 201 {
		t.Fatalf("token: %d", tok.Code)
	}
	if l := a.do("GET", org(bOrg, "tokens"), nil, withCookie(bob)).Body.String(); strings.Contains(l, "edge-a") {
		t.Fatalf("bob lists alice's token: %s", l)
	}
	d, _ := csr(t)
	resp, err := a.base.Enroll(a.ctx, "10.0.0.9", &continuumv1.EnrollRequest{Token: tok.json(t)["token"].(string), CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{"approve", "reject", "revoke"} {
		body := map[string]any{"reason": "no"}
		if act == "approve" {
			body = map[string]any{"confirm": fp[:8], "tier": 1}
		}
		if c := a.do("POST", org(bOrg, "agents/"+resp.AgentId+"/"+act), body, withCookie(bob)).Code; c != 404 {
			t.Errorf("bob %ss alice's agent: %d, want 404", act, c)
		}
	}
	if ag, _ := a.st.GetAgent(a.ctx, resp.AgentId); ag.Status != store.StatusPending || ag.OrgID != aOrg {
		t.Fatalf("agent = %+v", ag)
	}
	// The pending agent appears in Alice's view and never in Bob's.
	if !strings.Contains(a.do("GET", org(aOrg, "state"), nil, withCookie(alice)).Body.String(), resp.AgentId) {
		t.Error("alice does not see her pending agent")
	}
	if strings.Contains(a.do("GET", org(bOrg, "state"), nil, withCookie(bob)).Body.String(), resp.AgentId) {
		t.Error("bob sees alice's agent")
	}
	// Alice's audit trail is hers.
	if strings.Contains(a.do("GET", org(bOrg, "state"), nil, withCookie(bob)).Body.String(), "alice") {
		t.Error("bob's audit log mentions alice")
	}
	// A token from Bob's organisation does not enroll into Alice's, and the agent takes the token's organisation.
	btok := a.do("POST", org(bOrg, "tokens"), map[string]any{"name": "edge-b", "tier": 1}, withCookie(bob)).json(t)["token"].(string)
	d2, _ := csr(t)
	fp2 := "11111111-2222-4333-8444-555555555555"
	r2, err := a.base.Enroll(a.ctx, "10.0.0.9", &continuumv1.EnrollRequest{Token: btok, CsrDer: d2, ClusterFingerprint: fp2, InstalledAccessTier: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ag, _ := a.st.GetAgent(a.ctx, r2.AgentId); ag.OrgID != bOrg {
		t.Fatalf("agent enrolled with Bob's token belongs to %q", ag.OrgID)
	}
}

func TestSameClusterCanBeRegisteredByTwoTenantsIndependently(t *testing.T) {
	a := newAdminRig(t)
	alice, aOrg := a.register(t, "alice", "Org A")
	bob, bOrg := a.register(t, "bob", "Org B")
	enrollAndApprove := func(cookie, org string) string {
		tok := a.do("POST", "/api/v1/orgs/"+org+"/tokens", map[string]any{"name": "shared", "tier": 1}, withCookie(cookie)).json(t)["token"].(string)
		d, _ := csr(t)
		r, err := a.base.Enroll(a.ctx, "10.0.0.9", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1})
		if err != nil {
			t.Fatal(err)
		}
		if c := a.do("POST", "/api/v1/orgs/"+org+"/agents/"+r.AgentId+"/approve", map[string]any{"confirm": fp[:8], "tier": 1}, withCookie(cookie)).Code; c != 204 {
			t.Fatalf("approve in %s: %d", org, c)
		}
		return r.AgentId
	}
	ida, idb := enrollAndApprove(alice, aOrg), enrollAndApprove(bob, bOrg)
	x, _ := a.st.GetAgent(a.ctx, ida)
	y, _ := a.st.GetAgent(a.ctx, idb)
	if x.ClusterID == y.ClusterID || x.ClusterID == "" {
		t.Fatalf("cluster ids collide across tenants: %q %q", x.ClusterID, y.ClusterID)
	}
}

func TestInvitationsAreOneTimeExpiringAndRoleBound(t *testing.T) {
	a := newAdminRig(t)
	ownerID, owner := a.user(t, "boss", RoleOwner)
	_, admin := a.user(t, "adm", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	_ = ownerID

	// Who may invite whom.
	if c := a.do("POST", "/api/v1/invites", map[string]string{"role": RoleViewer}, withCookie(editor)).Code; c != 403 {
		t.Errorf("an editor invited someone: %d", c)
	}
	for _, role := range []string{RoleOwner, RoleAdmin} {
		if c := a.do("POST", "/api/v1/invites", map[string]string{"role": role}, withCookie(admin)).Code; c != 403 {
			t.Errorf("an administrator invited an %s: %d", role, c)
		}
	}
	if c := a.do("POST", "/api/v1/invites", map[string]string{"role": "root"}, withCookie(owner)).Code; c != 400 {
		t.Errorf("made-up role: %d", c)
	}
	tok := a.invite(t, admin, RoleEditor, "eve")

	// The list never shows the secret, only what it is for.
	list := a.do("GET", "/api/v1/invites", nil, withCookie(owner)).Body.String()
	if strings.Contains(list, tok) || !strings.Contains(list, "eve") {
		t.Fatalf("invite list = %s", list)
	}
	// Preview says what it is, to someone who is not signed in.
	pv := a.do("POST", "/api/v1/invites/preview", map[string]string{"token": tok}, fromIP("10.4.4.4"))
	if pv.Code != 200 || pv.json(t)["organisation"] != "Org One" || pv.json(t)["role"] != RoleEditor {
		t.Fatalf("preview: %d %s", pv.Code, pv.Body.String())
	}
	// An existing person accepts it while signed in.
	frankID := a.account(t, "frank")
	frank := a.login(t, "frank", goodPW)
	acc := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok}, withCookie(frank), fromIP("10.30.0.1"))
	if acc.Code != 200 || acc.json(t)["role"] != RoleEditor || acc.json(t)["id"] != "org-1" {
		t.Fatalf("accept: %d %s", acc.Code, acc.Body.String())
	}
	if m, err := a.st.GetMembership(a.ctx, "org-1", frankID); err != nil || m.Role != RoleEditor {
		t.Fatalf("membership: %+v %v", m, err)
	}
	// One time only.
	gina := a.login(t, "frank", goodPW)
	_ = gina
	other := a.account(t, "gina")
	_ = other
	ginaC := a.login(t, "gina", goodPW)
	if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok}, withCookie(ginaC), fromIP("10.30.0.2")).Code; c != 400 {
		t.Errorf("a used invitation worked again: %d", c)
	}
	if c := a.do("POST", "/api/v1/invites/preview", map[string]string{"token": tok}, fromIP("10.4.4.5")).Code; c != 400 {
		t.Errorf("a used invitation still previews: %d", c)
	}
	// An existing member keeps their role and the invitation is not spent.
	tok2 := a.invite(t, owner, RoleViewer, "")
	if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok2}, withCookie(frank), fromIP("10.30.0.3")).Code; c != 409 {
		t.Errorf("accepting into an organisation you are in: %d", c)
	}
	if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok2}, withCookie(ginaC), fromIP("10.30.0.4")).Code; c != 200 {
		t.Errorf("the unspent invitation: %d", c)
	}
	// Revoked and expired invitations are refused; garbage is refused without hitting the database.
	tok3 := a.invite(t, owner, RoleViewer, "late")
	invs, _ := a.st.ListInvites(a.ctx, "org-1")
	var id3 string
	for _, i := range invs {
		if i.Label == "late" {
			id3 = i.ID
		}
	}
	hank := a.login(t, "frank", goodPW)
	*a.now = a.now.Add(InviteTTL + time.Minute)
	if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok3}, withCookie(hank), fromIP("10.30.0.5")).Code; c != 400 && c != 401 {
		t.Errorf("an expired invitation worked: %d", c)
	}
	*a.now = a.now.Add(-InviteTTL - time.Minute)
	tok4 := a.invite(t, owner, RoleViewer, "gone")
	invs, _ = a.st.ListInvites(a.ctx, "org-1")
	for _, i := range invs {
		if i.Label == "gone" {
			if c := a.do("DELETE", org("org-1", "invites/"+i.ID), nil, withCookie(owner)).Code; c != 204 {
				t.Fatalf("revoke: %d", c)
			}
		}
	}
	_ = id3
	iris := a.account(t, "iris")
	_ = iris
	irisC := a.login(t, "iris", goodPW)
	if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": tok4}, withCookie(irisC), fromIP("10.30.0.6")).Code; c != 400 {
		t.Errorf("a revoked invitation worked: %d", c)
	}
	for i, bad := range []string{"", "nonsense", "cni_" + strings.Repeat("a", 32)} {
		if c := a.do("POST", "/api/v1/invites/accept", map[string]string{"token": bad}, withCookie(irisC), fromIP(fmt.Sprintf("10.31.0.%d", i))).Code; c != 400 {
			t.Errorf("token %q: %d", bad, c)
		}
	}
}

func TestRoleRulesAndTheLastOwner(t *testing.T) {
	a := newAdminRig(t)
	if err := a.st.RemoveMember(a.ctx, "org-1", "u-owner"); err != nil { // the rig's own placeholder owner
		t.Fatal(err)
	}
	ownerID, owner := a.user(t, "boss", RoleOwner)
	admID, admin := a.user(t, "adm", RoleAdmin)
	edID, editor := a.user(t, "ed", RoleEditor)
	viewID, viewer := a.user(t, "view", RoleViewer)
	setRole := func(cookie, id, role string) int {
		return a.do("POST", org("org-1", "members/"+id+"/role"), map[string]string{"role": role}, withCookie(cookie)).Code
	}
	remove := func(cookie, id string) int {
		return a.do("POST", org("org-1", "members/"+id+"/remove"), nil, withCookie(cookie)).Code
	}

	// Editors edit the workspace; viewers cannot.
	doc := []byte(`{"schemaVersion":3}`)
	if c := a.do("PUT", "/api/v1/workspace", doc, withCookie(viewer), withHeader("If-Match", "0")).Code; c != 403 {
		t.Errorf("viewer saved the workspace: %d", c)
	}
	if c := a.do("PUT", "/api/v1/workspace", doc, withCookie(editor), withHeader("If-Match", "0")).Code; c != 200 {
		t.Errorf("editor could not save the workspace: %d", c)
	}
	// ...but an editor manages nothing.
	if c := a.do("GET", "/api/v1/tokens", nil, withCookie(editor)).Code; c != 403 {
		t.Errorf("editor listed tokens: %d", c)
	}
	if c := a.do("PUT", "/api/v1/settings", Settings{}, withCookie(editor)).Code; c != 403 {
		t.Errorf("editor changed settings: %d", c)
	}

	// An administrator moves editors and viewers around, and nobody else.
	if c := setRole(admin, viewID, RoleEditor); c != 204 {
		t.Errorf("admin: viewer→editor: %d", c)
	}
	for name, c := range map[string]int{
		"admin promotes to admin": setRole(admin, edID, RoleAdmin),
		"admin demotes an owner":  setRole(admin, ownerID, RoleViewer),
		"admin removes an owner":  remove(admin, ownerID),
		"editor sets a role":      setRole(editor, viewID, RoleViewer),
		"editor removes someone":  remove(editor, viewID),
		"own role":                setRole(owner, ownerID, RoleAdmin),
	} {
		if c == 204 || c == 200 {
			t.Errorf("%s was allowed", name)
		}
	}
	if a.do("POST", org("org-1", "delete"), map[string]string{"confirm": "Org One"}, withCookie(admin)).Code != 403 {
		t.Error("an administrator deleted the organisation")
	}

	// The last owner cannot be demoted or removed or leave; with a second owner they can.
	if c := a.do("POST", org("org-1", "leave"), nil, withCookie(owner)).Code; c != 409 {
		t.Errorf("the only owner left: %d", c)
	}
	if c := setRole(owner, admID, RoleOwner); c != 204 {
		t.Fatalf("owner promotes: %d", c)
	}
	if c := setRole(admin, ownerID, RoleViewer); c != 204 { // admin is an owner now
		t.Errorf("a second owner demoting the first: %d", c)
	}
	if c := setRole(admin, admID, RoleViewer); c != 409 && c != 403 {
		t.Errorf("demoting yourself: %d", c)
	}
	if c := a.do("POST", org("org-1", "leave"), nil, withCookie(admin)).Code; c != 409 {
		t.Errorf("the last remaining owner left: %d", c)
	}
	// Removed people lose access at once but keep their account.
	if c := remove(admin, edID); c != 204 {
		t.Fatalf("remove: %d", c)
	}
	if c := a.do("GET", "/api/v1/state", nil, withCookie(editor)).Code; c != 404 {
		t.Errorf("a removed member still reaches the organisation: %d", c)
	}
	if c := a.do("GET", "/api/v1/auth/me", nil, withCookie(editor)).Code; c != 200 {
		t.Errorf("a removed member lost their account: %d", c)
	}
	// Someone who leaves by choice.
	if c := a.do("POST", org("org-1", "leave"), nil, withCookie(viewer)).Code; c != 204 {
		t.Errorf("a viewer could not leave: %d", c)
	}
	// Everything above is attributed.
	evs, _ := a.st.ListAudit(a.ctx, "org-1", 100)
	got := map[string]string{}
	for _, e := range evs {
		got[e.Action] = e.Actor
	}
	if got["member-role-changed"] != "adm" && got["member-role-changed"] != "boss" || got["member-removed"] != "adm" || got["member-left"] != "view" {
		t.Errorf("audit actors = %v", got)
	}
}

func TestDeletingAnOrganisationErasesItsDataAndEndsItsAgents(t *testing.T) {
	a := newAdminRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	bob, bOrg := a.register(t, "bob", "Bob Works")
	a.do("PUT", org(aOrg, "workspace"), []byte(`{"schemaVersion":3}`), withCookie(alice), withHeader("If-Match", "0"))
	a.do("PUT", org(bOrg, "workspace"), []byte(`{"schemaVersion":3,"keep":true}`), withCookie(bob), withHeader("If-Match", "0"))
	tok := a.do("POST", org(aOrg, "tokens"), map[string]any{"name": "edge", "tier": 1}, withCookie(alice)).json(t)["token"].(string)
	d, _ := csr(t)
	resp, err := a.base.Enroll(a.ctx, "10.0.0.9", &continuumv1.EnrollRequest{Token: tok, CsrDer: d, ClusterFingerprint: fp, InstalledAccessTier: 1})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.st.SaveSnapshot(a.ctx, resp.AgentId, []byte("{}"), *a.now)
	_ = a.st.SaveSnapshot(a.ctx, flowsKey(resp.AgentId), []byte("{}"), *a.now)
	if c := a.do("POST", org(aOrg, "delete"), map[string]string{"confirm": "wrong"}, withCookie(alice)).Code; c != 400 {
		t.Errorf("deleted without confirming the name: %d", c)
	}
	if c := a.do("POST", org(aOrg, "delete"), map[string]string{"confirm": "Alice Lab"}, withCookie(alice)).Code; c != 204 {
		t.Fatalf("delete: %d", c)
	}
	if c := a.do("GET", org(aOrg, "state"), nil, withCookie(alice)).Code; c != 404 {
		t.Errorf("a deleted organisation still answers: %d", c)
	}
	if _, err := a.st.GetAgent(a.ctx, resp.AgentId); err == nil {
		t.Error("the organisation's agent survived")
	}
	for _, k := range []string{resp.AgentId, flowsKey(resp.AgentId)} {
		if _, _, err := a.st.LoadSnapshot(a.ctx, k); err == nil {
			t.Errorf("snapshot %s survived the organisation", k)
		}
	}
	if ws, _ := a.st.GetWorkspace(a.ctx, aOrg); ws.Rev != 0 {
		t.Error("the workspace survived")
	}
	if ws, _ := a.st.GetWorkspace(a.ctx, bOrg); ws.Rev != 1 {
		t.Error("another organisation's workspace was touched")
	}
	if _, err := a.st.GetUserByName(a.ctx, "alice"); err != nil {
		t.Error("the person's account must survive the organisation")
	}
	// Who deleted it stays on record.
	evs, _ := a.st.ListAudit(a.ctx, aOrg, 20)
	var seen bool
	for _, e := range evs {
		seen = seen || (e.Action == "org-deleted" && e.Actor == "alice")
	}
	if !seen {
		t.Error("deletion was not recorded")
	}
}

func TestAgentsAreServedByTheirOwnOrganisationsHubOnly(t *testing.T) {
	e := newEnv(t)
	u2 := store.User{ID: "u-two", Username: "two", PasswordHash: "!", CreatedAt: time.Now()}
	_ = e.st.CreateUser(e.ctx, u2)
	if err := e.st.CreateOrg(e.ctx, store.Org{ID: "org-2", Name: "Two", CreatedAt: time.Now(), CreatedBy: u2.ID}, u2.ID); err != nil {
		t.Fatal(err)
	}
	plat := NewPlatform(e.base, nil)
	srv := e.base.NewGRPC(pki.NewServerCerts(e.base.CA, []string{"127.0.0.1"}), plat)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve(l)
	t.Cleanup(srv.Stop)
	r := &rig{env: e, addr: l.Addr().String()}

	c2 := e.base.ForOrg("org-2")
	id1, key1, leaf1 := e.approvedAgent(t, fp)
	e2 := &env{base: e.base, core: c2, st: e.st, now: e.now, ctx: e.ctx}
	id2, key2, leaf2 := e2.approvedAgent(t, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	connect := func(leaf []byte, key any) {
		cert := &tls.Certificate{Certificate: [][]byte{leaf}, PrivateKey: key}
		c := continuumv1.NewAgentServiceClient(r.dial(t, pki.ClientTLS(e.base.CA.Pin(), "127.0.0.1", cert)))
		s, err := c.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Send(&continuumv1.AgentMessage{Msg: &continuumv1.AgentMessage_Hello{Hello: &continuumv1.Hello{AgentVersion: "t"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	connect(leaf1, key1)
	connect(leaf2, key2)

	t1, _ := plat.Tenant(ctx, "org-1")
	t2, _ := plat.Tenant(ctx, "org-2")
	has := func(h *Hub, id string) bool { h.mu.Lock(); defer h.mu.Unlock(); return h.views[id] != nil }
	if !has(t1.Hub, id1) || has(t1.Hub, id2) || !has(t2.Hub, id2) || has(t2.Hub, id1) {
		t.Fatalf("hubs mixed the tenants up: org-1 has %v/%v, org-2 has %v/%v", has(t1.Hub, id1), has(t1.Hub, id2), has(t2.Hub, id1), has(t2.Hub, id2))
	}
	// The certificate carries its organisation and renewal keeps it, through the platform-wide core.
	d, _ := csr(t)
	renewed, _, err := e.base.Renew(ctx, id2, d)
	if err != nil {
		t.Fatal(err)
	}
	if o := certOrg(t, renewed); o != "org-2" {
		t.Fatalf("renewed certificate names %q", o)
	}
	// A revocation in one organisation cannot be ordered from another.
	if err := e.core.Revoke(ctx, "boss", id2, "not yours"); kindOf(err) != KindNotFound {
		t.Fatalf("org-1 revoking org-2's agent: %v", err)
	}
	if err := e.core.Reject(ctx, "boss", id2, "not yours"); kindOf(err) != KindNotFound {
		t.Fatalf("org-1 rejecting org-2's agent: %v", err)
	}
}

func TestRejoinKeepsTheAgentsOwnOrganisation(t *testing.T) {
	e := newEnv(t)
	id, key, leaf := e.approvedAgent(t, fp)
	// A certificate that names another organisation than the agent belongs to is refused, even though our CA signed it.
	parsed, _ := pki.ParseCSR(csrWith(t, key))
	wrong, _, _ := e.core.CA.IssueAgent(parsed, id, "org-9", time.Hour)
	*e.now = e.now.Add(2 * time.Hour)
	if _, err := e.base.Rejoin(e.ctx, "10.0.0.1", &continuumv1.RejoinRequest{ExpiredLeafDer: wrong, CsrDer: csrWith(t, key)}); kindOf(err) != KindUnauthenticated {
		t.Fatalf("certificate for the wrong organisation: %v", err)
	}
	resp, err := e.base.Rejoin(e.ctx, "10.0.0.1", &continuumv1.RejoinRequest{ExpiredLeafDer: leaf, CsrDer: csrWith(t, key)})
	if err != nil {
		t.Fatal(err)
	}
	if o := certOrg(t, resp.LeafDer); o != "org-1" {
		t.Fatalf("rejoined certificate names %q", o)
	}
}

func TestMigrationOfALegacySingleOrganisationDatabase(t *testing.T) {
	// Covered where the schema lives; here we check the bootstrap path on a fresh database stays whole.
	a := adminRigOn(t, newEnvBare(t))
	created, pw, err := a.base.BootstrapAdmin(a.ctx, "")
	if err != nil || !created || pw == "" {
		t.Fatalf("%v %v", created, err)
	}
	c := a.login(t, "admin", pw)
	me := a.do("GET", "/api/v1/auth/me", nil, withCookie(c)).json(t)
	orgs := me["orgs"].([]any)
	if len(orgs) != 1 || orgs[0].(map[string]any)["role"] != RoleOwner || orgs[0].(map[string]any)["id"] != "org-1" {
		t.Fatalf("bootstrap organisation = %v", orgs)
	}
	var _ context.Context = a.ctx
}

func certOrg(t *testing.T, der []byte) string {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil || len(c.Subject.Organization) != 1 {
		t.Fatalf("certificate: %v %v", c, err)
	}
	return c.Subject.Organization[0]
}
