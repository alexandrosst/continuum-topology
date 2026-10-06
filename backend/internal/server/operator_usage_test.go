package server

import (
	"strings"
	"testing"
)

// What depends on an operator is shown on it, and revoking or deleting it then needs force; the audit entry says what
// was knowingly left behind.
func TestRevokeAndDeleteAnOperatorSomethingSendsToNeedForce(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	clA := a.approvedCluster(t, fp)
	target := a.createOperatorDoc(t, cookie, extBody("upstream", clA))["operator"].(map[string]any)["id"].(string)
	if _, has := a.do("GET", "/api/v1/operators/"+target, nil, withCookie(cookie)).json(t)["usedBy"]; has {
		t.Fatal("usedBy on an operator nothing sends to")
	}

	// Another operator exports into it, and two intents (one by route) send a cluster to it.
	clB := a.approvedCluster(t, fp2)
	dest := map[string]any{"kind": "operator", "targetOperatorId": target}
	down := a.createOperatorDoc(t, cookie, map[string]any{"name": "downstream", "sourceClusterIds": []string{clB}, "destination": dest})["operator"].(map[string]any)["id"].(string)
	agent := a.approvedAgentID(t, "5b8e1f2a-3333-4333-8444-955566667788")
	if r := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agent, "name": "i", "destination": dest}, withCookie(cookie)); r.Code != 201 {
		t.Fatalf("intent: %d %s", r.Code, r.Body.String())
	}

	doc := a.do("GET", "/api/v1/operators/"+target, nil, withCookie(cookie)).json(t)
	used, _ := doc["usedBy"].(map[string]any)
	ops, _ := used["operators"].([]any)
	if len(ops) != 1 || ops[0].(map[string]any)["id"] != down || ops[0].(map[string]any)["name"] != "downstream" || used["intents"] != float64(1) || used["clusters"] != float64(1) {
		t.Fatalf("usedBy = %v", used)
	}
	// The list carries it too, and is computed once, not once per operator.
	for _, o := range a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).jsonArray(t) {
		if (o["id"] == target) != (o["usedBy"] != nil) {
			t.Fatalf("usedBy in the list: %v", o)
		}
	}

	r := a.do("POST", "/api/v1/operators/"+target+"/revoke", map[string]any{"reason": "retire"}, withCookie(cookie))
	if r.Code != 409 || !strings.Contains(r.Body.String(), "downstream") || !strings.Contains(r.Body.String(), "1 telemetry intent on 1 cluster") {
		t.Fatalf("revoke without force: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("DELETE", "/api/v1/operators/"+target, nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("delete without force: %d %s", r.Code, r.Body.String())
	}
	if st, _ := a.st.GetOperator(a.ctx, target); st.Status != "active" {
		t.Fatal("the operator was revoked without force")
	}

	r = a.do("POST", "/api/v1/operators/"+target+"/revoke", map[string]any{"reason": "retire", "force": true}, withCookie(cookie))
	if r.Code != 200 || r.json(t)["uninstall"] != "helm uninstall "+target+" --namespace continuum-system" {
		t.Fatalf("forced revoke: %d %s", r.Code, r.Body.String())
	}
	if n, detail := (&env{st: a.st, ctx: a.ctx}).countAudit(t, "operator-revoked"); n != 1 || detail != "retire (forced; operators=1 intents=1 clusters=1)" {
		t.Fatalf("audit: %d %q", n, detail)
	}
	if r := a.do("DELETE", "/api/v1/operators/"+target+"?force=true", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("forced delete: %d %s", r.Code, r.Body.String())
	}
	if n, detail := (&env{st: a.st, ctx: a.ctx}).countAudit(t, "operator-deleted"); n != 1 || detail != "forced; operators=1 intents=1 clusters=1" {
		t.Fatalf("audit: %d %q", n, detail)
	}
}

// A revoked intent and a revoked operator grant nothing, so they do not keep an operator in use.
func TestOnlyActiveDependentsCountTowardUsedBy(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	clA := a.approvedCluster(t, fp)
	target := a.createOperatorDoc(t, cookie, extBody("upstream", clA))["operator"].(map[string]any)["id"].(string)
	dest := map[string]any{"kind": "operator", "targetOperatorId": target}
	agent := a.approvedAgentID(t, fp2)
	iid := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agent, "name": "i", "destination": dest}, withCookie(cookie)).json(t)["id"].(string)
	down := a.createOperatorDoc(t, cookie, map[string]any{"name": "downstream", "sourceClusterIds": []string{clA}, "destination": dest})["operator"].(map[string]any)["id"].(string)
	a.do("POST", "/api/v1/telemetry-intents/"+iid+"/revoke", map[string]any{}, withCookie(cookie))
	if r := a.do("POST", "/api/v1/operators/"+down+"/revoke", map[string]any{"reason": "x"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("revoking the downstream operator: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/operators/"+target+"/revoke", map[string]any{"reason": "x"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("revoking an operator nothing active sends to: %d %s", r.Code, r.Body.String())
	}
}

// An intent that sends two signal types to one operator by route counts once, and a cluster counts once however many
// intents it has there.
func TestAnIntentWithSeveralRoutesToOneOperatorCountsOnce(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	target := a.createOperatorDoc(t, cookie, extBody("upstream", cl))["operator"].(map[string]any)["id"].(string)
	agent := a.approvedAgentID(t, fp2)
	dest := map[string]any{"kind": "operator", "targetOperatorId": target}
	r := a.do("POST", "/api/v1/telemetry-intents", map[string]any{
		"agentId": agent, "name": "i", "destination": map[string]any{"kind": "external", "endpoint": "c:4317"},
		"signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}, {"id": "systemLogs", "source": "builtin"}},
		"routes":  map[string]any{"metrics": dest, "logs": dest},
	}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("intent: %d %s", r.Code, r.Body.String())
	}
	used, _ := a.do("GET", "/api/v1/operators/"+target, nil, withCookie(cookie)).json(t)["usedBy"].(map[string]any)
	if used["intents"] != float64(1) || used["clusters"] != float64(1) {
		t.Fatalf("usedBy = %v", used)
	}
	for _, d := range a.do("GET", "/api/v1/operator-destinations", nil, withCookie(cookie)).jsonArray(t) {
		if d["id"] == target && d["usedBy"] != float64(1) {
			t.Fatalf("destination usedBy = %v", d["usedBy"])
		}
	}
}
