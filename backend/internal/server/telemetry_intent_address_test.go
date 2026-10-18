package server

import (
	"strings"
	"testing"

	"continuum/internal/accounts"
)

// intentToOperator makes a regional operator with a recorded address and an intent whose default destination is it.
func intentToOperator(t *testing.T, a *adminRig, cookie string) (opID, intentID string) {
	t.Helper()
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "site-athens", "sourceClusterIds": []string{cl},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"}, "exposure": "nodeport"}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create operator: %d %s", r.Code, r.Body.String())
	}
	opID = r.json(t)["operator"].(map[string]any)["id"].(string)
	if r := a.do("POST", "/api/v1/operators/"+opID+"/address", map[string]any{"address": "recorded.example.com:4317"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("record address: %d %s", r.Code, r.Body.String())
	}
	agentID := a.approvedAgentID(t, fp2)
	r = a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agentID, "name": "patras-edge",
		"destination": map[string]any{"kind": "operator", "targetOperatorId": opID}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	return opID, r.json(t)["id"].(string)
}

func intentFragment(t *testing.T, a *adminRig, cookie, intentID string) (frag string, doc map[string]any) {
	t.Helper()
	r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/command", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("command: %d %s", r.Code, r.Body.String())
	}
	doc = r.json(t)
	frag, _ = doc["installFragment"].(string)
	return frag, doc
}

func TestASenderDialsTheAddressItChoseNotTheOneRecordedForTheOperator(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	opID, intentID := intentToOperator(t, a, cookie)

	before, _ := intentFragment(t, a, cookie, intentID)
	if !strings.Contains(before, "--set telemetry.export.otlp.endpoint=recorded.example.com:4317") {
		t.Fatalf("with no address of its own the sender dials the operator's recorded one: %q", before)
	}

	// The bare host gets the receiver's port and is stored the way a recorded address is.
	body := map[string]any{"destination": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": " 10.20.0.14:31317 "}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/destination", body, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("choose an address: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["destination"].(map[string]any)["address"]; got != "10.20.0.14:31317" {
		t.Fatalf("the chosen address is not kept in its stored form: %v", got)
	}
	after, doc := intentFragment(t, a, cookie, intentID)
	want := strings.Replace(before, "telemetry.export.otlp.endpoint=recorded.example.com:4317", "telemetry.export.otlp.endpoint=10.20.0.14:31317", 1)
	if after != want {
		t.Fatalf("only the endpoint changes; the certificate is still verified by the stable name:\n got %q\nwant %q", after, want)
	}
	// A private address is what a same-site sender is meant to use: it carries no warning of its own about that.
	if w, _ := doc["warnings"].([]any); len(w) != 0 {
		t.Fatalf("warnings for a private address a person chose on purpose: %v", w)
	}

	// Clearing it falls back to the recorded address.
	body = map[string]any{"destination": map[string]any{"kind": "operator", "targetOperatorId": opID}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/destination", body, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("clear: %d %s", r.Code, r.Body.String())
	}
	if again, _ := intentFragment(t, a, cookie, intentID); again != before {
		t.Fatalf("without an address of its own the sender is back on the recorded one: %q", again)
	}
}

func TestAChosenAddressIsHeldToTheSameRulesAsARecordedOne(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	opID, intentID := intentToOperator(t, a, cookie)
	path := "/api/v1/telemetry-intents/" + intentID + "/destination"
	for _, bad := range []string{"https://x.example.com:4317", "localhost:4317", "127.0.0.1:4317", "169.254.169.254:80", "a b:4317", "x.example.com:99999"} {
		body := map[string]any{"destination": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": bad}}
		if r := a.do("POST", path, body, withCookie(cookie)); r.Code != 400 {
			t.Errorf("address %q was accepted: %d %s", bad, r.Code, r.Body.String())
		}
	}
	// An address belongs to a regional-operator destination: an external one is dialled at its endpoint.
	body := map[string]any{"destination": map[string]any{"kind": "external", "endpoint": "c.example:4317", "address": "10.0.0.1:4317"}}
	if r := a.do("POST", path, body, withCookie(cookie)); r.Code != 400 {
		t.Errorf("an address on an external destination: %d", r.Code)
	}
}

func TestEachRouteCarriesItsOwnAddress(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	opID, intentID := intentToOperator(t, a, cookie)
	body := map[string]any{
		"destination": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": "10.20.0.14:31317"},
		"routes":      map[string]any{"logs": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": "10.20.0.9:4317"}},
	}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/destination", body, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("routes: %d %s", r.Code, r.Body.String())
	}
	frag, _ := intentFragment(t, a, cookie, intentID)
	for _, want := range []string{"10.20.0.9:4317", "10.20.0.14:31317"} {
		if !strings.Contains(frag, want) {
			t.Errorf("%s missing from %q", want, frag)
		}
	}
	if strings.Contains(frag, "recorded.example.com") {
		t.Errorf("the recorded address is used although every route chose its own: %q", frag)
	}
}
