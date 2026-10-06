package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"continuum/internal/chart"
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
	if upOp["exposure"] != "loadbalancer" {
		t.Fatalf("the exposure that was asked for is not kept: %v", upOp)
	}
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

	// While there is no address, an operator pointing at it gets its Service name in the cluster, and verifies the
	// certificate by the stable name (the Service name is not on it).
	down := create("edge", map[string]any{"kind": "operator", "targetOperatorId": upID}, nil)
	if !strings.Contains(down["install"].(string), "--set export.otlp.endpoint="+operatorServiceName(upID)+".continuum-system.svc:4317") || !strings.Contains(down["install"].(string), "--set export.otlp.tls.serverName="+upID+".continuum-system.svc") {
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

// The central operator's address is set the same way as any other's, from the UI, and from then on commands that point
// at it dial that address and verify it by the stable name: no Helm value and no restart needed.
func TestTheCentralOperatorsAddressIsSetFromTheUIAndUsedByEveryCommand(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	_, viewer := a.user(t, "vera", RoleViewer)
	f, _ := newFusion(t, a)
	a.a.Fusion = f
	if r := a.do("POST", "/api/v1/fusion/enable", nil, withCookie(cookie)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	central := func() map[string]any {
		r := a.do("GET", "/api/v1/operators/"+CentralOperatorID, nil, withCookie(cookie))
		if r.Code != 200 {
			t.Fatalf("get central: %d %s", r.Code, r.Body.String())
		}
		return r.json(t)
	}
	if d := central(); d["reachableFromOtherClusters"] != false || d["address"] != nil || d["endpoint"] != "continuum-fusion-central.continuum.svc:4317" {
		t.Fatalf("before an address: %v", d)
	}
	path := "/api/v1/operators/" + CentralOperatorID + "/address"
	if r := a.do("POST", path, map[string]any{"address": "fusion.example.com:4317"}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer set it: %d", r.Code)
	}
	if r := a.do("POST", path, map[string]any{"address": "127.0.0.1:4317"}, withCookie(cookie)); r.Code != 400 {
		t.Fatalf("a loopback address: %d", r.Code)
	}
	if r := a.do("POST", path, map[string]any{"address": "Fusion.Example.com"}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("set: %d %s", r.Code, r.Body.String())
	}
	if d := central(); d["address"] != "fusion.example.com:4317" || d["reachableFromOtherClusters"] != true || d["endpoint"] != "fusion.example.com:4317" {
		t.Fatalf("after setting it: %v", d)
	}
	if !f.Exposed() || f.CentralEndpoint() != "fusion.example.com:4317" {
		t.Fatalf("the switch does not know: %v %q", f.Exposed(), f.CentralEndpoint())
	}
	if r := a.do("GET", "/api/v1/fusion", nil, withCookie(cookie)); !strings.Contains(r.Body.String(), `"service":"continuum-fusion-central"`) || !strings.Contains(r.Body.String(), `"exposed":true`) {
		t.Fatalf("fusion status: %s", r.Body.String())
	}
	// A cluster's own telemetry pointed at it dials that address and verifies the stable name.
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "athens", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "operator", "targetOperatorId": CentralOperatorID}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	install, _ := r.json(t)["install"].(string)
	for _, w := range []string{"--set export.otlp.endpoint=fusion.example.com:4317", "--set export.otlp.tls.serverName=op-central.continuum-system.svc"} {
		if !strings.Contains(install, w) {
			t.Errorf("install lacks %q:\n%s", w, install)
		}
	}
	// Clearing it returns to the in-cluster name.
	if r := a.do("POST", path, map[string]any{"address": ""}, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("clear: %d %s", r.Code, r.Body.String())
	}
	if d := central(); d["address"] != nil || d["reachableFromOtherClusters"] != false || f.Exposed() {
		t.Fatalf("after clearing it: %v", d)
	}
}

func TestGuardCentralStillProtectsEverythingElse(t *testing.T) {
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
	// With no address it is dialled by its Service name, which is not on the certificate: the stable name still is.
	if !strings.Contains(operatorRouteFlags(store.Operator{ID: "op-abc"}, "op-abc-regional-operator.continuum-system.svc:4317", store.ModalityTraces), "--set telemetry.export.routes.traces.tls.serverName=op-abc.continuum-system.svc") {
		t.Fatal("a regional operator with no address must still be verified by its stable name")
	}
	// The central operator in its own cluster is dialled by a name its certificate carries.
	// The route states the name anyway, empty, so a server name left by an earlier destination cannot linger.
	inCluster := operatorRouteFlags(store.Operator{ID: CentralOperatorID}, "x.continuum.svc:4317", store.ModalityTraces)
	if !strings.Contains(inCluster, "--set telemetry.export.routes.traces.tls.serverName= --set") {
		t.Fatalf("the in-cluster central operator needs no server name override, and must clear any earlier one: %s", inCluster)
	}
}

// The Service the chart creates, and the name a sender in the same cluster dials, are one and the same: the chart's
// own naming rule (release name, plus "-regional-operator" unless it already says so), and the receiver certificate
// carries both that name and the stable one.
func TestOperatorServiceNameFollowsTheChartAndIsOnTheCertificate(t *testing.T) {
	for _, c := range []struct{ id, want string }{
		{"op-abc", "op-abc-regional-operator"},
		{"my-regional-operator", "my-regional-operator"},
		{"op-" + strings.Repeat("x", 70), ("op-" + strings.Repeat("x", 70) + "-regional-operator")[:63]},
	} {
		if got := operatorServiceName(c.id); got != c.want {
			t.Fatalf("operatorServiceName(%q) = %q, want %q", c.id, got, c.want)
		}
	}
	op := store.Operator{ID: "op-abc"}
	if got := operatorInClusterEndpoint(op); got != "op-abc-regional-operator.continuum-system.svc:4317" {
		t.Fatalf("in-cluster endpoint: %s", got)
	}
	if !operatorNeedsServerName(op) || operatorNeedsServerName(store.Operator{ID: CentralOperatorID}) || !operatorNeedsServerName(store.Operator{ID: CentralOperatorID, Address: "a:1"}) {
		t.Fatal("which operators need a server name override")
	}
}

// What the server tells a sender to dial is the Service the chart really creates for that release: rendered here
// for the exact release name the install command uses (the operator's id), for plain and "regional-operator" names.
func TestTheDialledServiceNameIsTheOneTheChartRenders(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkg, err := chart.RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), chart.RegionalOperator.Filename())
	if err := os.WriteFile(tgz, pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"op-52c91de1fa99", "eu-regional-operator"} {
		out, err := exec.Command(h, "template", id, tgz, "--set", "export.otlp.endpoint=c:4317").CombinedOutput()
		if err != nil {
			t.Fatalf("helm template %s: %v\n%s", id, err, out)
		}
		var svc string
		for _, doc := range strings.Split(string(out), "\n---") {
			if !strings.Contains(doc, "\nkind: Service\n") {
				continue
			}
			for _, line := range strings.Split(doc, "\n") {
				if strings.HasPrefix(line, "  name: ") {
					svc = strings.TrimSpace(strings.TrimPrefix(line, "  name: "))
					break
				}
			}
		}
		if svc == "" || svc != operatorServiceName(id) {
			t.Fatalf("release %s renders Service %q, the server dials %q", id, svc, operatorServiceName(id))
		}
		if want := svc + ".continuum-system.svc:4317"; operatorInClusterEndpoint(store.Operator{ID: id}) != want {
			t.Fatalf("endpoint %s, want %s", operatorInClusterEndpoint(store.Operator{ID: id}), want)
		}
	}
}
