package server

import (
	"testing"
)

func TestOperatorsHTTPCreateListGetRevokeDelete(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)

	if r := a.do("GET", "/api/v1/operators", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("list (empty): %d %s", r.Code, r.Body.String())
	} else if got := r.jsonArray(t); len(got) != 0 {
		t.Fatalf("expected no operators yet, got %v", got)
	}

	body := map[string]any{
		"name":             "athens-regional",
		"sourceClusterIds": []string{cl},
		"destination":      map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}
	r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	created := r.json(t)
	token, _ := created["token"].(string)
	if token == "" {
		t.Fatalf("no token in create response: %v", created)
	}
	op, _ := created["operator"].(map[string]any)
	id, _ := op["id"].(string)
	if id == "" || op["status"] != "active" || op["name"] != "athens-regional" {
		t.Fatalf("create response operator = %v", op)
	}
	install, _ := created["install"].(string)
	if install == "" {
		t.Fatalf("no install command in create response: %v", created)
	}
	reminders, _ := created["reminders"].([]any)
	if len(reminders) != 1 {
		t.Fatalf("expected one source-cluster reminder, got %v", reminders)
	}

	list := a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).jsonArray(t)
	if len(list) != 1 || list[0]["id"] != id {
		t.Fatalf("list after create = %v", list)
	}
	if _, leaked := list[0]["token"]; leaked {
		t.Fatalf("the list must never carry the receiver token: %v", list[0])
	}

	if r := a.do("GET", "/api/v1/operators/"+id, nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("get: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["name"]; got != "athens-regional" {
		t.Fatalf("get name = %v", got)
	}

	scopeBody := map[string]any{
		"sourceClusterIds": []string{cl},
		"destination":      map[string]any{"kind": "external", "endpoint": "collector2.example:4317"},
	}
	if r := a.do("POST", "/api/v1/operators/"+id+"/scope", scopeBody, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("update scope: %d %s", r.Code, r.Body.String())
	} else {
		op, _ := r.json(t)["operator"].(map[string]any)
		dest, _ := op["destination"].(map[string]any)
		if dest["endpoint"] != "collector2.example:4317" {
			t.Fatalf("scope not updated: %v", op)
		}
	}

	if r := a.do("POST", "/api/v1/operators/"+id+"/revoke", map[string]string{"reason": "decommissioned"}, withCookie(cookie)); r.Code != 204 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/operators/"+id, nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("get after revoke: %d %s", r.Code, r.Body.String())
	} else if got := r.json(t)["status"]; got != "revoked" {
		t.Fatalf("status after revoke = %v", got)
	}

	if r := a.do("DELETE", "/api/v1/operators/"+id, nil, withCookie(cookie)); r.Code != 204 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/operators/"+id, nil, withCookie(cookie)); r.Code != 404 {
		t.Fatalf("get after delete: %d %s", r.Code, r.Body.String())
	}
}

func TestOperatorsHTTPRequiresAdminRole(t *testing.T) {
	a := newAdminRig(t)
	_, viewer := a.user(t, "jamie", RoleViewer)
	cl := a.approvedCluster(t, fp)
	body := map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
	if r := a.do("POST", "/api/v1/operators", body, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("viewer creating an operator: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("GET", "/api/v1/operators", nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("viewer listing operators: %d %s", r.Code, r.Body.String())
	}
}

func TestOperatorsHTTPRejectsBadDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "operator", "targetOperatorId": "op-other"}}
	if r := a.do("POST", "/api/v1/operators", body, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("chained destination: %d %s", r.Code, r.Body.String())
	}
}
