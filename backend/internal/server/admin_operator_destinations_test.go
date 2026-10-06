package server

import (
	"strings"
	"testing"
)

func destByID(list []map[string]any, id string) map[string]any {
	for _, d := range list {
		if d["id"] == id {
			return d
		}
	}
	return nil
}

// What the destination picker reads: editors may read it, nothing secret is in it, revoked operators are not offered.
func TestOperatorDestinationsAreForEditorsAndHoldNoSecrets(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	_, viewer := a.user(t, "vi", RoleViewer)
	cl := a.approvedCluster(t, fp)
	body := extBody("athens", cl)
	body["exposure"] = "loadbalancer"
	body["acceptedModalities"] = []string{"metrics"}
	id := a.createOperatorDoc(t, admin, body)["operator"].(map[string]any)["id"].(string)
	gone := a.createOperatorDoc(t, admin, extBody("gone", cl))["operator"].(map[string]any)["id"].(string)
	a.do("POST", "/api/v1/operators/"+gone+"/revoke", map[string]any{"reason": "x"}, withCookie(admin))

	if r := a.do("GET", "/api/v1/operator-destinations", nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("viewer: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/operators", nil, withCookie(editor)); r.Code != 403 {
		t.Fatalf("the full list stays an admin's: %d", r.Code)
	}
	r := a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor))
	if r.Code != 200 {
		t.Fatalf("editor: %d %s", r.Code, r.Body.String())
	}
	list := r.jsonArray(t)
	if len(list) != 1 || destByID(list, gone) != nil {
		t.Fatalf("destinations = %v", list)
	}
	d := list[0]
	if d["id"] != id || d["kind"] != "regional" || d["status"] != "active" || d["addressState"] != "pending" || d["reachableFromOtherClusters"] != false || d["recommended"] != false || d["usedBy"] != float64(0) {
		t.Fatalf("destination = %v", d)
	}
	if m, _ := d["acceptedModalities"].([]any); len(m) != 1 || m[0] != "metrics" {
		t.Fatalf("acceptedModalities = %v", d["acceptedModalities"])
	}
	if d["endpoint"] != operatorServiceName(id)+".continuum-system.svc:4317" {
		t.Fatalf("endpoint = %v", d["endpoint"])
	}
	if h, _ := d["health"].(map[string]any); h["state"] != "unknown" {
		t.Fatalf("health = %v", h)
	}
	for _, k := range []string{"token", "secret", "address", "receiverAuth", "clientCaScope", "destination", "createdBy"} {
		if _, has := d[k]; has {
			t.Fatalf("the read model carries %q", k)
		}
	}
	// A heartbeat that is on but has not arrived is "waiting"; an address, once recorded, is "set" and reachable.
	hb := extBody("beating", cl)
	hb["heartbeat"] = true
	hbID := a.createOperatorDoc(t, admin, hb)["operator"].(map[string]any)["id"].(string)
	a.do("POST", "/api/v1/operators/"+id+"/address", map[string]any{"address": "otlp.example.com:4317"}, withCookie(admin))
	list = a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t)
	if h := destByID(list, hbID)["health"].(map[string]any); h["state"] != "waiting" {
		t.Fatalf("waiting health = %v", h)
	}
	if d := destByID(list, id); d["addressState"] != "set" || d["reachableFromOtherClusters"] != true || d["endpoint"] != "otlp.example.com:4317" {
		t.Fatalf("after the address = %v", d)
	}
}

