package server

import (
	"testing"
	"time"

	"continuum/internal/accounts"
	"continuum/internal/store"
)

func TestAPredictionHeldWhenEveryObservedMetricWentTheWayAndByHalfAsMuch(t *testing.T) {
	for _, c := range []struct {
		name                   string
		predicted, observed    string
		wantHeld, wantCompared bool
	}{
		{"exactly as predicted", `{"agent_cpu_cores":-0.3}`, `{"change":{"agent_cpu_cores":-0.3}}`, true, true},
		{"more than predicted", `{"agent_cpu_cores":-0.3}`, `{"change":{"agent_cpu_cores":-0.8}}`, true, true},
		{"just half", `{"agent_cpu_cores":-0.4}`, `{"change":{"agent_cpu_cores":-0.2}}`, true, true},
		{"under half", `{"agent_cpu_cores":-0.4}`, `{"change":{"agent_cpu_cores":-0.19}}`, false, true},
		{"the wrong way", `{"agent_cpu_cores":-0.3}`, `{"change":{"agent_cpu_cores":0.1}}`, false, true},
		{"an increase predicted and seen", `{"agent_rss_bytes":0.2}`, `{"change":{"agent_rss_bytes":0.25}}`, true, true},
		{"one of two missed", `{"agent_cpu_cores":-0.3,"link_bytes_per_sec":-0.5}`, `{"change":{"agent_cpu_cores":-0.3,"link_bytes_per_sec":0.1}}`, false, true},
		{"a metric that could not be observed is left out", `{"agent_cpu_cores":-0.3,"link_bytes_per_sec":-0.5}`, `{"change":{"agent_cpu_cores":-0.3}}`, true, true},
		{"nothing predicted could be observed", `{"link_bytes_per_sec":-0.5}`, `{"change":{"agent_cpu_cores":-0.3}}`, false, false},
		{"nothing predicted", `{}`, `{"change":{"agent_cpu_cores":-0.3}}`, false, false},
		{"unreadable", `nope`, `{}`, false, false},
	} {
		held, compared := heldPrediction([]byte(c.predicted), []byte(c.observed))
		if held != c.wantHeld || compared != c.wantCompared {
			t.Errorf("%s: held=%v compared=%v, want %v %v", c.name, held, compared, c.wantHeld, c.wantCompared)
		}
	}
}

func TestEachAdvisorsRecordCountsItsProposalsDecisionsAndPredictions(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	_, viewer := a.user(t, "vic", accounts.RoleViewer)
	id, _, _ := a.approvedAgent(t, fp)

	submit := func(advisor string, change map[string]any) string {
		t.Helper()
		change["agentId"] = id
		r := a.do("POST", "/api/v1/proposals", map[string]any{"advisor": advisor, "change": change,
			"evidence":  []map[string]any{{"name": "cpu", "value": "0.6", "class": "measured"}},
			"predicted": map[string]any{"agent_cpu_cores": -0.4}}, withCookie(editor))
		if r.Code != 201 {
			t.Fatalf("submit: %d %s", r.Code, r.Body.String())
		}
		return r.json(t)["id"].(string)
	}
	decide := func(pid, verb string) {
		t.Helper()
		if c := a.do("POST", "/api/v1/proposals/"+pid+"/"+verb, map[string]any{}, withCookie(editor)).Code; c != 200 {
			t.Fatalf("%s %s: %d", verb, pid, c)
		}
	}
	first := submit("rules-v1", map[string]any{"pause": []string{"flow"}})
	second := submit("rules-v1", map[string]any{"pause": []string{"probes"}})
	third := submit("rules-v1", map[string]any{"exclude": []string{"shop"}})
	submit("rules-v1", map[string]any{"pause": []string{"measure"}}) // left waiting
	submit("cpu-guard", map[string]any{"exclude": []string{"batch"}})
	decide(first, "accept")
	decide(second, "accept")
	decide(third, "reject")

	// The first change did what was said, the second did the opposite; the Outcomes are written by the hub in a real run.
	rec := func(pid, observed string) {
		t.Helper()
		err := a.st.RecordProposalOutcome(a.ctx, store.ProposalOutcome{ProposalID: pid, OrgID: "org-1", Result: "measured",
			Predicted: []byte(`{"agent_cpu_cores":-0.4}`), Observed: []byte(observed), RecordedAt: time.Now()})
		if err != nil {
			t.Fatal(err)
		}
	}
	rec(first, `{"change":{"agent_cpu_cores":-0.5}}`)
	rec(second, `{"change":{"agent_cpu_cores":0.1}}`)

	if c := a.do("GET", "/api/v1/proposals/advisors", nil, withCookie(viewer)).Code; c != 403 {
		t.Fatalf("a viewer read the advisors: %d", c)
	}
	if c := a.do("GET", "/api/v1/proposals/advisors", nil).Code; c != 401 {
		t.Fatalf("not signed in: %d", c)
	}
	r := a.do("GET", "/api/v1/proposals/advisors", nil, withCookie(editor))
	if r.Code != 200 {
		t.Fatalf("advisors: %d %s", r.Code, r.Body.String())
	}
	list, _ := r.json(t)["advisors"].([]any)
	if len(list) != 2 {
		t.Fatalf("two advisors expected: %v", list)
	}
	num := func(m map[string]any, k string) int { f, _ := m[k].(float64); return int(f) }
	rules, guard := list[1].(map[string]any), list[0].(map[string]any) // by name: cpu-guard, rules-v1
	if rules["advisor"] != "rules-v1" || guard["advisor"] != "cpu-guard" {
		t.Fatalf("sorted by name: %v", list)
	}
	for k, want := range map[string]int{"proposals": 4, "pending": 1, "accepted": 2, "rejected": 1, "failed": 0, "expired": 0, "measured": 2, "held": 1, "missed": 1, "unconfirmed": 0, "noData": 0} {
		if got := num(rules, k); got != want {
			t.Errorf("rules-v1 %s = %d, want %d (%v)", k, got, want, rules)
		}
	}
	for k, want := range map[string]int{"proposals": 1, "pending": 1, "accepted": 0, "rejected": 0, "measured": 0, "held": 0, "missed": 0} {
		if got := num(guard, k); got != want {
			t.Errorf("cpu-guard %s = %d, want %d (%v)", k, got, want, guard)
		}
	}
}

func TestAnOrganisationWithNoProposalsHasNoAdvisors(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "edith", accounts.RoleEditor)
	r := a.do("GET", "/api/v1/proposals/advisors", nil, withCookie(editor))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	if l, ok := r.json(t)["advisors"].([]any); !ok || len(l) != 0 {
		t.Fatalf("expected an empty list, not null: %v", r.Body.String())
	}
}
