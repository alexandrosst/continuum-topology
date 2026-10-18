package server

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"continuum/internal/accounts"
)

// Organisation scope is a convention here: the store takes raw ids, and every handler has to look the id up through an
// organisation check (AgentInOrg and its siblings). A handler that forgets would let an administrator of one organisation read
// or change another's entity by naming its id through their own organisation's path.
//
// This test is the pin for that. Bob, who administers his own organisation, calls every organisation-scoped route that takes an id,
// through his own organisation, with the id of Alice's real entity. Each must answer 404 as if there were no such thing, nothing of
// Alice's may change, and nothing may reach Bob's audit trail. The routes are read from testdata/routes.txt, so a new
// route with an id that has no case below (or a reason for having none) fails the test.
//
// The other door, Bob using Alice's organisation path, is not a member check on an id and is covered by
// TestNobodyReachesAnotherOrganisation.
func TestAnotherOrganisationsIdIsNotFoundOnEveryRouteThatTakesAnId(t *testing.T) {
	a := newAdminRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	bob, bOrg := a.register(t, "bob", "Bob Works")
	a.core = a.base.ForOrg(aOrg) // the fixtures below are made through Alice's organisation's own Core

	// Alice's entities, one of each kind that has an id in a route.
	agent, _, _ := a.approvedAgent(t, fp)
	a.core = a.base.ForOrg(aOrg)
	var (
		operator = a.do("POST", org(aOrg, "operators"), extBody("alice-op"), withCookie(alice)).json(t)["operator"].(map[string]any)["id"].(string)
		token    = a.do("POST", org(aOrg, "tokens"), map[string]any{"name": "edge", "tier": 1}, withCookie(alice)).json(t)["meta"].(map[string]any)["id"].(string)
		invite   = func() string {
			if r := a.do("POST", org(aOrg, "invites"), map[string]string{"role": accounts.RoleViewer, "label": "carol"}, withCookie(alice)); r.Code != 201 {
				t.Fatalf("invite as viewer: %d %s", r.Code, r.Body.String())
			}
			return a.do("GET", org(aOrg, "invites"), nil, withCookie(alice)).jsonArray(t)[0]["id"].(string)
		}()
		member   = a.do("GET", "/api/v1/auth/me", nil, withCookie(alice)).json(t)["user"].(map[string]any)["id"].(string)
		backend  = a.savedBackend(t, "jaeger", "traces")
		intent   = a.do("POST", org(aOrg, "telemetry-intents"), map[string]any{"agentId": agent, "name": "edge", "namespaces": []string{"checkout"}, "signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}}, "destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"}}, withCookie(alice)).json(t)["id"].(string)
		proposal = a.do("POST", org(aOrg, "proposals"), map[string]any{"advisor": "rules-v1", "modelVersion": 1, "change": map[string]any{"agentId": agent, "pause": []string{"flow"}}, "evidence": []map[string]any{{"name": "flow bytes", "value": "1 MB/s", "class": "measured"}}, "predicted": map[string]any{"link_bytes_per_sec": -0.5}}, withCookie(alice)).json(t)["id"].(string)
	)
	for kind, id := range map[string]string{"agent": agent, "operator": operator, "token": token, "invite": invite, "member": member, "backend": backend, "intent": intent, "proposal": proposal} {
		if id == "" {
			t.Fatalf("no %s was made for Alice", kind)
		}
	}
	ids := map[string]string{"agent": agent, "operator": operator, "token": token, "invite": invite, "member": member, "backend": backend, "intent": intent, "proposal": proposal}

	// What each route's id names, and the body it is sent (some routes validate a body before they look the id up).
	type call struct {
		kind string
		body any
	}
	scope := map[string]any{"sourceClusterIds": []string{}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
	cases := map[string]call{
		"POST /agents/{id}/approve":                {"agent", map[string]any{"code": "000000"}},
		"POST /agents/{id}/reject":                 {"agent", map[string]any{}},
		"POST /agents/{id}/revoke":                 {"agent", map[string]any{"reason": "x"}},
		"POST /agents/{id}/tier":                   {"agent", map[string]any{"tier": 0}},
		"POST /agents/{id}/consent":                {"agent", map[string]any{"pausedCollectors": []string{"flow"}}},
		"GET /agents/{id}/address-tests":           {"agent", nil},
		"POST /agents/{id}/address-tests":          {"agent", map[string]any{"addresses": []string{"203.0.113.9:443"}}},
		"GET /operators/{id}":                      {"operator", nil},
		"GET /operators/{id}/senders":              {"operator", nil},
		"GET /operators/{id}/certificates":         {"operator", nil},
		"GET /operators/{id}/address-candidates":   {"operator", nil},
		"POST /operators/{id}/install":             {"operator", nil},
		"POST /operators/{id}/revoke":              {"operator", map[string]any{"reason": "x", "force": true}},
		"DELETE /operators/{id}":                   {"operator", nil},
		"POST /operators/{id}/scope":               {"operator", scope},
		"POST /operators/{id}/address":             {"operator", map[string]any{"address": "x.example.com:4317"}},
		"POST /operators/{id}/address/check":       {"operator", nil},
		"POST /operators/{id}/heartbeat":           {"operator", nil},
		"PUT /operators/{id}/server-cluster":       {"operator", map[string]any{}},
		"GET /telemetry-intents/{id}":              {"intent", nil},
		"DELETE /telemetry-intents/{id}":           {"intent", nil},
		"POST /telemetry-intents/{id}/scope":       {"intent", map[string]any{"namespaces": []string{"x"}, "signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}}}},
		"POST /telemetry-intents/{id}/destination": {"intent", map[string]any{"destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}},
		"POST /telemetry-intents/{id}/revoke":      {"intent", map[string]any{"reason": "x"}},
		"POST /telemetry-intents/{id}/command":     {"intent", nil},
		"GET /proposals/{id}":                      {"proposal", nil},
		"POST /proposals/{id}/accept":              {"proposal", map[string]any{}},
		"POST /proposals/{id}/reject":              {"proposal", map[string]any{}},
		"DELETE /tokens/{id}":                      {"token", nil},
		"DELETE /invites/{id}":                     {"invite", nil},
		"POST /members/{id}/role":                  {"member", map[string]any{"role": accounts.RoleViewer}},
		"POST /members/{id}/remove":                {"member", nil},
		"GET /quick-start/{id}/gateway-token":      {"backend", nil},
		"POST /quick-start/{id}/gateway-token":     {"backend", map[string]any{}},
	}
	// Routes with an id that have no case, and why. A reason that stops being true is a reason to add the case.
	exempt := map[string]string{
		"DELETE /fusion/tokens/{id}":          "FUSION belongs to the main organisation only (fusionOrgOnly): neither of two ordinary organisations has one, so there is no token of Alice's to name",
		"GET /history/dependency/{id}/series": "the id selects a series inside the caller's own organisation's history; another organisation's id finds nothing there by construction, and no entity of Alice's is addressed",
	}

	// Every route of the table that takes an id is either a case or exempt.
	want := routesWithAnId(t)
	for _, k := range want {
		if _, ok := cases[k]; ok {
			continue
		}
		if _, ok := exempt[k]; ok {
			continue
		}
		t.Errorf("%s takes an id and has no case in this test and no reason to be exempt: add one", k)
	}
	have := map[string]bool{}
	for _, k := range want {
		have[k] = true
	}
	for k := range cases {
		if !have[k] {
			t.Errorf("the case %q is for a route that no longer exists", k)
		}
	}
	for k := range exempt {
		if !have[k] {
			t.Errorf("the exemption %q is for a route that no longer exists", k)
		}
	}

	// What Alice's side looks like before Bob tries anything.
	snapshot := func() string {
		var b strings.Builder
		for _, p := range []string{"members", "invites", "tokens", "proposals", "operators", "telemetry-intents", "settings", "state"} {
			r := a.do("GET", org(aOrg, p), nil, withCookie(alice))
			fmt.Fprintf(&b, "%s %d %s\n", p, r.Code, r.Body.String())
		}
		ag, _ := a.st.GetAgent(a.ctx, agent)
		fmt.Fprintf(&b, "agent %s tier=%d consent=%+v\n", ag.Status, ag.AccessTier, a.hub().consentOf(agent))
		return b.String()
	}
	trail := func(orgID string) int {
		evs, err := a.st.ListAudit(a.ctx, orgID, 1000)
		if err != nil {
			t.Fatal(err)
		}
		return len(evs)
	}
	before, bobTrail := snapshot(), trail(bOrg)

	keys := make([]string, 0, len(cases))
	for k := range cases {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		c := cases[k]
		method, path, _ := strings.Cut(k, " ")
		path = strings.TrimPrefix(strings.ReplaceAll(path, "{id}", ids[c.kind]), "/")
		if got := a.do(method, org(bOrg, path), c.body, withCookie(bob), fromIP(fmt.Sprintf("10.31.0.%d", i+1))).Code; got != 404 {
			t.Errorf("Bob %s through his own organisation with Alice's %s: %d, want 404", k, c.kind, got)
		}
	}

	if after := snapshot(); after != before {
		t.Errorf("something of Alice's changed while Bob tried her ids:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if n := trail(bOrg); n != bobTrail {
		t.Errorf("Bob's audit trail grew from %d to %d rows from calls that were refused", bobTrail, n)
	}
}

// routesWithAnId reads, from the pinned route table, every organisation-scoped route whose path has a parameter, as "METHOD /path".
func routesWithAnId(t *testing.T) []string {
	t.Helper()
	const prefix = "/api/v1/orgs/{org}"
	raw, err := os.ReadFile("testdata/routes.txt")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		_, pattern, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		method, path, ok := strings.Cut(pattern, " ")
		if !ok || !strings.HasPrefix(path, prefix+"/") || !strings.Contains(path[len(prefix):], "{") {
			continue
		}
		out = append(out, method+" "+path[len(prefix):])
	}
	if len(out) < 30 {
		t.Fatalf("read %d routes with an id from the table: the reader no longer finds them", len(out))
	}
	return out
}
