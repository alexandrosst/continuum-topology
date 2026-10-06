package server

import (
	"strings"
	"testing"
)

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

// TestTelemetryIntentCommandExternalDestination covers an external-destination intent's /command
// response: only the provenance flags (added to the chart in c8a2a99, alongside this intent model) -
// everything else about an external endpoint (its own address, auth, TLS) is already built client-side
// by the frontend's own withTelemetry(), not duplicated here.
func TestTelemetryIntentCommandExternalDestination(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	agentID := a.approvedAgentID(t, fp)
	ag, err := a.st.GetAgent(a.ctx, agentID)
	if err != nil {
		t.Fatal(err)
	}

	body := map[string]any{
		"agentId":     agentID,
		"name":        "patras-edge",
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}
	r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)

	r = a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("command: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	frag, _ := doc["installFragment"].(string)
	for _, want := range []string{
		"--set telemetry.resource.orgId=org-1",
		"--set telemetry.resource.clusterId=" + ag.ClusterID,
		"--set telemetry.resource.intentId=" + id,
	} {
		if !strings.Contains(frag, want) {
			t.Fatalf("installFragment missing %q: %q", want, frag)
		}
	}
	if strings.Contains(frag, "export.otlp") {
		t.Fatalf("an external destination must not get operator export flags: %q", frag)
	}
	secretCommands, _ := doc["secretCommands"].([]any)
	if len(secretCommands) != 0 {
		t.Fatalf("expected no secret commands for an external destination, got %v", secretCommands)
	}
}

// TestTelemetryIntentCommandOperatorDestinationReissuesEachTime covers an operator-destination intent's
// /command response: the resolved export flags plus exactly one secret-creation command, and a second
// call minting a genuinely fresh certificate rather than replaying the first - "on demand" the same way
// TestIssueOperatorClientCertReissuesOnDemand proves it at the Core level.
func TestTelemetryIntentCommandOperatorDestinationReissuesEachTime(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	opCluster := a.approvedCluster(t, fp)
	opBody := map[string]any{
		"name":             "athens-regional",
		"sourceClusterIds": []string{opCluster},
		"destination":      map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}
	r := a.do("POST", "/api/v1/operators", opBody, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create operator: %d %s", r.Code, r.Body.String())
	}
	opID, _ := r.json(t)["operator"].(map[string]any)["id"].(string)

	agentID := a.approvedAgentID(t, fp2)
	body := map[string]any{
		"agentId":     agentID,
		"name":        "patras-edge",
		"destination": map[string]any{"kind": "operator", "targetOperatorId": opID},
	}
	r = a.do("POST", "/api/v1/telemetry-intents", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)

	command := func() (frag string, secretCmd string) {
		r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie))
		if r.Code != 200 {
			t.Fatalf("command: %d %s", r.Code, r.Body.String())
		}
		doc := r.json(t)
		frag, _ = doc["installFragment"].(string)
		cmds, _ := doc["secretCommands"].([]any)
		if len(cmds) != 1 {
			t.Fatalf("expected exactly one secret command, got %v", cmds)
		}
		secretCmd, _ = cmds[0].(string)
		return frag, secretCmd
	}

	frag1, secret1 := command()
	if !strings.Contains(frag1, "--set telemetry.export.otlp.endpoint="+operatorServiceName(opID)+".continuum-system.svc:4317") {
		t.Fatalf("installFragment missing the resolved operator endpoint: %q", frag1)
	}
	if !strings.Contains(frag1, "telemetry.export.otlp.tls.mtls.enabled=true") {
		t.Fatalf("installFragment missing mTLS flags: %q", frag1)
	}
	if !strings.Contains(secret1, "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("secret command does not carry a certificate: %q", secret1)
	}

	frag2, secret2 := command()
	if frag2 != frag1 {
		t.Fatalf("installFragment should be stable across calls (same operator, same agent): %q vs %q", frag1, frag2)
	}
	if secret2 == secret1 {
		t.Fatal("expected a freshly minted certificate on each call, got identical secret commands")
	}
	// The Secret goes where the caller's upgrade will run, and the response says where that is, so the
	// caller never has to guess (this agent never reported its own: the chart's documented defaults).
	if !strings.Contains(secret1, "namespace: continuum-system\n") || !strings.HasPrefix(secret1, "kubectl apply -f - <<'CONTINUUM_SECRET'") || strings.Contains(secret1, "--from-literal") {
		t.Fatalf("secret command is not a create-or-update in the release namespace: %q", secret1)
	}
	doc := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie)).json(t)
	if doc["namespace"] != "continuum-system" || doc["release"] != "continuum-agent" {
		t.Fatalf("namespace/release = %v/%v", doc["namespace"], doc["release"])
	}
}