// The central operator is on offer from the first call, before FUSION was ever enabled, with FUSION's own state as its
// health; nothing else is ever "recommended".
func TestOperatorDestinationsOfferCentralFromTheStart(t *testing.T) {
	a := newAdminRig(t)
	_, editor := a.user(t, "ed", RoleEditor)
	_, admin := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	regional := a.createOperatorDoc(t, admin, extBody("athens", cl))["operator"].(map[string]any)["id"].(string)

	// A server with no FUSION has no central operator to offer.
	list := a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t)
	if destByID(list, CentralOperatorID) != nil {
		t.Fatalf("central offered without FUSION: %v", list)
	}

	f, k := newFusion(t, a)
	a.a.Fusion = f
	list = a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t)
	if len(list) != 2 || list[0]["id"] != CentralOperatorID || list[1]["id"] != regional {
		t.Fatalf("central is not first: %v", list)
	}
	c := list[0]
	if c["kind"] != "central" || c["status"] != "active" || c["recommended"] != false || c["addressState"] != "none" || c["reachableFromOtherClusters"] != false {
		t.Fatalf("synthetic central = %v", c)
	}
	if _, has := c["endpoint"]; has {
		t.Fatalf("an endpoint before it is known: %v", c["endpoint"])
	}
	if h := c["health"].(map[string]any); h["state"] != "off" {
		t.Fatalf("health = %v", h)
	}

	// Starting: recommended, and the same entry once the central operator really exists.
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	list = a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t)
	c = destByID(list, CentralOperatorID)
	if len(list) != 2 || c["kind"] != "central" || c["recommended"] != true || c["health"].(map[string]any)["state"] != "starting" || c["endpoint"] == nil {
		t.Fatalf("central while starting = %v", list)
	}
	k.allReady()
	c = destByID(a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t), CentralOperatorID)
	if c["recommended"] != true || c["health"].(map[string]any)["state"] != "online" {
		t.Fatalf("central while running = %v", c)
	}
	// Turned off again: still offered, no longer recommended.
	if _, err := f.Disable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	c = destByID(a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t), CentralOperatorID)
	if c["recommended"] != false || c["health"].(map[string]any)["state"] != "off" {
		t.Fatalf("central after turning it off = %v", c)
	}
	if r := destByID(a.do("GET", "/api/v1/operator-destinations", nil, withCookie(editor)).jsonArray(t), regional); r["recommended"] != false {
		t.Fatalf("a regional operator was recommended: %v", r)
	}
}

// The central operator's document carries FUSION's state as its health, so the operators page and the picker agree.
func TestCentralOperatorHealthIsFusionsState(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	f, k := newFusion(t, a)
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	health := func() map[string]any {
		return a.do("GET", "/api/v1/operators/"+CentralOperatorID, nil, withCookie(admin)).json(t)["health"].(map[string]any)
	}
	if h := health(); h["state"] != "starting" || h["reporting"] != false {
		t.Fatalf("starting: %v", h)
	}
	k.allReady()
	if h := health(); h["state"] != "online" {
		t.Fatalf("running: %v", h)
	}
	doc := a.do("GET", "/api/v1/operators/"+CentralOperatorID, nil, withCookie(admin)).json(t)
	if doc["addressState"] != "none" || strings.Contains(a.do("GET", "/api/v1/operators", nil, withCookie(admin)).Body.String(), `"state":"waiting"`) {
		t.Fatalf("central doc = %v", doc)
	}
}

func TestCentralHealthMapsFusionsStates(t *testing.T) {
	for _, c := range []struct {
		st   FusionStatus
		want string
	}{
		{FusionStatus{Available: true, State: "running"}, HealthOnline},
		{FusionStatus{Available: true, State: "starting"}, HealthStarting},
		{FusionStatus{Available: true, State: "attention"}, HealthAttention},
		{FusionStatus{Available: true, State: "off"}, HealthOff},
		{FusionStatus{State: "off", Reason: "not-configured"}, HealthUnknown},
	} {
		if got := centralHealth(c.st).State; got != c.want {
			t.Errorf("%+v: %s, want %s", c.st, got, c.want)
		}
	}
}

// Waiting is for an operator that can still report: a revoked one that never did is simply unknown.
func TestHealthIsWaitingOnlyForAnActiveOperatorWithAHeartbeatCredential(t *testing.T) {
	e := newEnv(t)
	op, _ := e.hbOperator(t, "athens", fp)
	if h := operatorHealthAt(op, e.core.Now()); h.State != HealthWaiting || h.HeartbeatEnabledAt == nil {
		t.Fatalf("active, enabled, unseen: %+v", h)
	}
	op.Status = "revoked"
	if h := operatorHealthAt(op, e.core.Now()); h.State != HealthUnknown {
		t.Fatalf("revoked, never seen: %+v", h)
	}
	op.Status, op.HeartbeatHash, op.HeartbeatEnabledAt = "active", nil, nil
	if h := operatorHealthAt(op, e.core.Now()); h.State != HealthUnknown || h.HeartbeatEnabledAt != nil {
		t.Fatalf("no credential: %+v", h)
	}
}
