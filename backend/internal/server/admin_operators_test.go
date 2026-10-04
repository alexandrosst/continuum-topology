package server

import (
	"strings"
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
	if !strings.Contains(install, "receiver.tls.enabled=true") {
		t.Fatalf("install command does not wire up the receiver's mTLS certificate: %v", install)
	}
	tlsSecretCommand, _ := created["tlsSecretCommand"].(string)
	if tlsSecretCommand == "" || !strings.Contains(tlsSecretCommand, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("no receiver TLS secret command in create response: %v", created)
	}
	// Two lines per source cluster now: the client certificate Secret to create there first, then the
	// helm upgrade that points its exporter at this operator with mTLS turned on.
	reminders, _ := created["reminders"].([]any)
	if len(reminders) != 2 {
		t.Fatalf("expected two source-cluster reminder lines (client cert secret + helm upgrade), got %v", reminders)
	}
	if !strings.Contains(reminders[0].(string), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("first reminder should be the client certificate secret command, got %v", reminders[0])
	}
	if !strings.Contains(reminders[1].(string), "telemetry.export.otlp.tls.mtls.enabled=true") {
		t.Fatalf("second reminder should wire up mTLS on the exporter, got %v", reminders[1])
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

// TestOperatorInstallCommandReleaseNameIsUniquePerOperator guards the chart-side fix in
// continuum-regional-operator (operator.name now derives from .Release.Name, see _helpers.tpl): that fix
// only closes the collision if operatorInstallCommand actually hands each operator a distinct, valid
// Helm release name in the first place, instead of always suggesting the same namespace+release for
// every operator. newOperatorID already mints a short, lowercase, hyphenated id ("op-<12 hex>") per
// operator and operatorInstallCommand uses it as the release name - this pins that down so a future
// change can't quietly go back to a fixed release name.
func TestOperatorInstallCommandReleaseNameIsUniquePerOperator(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)

	create := func(name string) string {
		body := map[string]any{
			"name":             name,
			"sourceClusterIds": []string{cl},
			"destination":      map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
		}
		r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
		if r.Code != 201 {
			t.Fatalf("create %q: %d %s", name, r.Code, r.Body.String())
		}
		install, _ := r.json(t)["install"].(string)
		if install == "" {
			t.Fatalf("no install command for %q", name)
		}
		return install
	}

	installA := create("athens-regional")
	installB := create("corinth-regional")

	releaseOf := func(install string) string {
		t.Helper()
		const marker = "helm install "
		i := strings.Index(install, marker)
		if i < 0 {
			t.Fatalf("install command has no %q: %s", marker, install)
		}
		rest := install[i+len(marker):]
		return rest[:strings.IndexAny(rest, " \n")]
	}

	releaseA, releaseB := releaseOf(installA), releaseOf(installB)
	if releaseA == "" || releaseB == "" {
		t.Fatalf("empty release name(s): %q %q", releaseA, releaseB)
	}
	if releaseA == releaseB {
		t.Fatalf("two different operators got the same helm release name %q - every object the chart templates (now keyed off .Release.Name) would collide:\nA: %s\nB: %s", releaseA, installA, installB)
	}
	// Both still install into the same shared namespace - the fix is a unique release name, not a
	// unique namespace, since the chart's own object names are now derived from .Release.Name.
	if !strings.Contains(installA, "--namespace continuum-system") || !strings.Contains(installB, "--namespace continuum-system") {
		t.Fatalf("expected both installs to target --namespace continuum-system:\nA: %s\nB: %s", installA, installB)
	}
	// A valid Helm release name: lowercase alphanumeric and hyphens only.
	validRelease := func(s string) bool {
		for _, c := range s {
			if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
		return s != ""
	}
	if !validRelease(releaseA) || !validRelease(releaseB) {
		t.Fatalf("release name(s) not a valid Helm release name: %q %q", releaseA, releaseB)
	}
}