func TestTelemetryIntentCommandRequiresAdminRole(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "jamie", RoleEditor)
	agentID := a.approvedAgentID(t, fp)
	body := map[string]any{"agentId": agentID, "name": "x", "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
	r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(editor))
	if r.Code != 201 {
		t.Fatalf("create (editor should still be allowed): %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(editor)); r.Code != 403 {
		t.Fatalf("editor calling /command: %d %s", r.Code, r.Body.String())
	}
}

// Two signal types exporting to the same operator are issued one certificate in one Secret, and each route
// names that Secret; a third going to an external endpoint needs nothing from the server.
func TestTelemetryIntentCommandRoutesShareOneCertificatePerOperator(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	opCluster := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{
		"name": "athens-regional", "sourceClusterIds": []string{opCluster},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create operator: %d %s", r.Code, r.Body.String())
	}
	opID, _ := r.json(t)["operator"].(map[string]any)["id"].(string)

	agentID := a.approvedAgentID(t, fp2)
	opDest := map[string]any{"kind": "operator", "targetOperatorId": opID}
	r = a.do("POST", "/api/v1/telemetry-intents", map[string]any{
		"agentId": agentID, "name": "patras-edge", "destination": opDest,
		"signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}, {"id": "systemLogs", "source": "builtin"}, {"id": "traces", "source": "existing"}},
	}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)

	r = a.do("POST", "/api/v1/telemetry-intents/"+id+"/destination", map[string]any{
		"destination": opDest,
		"routes": map[string]any{
			"metrics": opDest,
			"logs":    opDest,
			"traces":  map[string]any{"kind": "external", "endpoint": "zipkin.tracing.svc:9411/api/v2/spans", "insecure": true},
		},
	}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("set routes: %d %s", r.Code, r.Body.String())
	}
	routes, _ := r.json(t)["routes"].(map[string]any)
	if len(routes) != 3 {
		t.Fatalf("routes in the response = %v", routes)
	}

	r = a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("command: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	frag, _ := doc["installFragment"].(string)
	cmds, _ := doc["secretCommands"].([]any)
	if len(cmds) != 1 {
		t.Fatalf("one operator, so one Secret command; got %d: %v", len(cmds), cmds)
	}
	if !strings.Contains(cmds[0].(string), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("the Secret command has no certificate: %q", cmds[0])
	}
	secret := opID + "-export-mtls"
	for _, m := range []string{"metrics", "logs"} {
		for _, want := range []string{
			"--set telemetry.export.routes." + m + ".endpoint=" + operatorServiceName(opID) + ".continuum-system.svc:4317",
			"--set telemetry.export.routes." + m + ".tls.mtls.enabled=true",
			"--set telemetry.export.routes." + m + ".tls.mtls.secretName=" + secret,
		} {
			if !strings.Contains(frag, want) {
				t.Fatalf("fragment is missing %q: %q", want, frag)
			}
		}
	}
	// The external route is the caller's to build, and the default's own flags are not stated: every signal has a route.
	if strings.Contains(frag, "routes.traces") || strings.Contains(frag, "telemetry.export.otlp.") {
		t.Fatalf("fragment states more than the operator routes: %q", frag)
	}
	ops, _ := doc["operators"].(map[string]any)
	if ops[opID] == nil {
		t.Fatalf("response does not say how %s authenticates: %v", opID, doc["operators"])
	}

	// Each call reissues, once per operator.
	r = a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie))
	if again, _ := r.json(t)["secretCommands"].([]any); len(again) != 1 || again[0] == cmds[0] {
		t.Fatalf("expected a freshly minted certificate on the second call")
	}

	// An empty routes puts it back to one destination.
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/destination", map[string]any{"destination": opDest, "routes": map[string]any{}}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("clear routes: %d %s", r.Code, r.Body.String())
	}
	doc = a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie)).json(t)
	if f, _ := doc["installFragment"].(string); !strings.Contains(f, "telemetry.export.otlp.endpoint="+opID) || strings.Contains(f, "export.routes") {
		t.Fatalf("after clearing the routes the fragment is %q", f)
	}
}

// A revoked intent grants nothing, so it hands out no command - and so issues no client certificate.
func TestTelemetryIntentCommandRefusesARevokedIntent(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	agentID := a.approvedAgentID(t, fp2)
	r := a.do("POST", "/api/v1/telemetry-intents", map[string]any{
		"agentId": agentID, "name": "patras-edge",
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("command while active: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/revoke", map[string]any{"reason": "done"}, withCookie(cookie)); r.Code != 204 {
		t.Fatalf("revoke: %d %s", r.Code, r.Body.String())
	}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie)); r.Code != 409 {
		t.Fatalf("command for a revoked intent: %d %s", r.Code, r.Body.String())
	}
}
