package server

import (
	"testing"
)

func TestRecordDecisionsNeedsAnEditorAndListDecisionsIsReadableByAnyMember(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "ed", RoleEditor)
	_, viewer := a.user(t, "vi", RoleViewer)

	body := map[string]any{
		"entries": []map[string]any{{
			"deciderId": "baseline", "deciderName": "Baseline (weighted cost)", "deciderKind": "builtin",
			"input": map[string]any{"schema": 1, "clusterCount": 3, "serviceCount": 10, "policy": map[string]any{"latency": 1}},
			"moves": []map[string]any{{
				"serviceId": "api", "serviceName": "api", "from": "cl-edge", "to": "cl-cloud", "reason": "cheaper there",
				"benefit": 12.5, "confidence": "high", "verdict": "fits", "beforeCost": 40, "afterCost": 27.5, "migrationCost": 0,
			}},
		}},
	}

	// A viewer may not add to the log: the frontend computed this recommendation for them to look at,
	// not to write a permanent record with their name on it.
	if r := a.do("POST", "/api/v1/decisions", body, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer recorded a decision: %d %s", r.Code, r.Body.String())
	}
	if got := a.do("GET", "/api/v1/decisions", nil, withCookie(viewer)).json(t)["decisions"]; got != nil {
		if l, ok := got.([]any); !ok || len(l) != 0 {
			t.Fatalf("a viewer's rejected post still landed: %v", got)
		}
	}

	if r := a.do("POST", "/api/v1/decisions", body, withCookie(editor)); r.Code != 201 {
		t.Fatalf("an editor was refused: %d %s", r.Code, r.Body.String())
	}

	// Any member, including the viewer who could not write, can read the log back.
	list := a.do("GET", "/api/v1/decisions", nil, withCookie(viewer)).json(t)["decisions"].([]any)
	if len(list) != 1 {
		t.Fatalf("want 1 recorded decision, got %d: %v", len(list), list)
	}
	row := list[0].(map[string]any)
	if row["deciderId"] != "baseline" || row["deciderKind"] != "builtin" || row["serviceId"] != "api" || row["to"] != "cl-cloud" ||
		row["benefit"] != 12.5 || row["confidence"] != "high" || row["verdict"] != "fits" || row["beforeCost"] != float64(40) || row["afterCost"] != 27.5 {
		t.Fatalf("recorded row does not match what was sent: %+v", row)
	}
	if row["recordedBy"] != "ed" {
		t.Fatalf("recordedBy should be whoever called it: %+v", row)
	}
}

func TestRecordDecisionsIsScopedPerOrganisation(t *testing.T) {
	a := newAdminRig(t)
	_, owner := a.user(t, "boss", RoleOwner)
	// A second organisation, so the same decision log endpoint for org-2 can never see org-1's rows.
	r := a.do("POST", "/api/v1/orgs", map[string]any{"name": "Org Two"}, withCookie(owner))
	if r.Code != 201 {
		t.Fatalf("create org two: %d %s", r.Code, r.Body.String())
	}
	org2, _ := r.json(t)["id"].(string)
	if org2 == "" {
		t.Fatal("org two was not created")
	}

	one := map[string]any{"entries": []map[string]any{{
		"deciderId": "baseline", "deciderKind": "builtin",
		"input": map[string]any{"schema": 1},
		"moves": []map[string]any{{"serviceId": "svc-org1", "to": "cl-a", "benefit": 1}},
	}}}
	two := map[string]any{"entries": []map[string]any{{
		"deciderId": "baseline", "deciderKind": "builtin",
		"input": map[string]any{"schema": 1},
		"moves": []map[string]any{{"serviceId": "svc-org2", "to": "cl-b", "benefit": 2}},
	}}}

	if r := a.do("POST", "/api/v1/decisions", one, withCookie(owner)); r.Code != 201 {
		t.Fatalf("org-1 record: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/orgs/"+org2+"/decisions", two, withCookie(owner)); r.Code != 201 {
		t.Fatalf("org-2 record: %d %s", r.Code, r.Body.String())
	}

	org1List := a.do("GET", "/api/v1/decisions", nil, withCookie(owner)).json(t)["decisions"].([]any)
	org2List := a.do("GET", "/api/v1/orgs/"+org2+"/decisions", nil, withCookie(owner)).json(t)["decisions"].([]any)
	if len(org1List) != 1 || org1List[0].(map[string]any)["serviceId"] != "svc-org1" {
		t.Fatalf("org 1's list is wrong, or leaked org 2's row: %v", org1List)
	}
	if len(org2List) != 1 || org2List[0].(map[string]any)["serviceId"] != "svc-org2" {
		t.Fatalf("org 2's list is wrong, or leaked org 1's row: %v", org2List)
	}
}

// TestRecordDecisionsRejectsMalformedOrOversizedInput covers the server-side bounds that stand even if
// the frontend's own caps (MAX_MOVES, the number of configured deciders) are somehow bypassed.
func TestRecordDecisionsRejectsMalformedOrOversizedInput(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "ed", RoleEditor)

	// A move missing the fields that make it one at all is silently dropped, not an error - the caller
	// does not need to pre-filter whatever a decider happened to propose and have rejected elsewhere.
	noop := map[string]any{"entries": []map[string]any{{
		"deciderId": "baseline", "deciderKind": "builtin", "input": map[string]any{},
		"moves": []map[string]any{{"serviceId": "", "to": "cl-a"}, {"serviceId": "svc", "to": ""}},
	}}}
	if r := a.do("POST", "/api/v1/decisions", noop, withCookie(editor)); r.Code != 201 {
		t.Fatalf("empty-looking moves: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/decisions", nil, withCookie(editor)); len(r.json(t)["decisions"].([]any)) != 0 {
		t.Fatalf("a move with no service or target was recorded anyway: %s", r.Body.String())
	}

	// An unknown decider kind is refused outright: builtin/external is a closed set.
	bad := map[string]any{"entries": []map[string]any{{"deciderId": "x", "deciderKind": "made-up", "input": map[string]any{}}}}
	if r := a.do("POST", "/api/v1/decisions", bad, withCookie(editor)); r.Code != 400 {
		t.Fatalf("made-up decider kind: %d %s", r.Code, r.Body.String())
	}

	// Too many entries in one call is refused, not silently truncated.
	var many []map[string]any
	for i := 0; i < maxDecisionEntries+1; i++ {
		many = append(many, map[string]any{"deciderId": "x", "deciderKind": "builtin", "input": map[string]any{}})
	}
	if r := a.do("POST", "/api/v1/decisions", map[string]any{"entries": many}, withCookie(editor)); r.Code != 400 {
		t.Fatalf("too many entries: %d %s", r.Code, r.Body.String())
	}
}
