package server

import "testing"

func TestTelemetryIntentsHTTPCreateListGetScopeDestinationRevokeDelete(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleEditor)
	agentID := a.approvedAgentID(t, fp)

	if r := a.do("GET", "/api/v1/telemetry-intents", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("list (empty): %d %s", r.Code, r.Body.String())
	} else if got := r.jsonArray(t); len(got) != 0 {
		t.Fatalf("expected no telemetry intents yet, got %v", got)
	}

	body := map[string]any{
		"agentId":     agentID,
		"name":        "patras-edge",
		"namespaces":  []string{"checkout"},
		"signals":     []map[string]any{{"id": "resourceUsage", "source": "builtin"}},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}
	r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	created := r.json(t)
	id, _ := created["id"].(string)
	if id == "" || created["status"] != "active" || created["name"] != "patras-edge" || created["agentId"] != agentID {
		t.Fatalf("create response = %v", created)
	}

	list := a.do("GET", "/api/v1/telemetry-intents?agentId="+agentID, nil, withCookie(cookie)).jsonArray(t)
	if len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("list by agent after create = %v", list)
	}
	list = a.do("GET", "/api/v1/telemetry-intents", nil, withCookie(cookie)).jsonArray(t)
	if len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("org-wide list after create = %v", list)
	}

	if r := a.do("GET", "/api/v1/telemetry-intents/"+id, nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("get: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["name"]; got != "patras-edge" {
		t.Fatalf("get name = %v", got)
	}

	scopeBody := map[string]any{
		"namespaces": []string{"checkout", "payments"},
		"exclude":    []string{"payments-canary"},
		"signals":    []map[string]any{{"id": "traces", "source": "existing"}},
	}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/scope", scopeBody, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("update scope: %d %s", r.Code, r.Body.String())
	} else {
		doc := r.json(t)
		ns, _ := doc["namespaces"].([]any)
		if len(ns) != 2 {
			t.Fatalf("scope not updated: %v", doc)
		}
	}

	destBody := map[string]any{"destination": map[string]any{"kind": "external", "endpoint": "collector2.example:4317"}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/destination", destBody, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("update destination: %d %s", r.Code, r.Body.String())
	} else {
		doc := r.json(t)
		dest, _ := doc["destination"].(map[string]any)
		if dest["endpoint"] != "collector2.example:4317" {
			t.Fatalf("destination not updated: %v", doc)
		}
	}

	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/revoke", map[string]string{"reason": "decommissioned"}, withCookie(cookie)); r.Code != 204 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/telemetry-intents/"+id, nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("get after revoke: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["status"]; got != "revoked" {
		t.Fatalf("status after revoke = %v", got)
	}

	if r := a.do("DELETE", "/api/v1/telemetry-intents/"+id, nil, withCookie(cookie)); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/telemetry-intents/"+id, nil, withCookie(cookie)); r.Code != 404 {
		t.Fatalf("get after delete: %d %s", r.Code, r.Body.String())
	}
}

func TestTelemetryIntentsHTTPRequiresEditorRole(t *testing.T) {
	a := newAdminRig(t)
	_, viewer := a.user(t, "jamie", RoleViewer)
	agentID := a.approvedAgentID(t, fp)
	body := map[string]any{"agentId": agentID, "name": "x", "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
	if r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("viewer creating a telemetry intent: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/telemetry-intents", nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("viewer listing telemetry intents: %d %s", r.Code, r.Body.String())
	}
}

func TestTelemetryIntentsHTTPRejectsASecondActiveIntentOnTheSameAgent(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleEditor)
	agentID := a.approvedAgentID(t, fp)
	body := map[string]any{"agentId": agentID, "name": "first", "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
	if r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(cookie)); r.Code != 201 {
		t.Fatalf("first create: %d %s", r.Code, r.Body.String())
	}
	body["name"] = "second"
	if r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("second create on the same agent: %d %s", r.Code, r.Body.String())
	}
}
