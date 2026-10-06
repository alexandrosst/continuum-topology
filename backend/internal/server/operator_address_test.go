package server

import (
	"strings"
	"testing"

	"continuum/internal/store"
)

func TestValidOperatorAddress(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"  ":                           "",
		"otlp.example.com:4317":        "otlp.example.com:4317",
		"  OTLP.Example.COM.:4317":     "otlp.example.com:4317",
		"203.0.113.7:4317":             "203.0.113.7:4317",
		"[2001:db8::1]:4317":           "[2001:db8::1]:4317",
		"otlp.example.com":             "otlp.example.com:4317", // a bare host gets the receiver's own port
		"203.0.113.7":                  "203.0.113.7:4317",
		"Otlp.Example.com:+4317":       "otlp.example.com:4317", // the port is stored as the number it is
		"otlp.example.com:04317":       "otlp.example.com:4317",
		"op-1.svc.clusterset.local:80": "op-1.svc.clusterset.local:80",
	} {
		got, err := validOperatorAddress(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{
		"https://otlp.example.com:4317", // a scheme
		"otlp.example.com:4317/x",       // a path
		"u:p@otlp.example.com:4317",     // credentials
		"otlp.example.com:0",
		"otlp.example.com:70000",
		"otlp.example.com:abc",
		"2001:db8::1", // an IPv6 address needs its brackets
		"otlp.example.com:",
		"127.0.0.1:4317",
		"[::1]:4317",
		"0.0.0.0:4317",
		"169.254.1.1:4317",
		"localhost:4317",
		"a b.example.com:4317",
		"-bad.example.com:4317",
		"bad_name.example.com:4317",
		strings.Repeat("a", 300) + ":4317",
	} {
		if got, err := validOperatorAddress(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

func TestAnOperatorAddressIsSetShownAuditedAndUsedByEveryCommandThatPointsAtIt(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	_, viewer := a.user(t, "vera", RoleViewer)
	cl := a.approvedCluster(t, fp)

	create := func(name string, dest map[string]any, extra map[string]any) map[string]any {
		body := map[string]any{"name": name, "sourceClusterIds": []string{cl}, "destination": dest}
		for k, v := range extra {
			body[k] = v
		}
		r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
		if r.Code != 201 {
			t.Fatalf("create %s: %d %s", name, r.Code, r.Body.String())
		}
		return r.json(t)
	}
	// An operator nobody has told the server an address for is only reachable in its own cluster.
	up := create("eu-hub", map[string]any{"kind": "external", "endpoint": "collector.example:4317"}, map[string]any{"exposure": "loadbalancer"})
	upOp := up["operator"].(map[string]any)
	upID := upOp["id"].(string)
	if upOp["reachableFromOtherClusters"] != false || upOp["address"] != nil {
		t.Fatalf("a new operator already has an address: %v", upOp)
	}
	if !strings.Contains(up["install"].(string), "--set service.type=LoadBalancer") {
		t.Fatalf("exposure loadbalancer did not set the Service type: %v", up["install"])
	}
	for _, e := range []string{"", "cluster"} {
		got := create("c-"+e, map[string]any{"kind": "external", "endpoint": "c:4317"}, map[string]any{"exposure": e})
		if strings.Contains(got["install"].(string), "service.type") {
			t.Fatalf("exposure %q set a Service type", e)
		}
	}
	if got := create("np", map[string]any{"kind": "external", "endpoint": "c:4317"}, map[string]any{"exposure": "nodeport"}); !strings.Contains(got["install"].(string), "--set service.type=NodePort") {
		t.Fatal("nodeport was not honoured")
	}
	if r := a.do("POST", "/api/v1/operators", map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}, "exposure": "internet"}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("a made-up exposure: %d", r.Code)
	}

	// While there is no address, an operator pointing at it gets the in-cluster name and no server-name override.
	down := create("edge", map[string]any{"kind": "operator", "targetOperatorId": upID}, nil)
	if !strings.Contains(down["install"].(string), "--set export.otlp.endpoint="+upID+".continuum-system.svc:4317") || strings.Contains(down["install"].(string), "serverName") {
		t.Fatalf("before an address is set: %v", down["install"])
	}

	// Only an administrator sets it, and only a valid one.
	path := "/api/v1/operators/" + upID + "/address"
	if r := a.do("POST", path, map[string]any{"address": "otlp.eu.example.com:4317"}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer set an address: %d", r.Code)
	}
	for _, bad := range []string{"otlp.eu.example.com:99999", "https://x:1", "127.0.0.1:4317"} {
		if r := a.do("POST", path, map[string]any{"address": bad}, withCookie(cookie)); r.Code != 400 {
			t.Fatalf("%q: %d %s", bad, r.Code, r.Body.String())
		}
	}
	if r := a.do("POST", "/api/v1/operators/op-nope/address", map[string]any{"address": "a.example.com:1"}, withCookie(cookie)); r.Code != 404 {
		t.Fatalf("an unknown operator: %d", r.Code)
	}
	r := a.do("POST", path, map[string]any{"address": " OTLP.EU.example.com:4317 "}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("set address: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	if doc["address"] != "otlp.eu.example.com:4317" || doc["reachableFromOtherClusters"] != true {
		t.Fatalf("operator after the address: %v", doc)
	}
	if got := a.do("GET", "/api/v1/operators/"+upID, nil, withCookie(cookie)).json(t); got["address"] != "otlp.eu.example.com:4317" {
		t.Fatalf("get: %v", got)
	}

	// Now an operator created to export to it dials the address and verifies its stable name.
	down2 := create("edge2", map[string]any{"kind": "operator", "targetOperatorId": upID}, nil)
	inst := down2["install"].(string)
	if !strings.Contains(inst, "--set export.otlp.endpoint=otlp.eu.example.com:4317") || !strings.Contains(inst, "--set export.otlp.tls.serverName="+upID+".continuum-system.svc") {
		t.Fatalf("an operator pointing at an addressed operator: %s", inst)
	}
	if et, _ := down2["exportTarget"].(map[string]any); et["reachableFromOtherClusters"] != true || et["endpoint"] != "otlp.eu.example.com:4317" {
		t.Fatalf("exportTarget = %v", et)
	}

	// The audit trail says what changed, and clearing it goes back to the in-cluster name.
	var changes []string
	rows, _ := a.st.AuditSince(a.ctx, 0, 500)
	for _, e := range rows {
		if e.Action == "operator-address-changed" {
			changes = append(changes, e.Detail)
		}
	}
	if len(changes) != 1 || changes[0] != "otlp.eu.example.com:4317" {
		t.Fatalf("audit = %v", changes)
	}
	if r := a.do("POST", path, map[string]any{"address": ""}, withCookie(cookie)); r.Code != 200 || r.json(t)["reachableFromOtherClusters"] != false {
		t.Fatalf("clear: %d %s", r.Code, r.Body.String())
	}
}

func TestTheCentralOperatorsAddressIsFusionsNotSetByHand(t *testing.T) {
	if err := guardCentral(CentralOperatorID); err == nil {
		t.Fatal("the central operator can be edited by hand")
	}
}

// A cluster's own telemetry intent, pointed at an operator elsewhere, gets the same address and server name.
func TestATelemetryIntentCommandDialsTheOperatorsAddress(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	opCluster := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "eu-hub", "sourceClusterIds": []string{opCluster},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create operator: %d %s", r.Code, r.Body.String())
	}
	opID, _ := r.json(t)["operator"].(map[string]any)["id"].(string)
	if r := a.do("POST", "/api/v1/operators/"+opID+"/address", map[string]any{"address": "203.0.113.7:4317"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("set address: %d %s", r.Code, r.Body.String())
	}
	agentID := a.approvedAgentID(t, fp2)
	r = a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agentID, "name": "patras-edge",
		"destination": map[string]any{"kind": "operator", "targetOperatorId": opID}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	id, _ := r.json(t)["id"].(string)
	r = a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("command: %d %s", r.Code, r.Body.String())
	}
	frag, _ := r.json(t)["installFragment"].(string)
	if !strings.Contains(frag, "--set telemetry.export.otlp.endpoint=203.0.113.7:4317") || !strings.Contains(frag, "--set telemetry.export.otlp.tls.serverName="+opID+".continuum-system.svc") {
		t.Fatalf("installFragment: %q", frag)
	}
}

// The per-signal route flags carry the server name too, since each route is its own exporter.
func TestAnOperatorRouteVerifiesTheStableNameWhenItHasAnAddress(t *testing.T) {
	op := store.Operator{ID: "op-abc", Address: "otlp.example.com:4317"}
	got := operatorRouteFlags(op, op.Address, store.ModalityTraces)
	if !strings.Contains(got, "--set telemetry.export.routes.traces.tls.serverName=op-abc.continuum-system.svc") {
		t.Fatalf("%s", got)
	}
	if strings.Contains(operatorRouteFlags(store.Operator{ID: "op-abc"}, "op-abc.continuum-system.svc:4317", store.ModalityTraces), "serverName") {
		t.Fatal("an operator with no address gets a server name override")
	}
}
