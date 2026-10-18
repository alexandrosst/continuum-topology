package server

import (
	"strings"
	"testing"

	"continuum/internal/accounts"
	"continuum/internal/store"
	"continuum/internal/testkit"
)

func TestTheProposalRoutesAreForEditorsAndAWideningAcceptanceNeedsAnAdministrator(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, admin := a.user(t, "adam", accounts.RoleAdmin)
	_, viewer := a.user(t, "vic", accounts.RoleViewer)
	id, _, _ := a.approvedAgent(t, testkit.FP) // approved at 2, installed at 2

	submit := func(cookie string, body map[string]any) resp {
		return a.do("POST", "/api/v1/proposals", body, withCookie(cookie))
	}
	body := func(change map[string]any) map[string]any {
		change["agentId"] = id
		return map[string]any{"advisor": "rules-v1", "modelVersion": 4, "change": change,
			"evidence":  []map[string]any{{"name": "flow bytes", "value": "12 MB/s", "class": "measured"}},
			"predicted": map[string]any{"link_bytes_per_sec": -0.5}}
	}

	// not signed in, and a viewer: nothing is stored
	if c := a.do("POST", "/api/v1/proposals", body(map[string]any{"pause": []string{"flow"}})).Code; c != 401 {
		t.Fatalf("not signed in: %d", c)
	}
	if c := submit(viewer, body(map[string]any{"pause": []string{"flow"}})).Code; c != 403 {
		t.Fatalf("a viewer submitted: %d", c)
	}
	if c := a.do("GET", "/api/v1/proposals", nil, withCookie(viewer)).Code; c != 403 {
		t.Fatalf("a viewer listed proposals: %d", c)
	}

	// an advisor (an editor's token) submits; nothing about the agent changes
	r := submit(editor, body(map[string]any{"pause": []string{"flow"}}))
	if r.Code != 201 {
		t.Fatalf("submit: %d %s", r.Code, r.Body.String())
	}
	j := r.json(t)
	pid, _ := j["id"].(string)
	if !strings.HasPrefix(pid, "pr-") || j["status"] != "pending" || j["widens"] != false || j["confidence"] != "measured" || j["advisor"] != "rules-v1" {
		t.Fatalf("the proposal: %v", j)
	}
	if c := a.hub().ConsentOf(id); len(c.Paused) != 0 {
		t.Fatalf("submitting paused something: %+v", c)
	}

	// refused submissions
	for name, b := range map[string]map[string]any{
		"an unknown field": {"advisor": "a", "change": map[string]any{"agentId": id, "pause": []string{"flow"}}, "autoApply": true},
		"no change":        body(map[string]any{}),
		"a bad ttl": func() map[string]any {
			m := body(map[string]any{"pause": []string{"flow"}})
			m["ttlSeconds"] = 99999999
			return m
		}(),
	} {
		if c := submit(editor, b).Code; c != 400 {
			t.Errorf("%s: %d", name, c)
		}
	}

	// list, filter and read
	l := a.do("GET", "/api/v1/proposals?status=pending", nil, withCookie(editor)).json(t)
	if ps, _ := l["proposals"].([]any); len(ps) != 1 {
		t.Fatalf("list: %v", l)
	}
	if c := a.do("GET", "/api/v1/proposals?status=nonsense", nil, withCookie(editor)).Code; c != 400 {
		t.Fatalf("a bad status filter: %d", c)
	}
	if c := a.do("GET", "/api/v1/proposals/pr-nope", nil, withCookie(editor)).Code; c != 404 {
		t.Fatalf("an unknown proposal: %d", c)
	}

	// an editor accepts the narrowing; it is applied
	acc := a.do("POST", "/api/v1/proposals/"+pid+"/accept", map[string]any{"note": "ok"}, withCookie(editor))
	if acc.Code != 200 || acc.json(t)["status"] != "applied" {
		t.Fatalf("accept: %d %s", acc.Code, acc.Body.String())
	}
	if c := a.hub().ConsentOf(id); strings.Join(c.Paused, ",") != "flow" {
		t.Fatalf("overrides: %+v", c)
	}
	if c := a.do("POST", "/api/v1/proposals/"+pid+"/accept", map[string]any{}, withCookie(editor)).Code; c != 409 {
		t.Fatalf("accepting twice: %d", c)
	}

	// lifting the pause widens: an editor is refused, an administrator is not
	w := submit(editor, body(map[string]any{"resume": []string{"flow"}}))
	if w.Code != 201 || w.json(t)["widens"] != true {
		t.Fatalf("a widening proposal: %d %s", w.Code, w.Body.String())
	}
	wid := w.json(t)["id"].(string)
	if c := a.do("POST", "/api/v1/proposals/"+wid+"/accept", map[string]any{}, withCookie(editor)).Code; c != 403 {
		t.Fatalf("an editor accepted a widening: %d", c)
	}
	if c := a.hub().ConsentOf(id); strings.Join(c.Paused, ",") != "flow" {
		t.Fatalf("the refused acceptance changed the overrides: %+v", c)
	}
	if c := a.do("POST", "/api/v1/proposals/"+wid+"/accept", map[string]any{}, withCookie(admin)).Code; c != 200 {
		t.Fatalf("the administrator: %d", c)
	}
	if c := a.hub().ConsentOf(id); len(c.Paused) != 0 {
		t.Fatalf("the pause is still on: %+v", c)
	}

	// reject
	rj := submit(editor, body(map[string]any{"exclude": []string{"shop"}})).json(t)["id"].(string)
	d := a.do("POST", "/api/v1/proposals/"+rj+"/reject", map[string]any{"note": "not now"}, withCookie(editor))
	if d.Code != 200 || d.json(t)["status"] != "rejected" {
		t.Fatalf("reject: %d %s", d.Code, d.Body.String())
	}
	if got, _ := a.st.ListProposals(a.ctx, "org-1", store.ProposalQuery{Status: store.ProposalApplied}); len(got) != 2 {
		t.Fatalf("applied: %+v", got)
	}
}
