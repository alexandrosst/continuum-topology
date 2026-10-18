package server

import (
	"strings"
	"testing"

	continuumv1 "continuum/gen/continuumv1"

	"continuum/internal/accounts"
	"continuum/internal/operators"
)

func failing(err string) *SenderDeliveryDoc {
	return &SenderDeliveryDoc{State: "failing", LastError: err}
}

func TestDiagnosisSaysWhichEndIsAtFault(t *testing.T) {
	base := diagnosisInput{ClusterName: "patras-edge", Connected: true, Address: "10.20.0.14:31317", AddrSource: "recorded", OperatorID: "op-1", OperatorName: "site-athens", OperatorState: operators.HealthUnknown}
	peerOK := []peerOutcome{{Name: "athens-edge", Address: "10.20.0.14:31317", State: "exporting"}}
	peerElsewhere := []peerOutcome{{Name: "athens-edge", Address: "203.0.113.9:4317", State: "exporting"}}

	cases := []struct {
		name  string
		mod   func(*diagnosisInput)
		where string
		has   []string
	}{
		{"nothing wrong when exporting", func(in *diagnosisInput) { in.Delivery = &SenderDeliveryDoc{State: "exporting"} }, "", nil},
		{"nothing reported is not a fault", func(in *diagnosisInput) { in.Delivery = nil }, "", nil},
		{"agent offline says nothing about the operator", func(in *diagnosisInput) { in.Connected = false; in.Delivery = failing("x") }, "sender", []string{"not connected"}},
		{"timeout while a peer exports at the same address: the sender's path", func(in *diagnosisInput) {
			in.Delivery = failing("rpc error: code = Unavailable desc = connection error: dial tcp 10.20.0.14:31317: i/o timeout")
			in.Peers = peerOK
		}, "sender-network", []string{"athens-edge is exporting", "operator is up", "private address", "Test"}},
		{"timeout, a peer works at another address: say which", func(in *diagnosisInput) {
			in.Delivery = failing("context deadline exceeded")
			in.Peers = peerElsewhere
		}, "sender-network", []string{"203.0.113.9:4317", "try that one"}},
		{"timeout, operator offline and nobody else getting through: the operator", func(in *diagnosisInput) {
			in.Delivery = failing("i/o timeout")
			in.OperatorState = operators.HealthOffline
			in.OperatorSeen = "2026-10-12T10:00:00Z"
		}, "operator", []string{"stopped reporting", "2026-10-12T10:00:00Z", "get pods"}},
		{"timeout, operator offline but a peer exports: the path again", func(in *diagnosisInput) {
			in.Delivery = failing("i/o timeout")
			in.OperatorState = operators.HealthOffline
			in.Peers = peerOK
		}, "sender-network", nil},
		{"timeout with nothing to compare", func(in *diagnosisInput) { in.Delivery = failing("i/o timeout") }, "unclear", []string{"nothing to compare", "Test"}},
		{"refused: the port", func(in *diagnosisInput) {
			in.Delivery = failing("dial tcp 10.20.0.14:31317: connect: connection refused")
		}, "address", []string{"nothing listens", "node port"}},
		{"dns", func(in *diagnosisInput) {
			in.Address = "otlp.example.com:4317"
			in.Delivery = failing("lookup otlp.example.com: no such host")
		}, "address", []string{"cannot look it up"}},
		{"certificate while others work: this cluster's own", func(in *diagnosisInput) {
			in.Delivery = failing("tls: failed to verify certificate: x509: certificate has expired")
			in.Peers = peerOK
		}, "certificate", []string{"client certificate", "run its generated command again"}},
		{"certificate, nothing to compare", func(in *diagnosisInput) { in.Delivery = failing("x509: certificate signed by unknown authority") }, "certificate", []string{"op-1.continuum-system.svc", "Renew"}},
		{"credential", func(in *diagnosisInput) { in.Delivery = failing("rpc error: code = Unauthenticated desc = bad token") }, "credential", []string{"credentials"}},
		{"a chosen private address gets no private-address warning", func(in *diagnosisInput) {
			in.AddrSource = "chosen"
			in.Delivery = failing("i/o timeout")
			in.Peers = peerOK
		}, "sender-network", nil},
	}
	for _, c := range cases {
		in := base
		c.mod(&in)
		got := diagnoseSender(in)
		if c.where == "" {
			if got != nil {
				t.Errorf("%s: explained something that is not wrong: %+v", c.name, got)
			}
			continue
		}
		if got == nil || got.Where != c.where {
			t.Errorf("%s: where = %+v, want %s", c.name, got, c.where)
			continue
		}
		for _, w := range c.has {
			if !strings.Contains(got.Title+" "+got.Detail, w) {
				t.Errorf("%s: %q missing from %q", c.name, w, got.Title+" "+got.Detail)
			}
		}
	}
	// The chosen-address case must not carry the private-address sentence.
	in := base
	in.AddrSource, in.Delivery, in.Peers = "chosen", failing("i/o timeout"), peerOK
	if d := diagnoseSender(in); strings.Contains(d.Detail, "private address") {
		t.Errorf("an address a person chose on purpose is called out as private: %s", d.Detail)
	}
}

func exportHealth(state, signal, lastErr string, sent, failed uint64) *continuumv1.Diagnostics {
	st := map[string]continuumv1.ExportRouteHealth_State{"exporting": continuumv1.ExportRouteHealth_EXPORTING, "failing": continuumv1.ExportRouteHealth_FAILING}[state]
	return &continuumv1.Diagnostics{ExportHealth: &continuumv1.ExportHealth{PodsReached: 1, LastError: lastErr,
		Routes: []*continuumv1.ExportRouteHealth{{Exporter: "otlp", Signal: signal, State: st, Sent: sent, Failed: failed}}}}
}

func TestSendersOfAnOperatorShowTheirAddressesAndHowDeliveryGoes(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	_, viewer := a.user(t, "vera", accounts.RoleViewer)
	opID, intentID := intentToOperator(t, a, cookie)
	// The intent from the helper is for a second agent; give it a stream-less view with a failing exporter.
	r := a.do("GET", "/api/v1/telemetry-intents/"+intentID, nil, withCookie(cookie))
	agentID := r.json(t)["agentId"].(string)
	body := map[string]any{
		"namespaces": []string{}, "exclude": []string{},
		"signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}, {"id": "systemLogs", "source": "builtin"}},
	}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/scope", body, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("scope: %d %s", r.Code, r.Body.String())
	}
	dest := map[string]any{"destination": map[string]any{"kind": "operator", "targetOperatorId": opID, "address": "10.20.0.14:31317"},
		"routes": map[string]any{"logs": map[string]any{"kind": "operator", "targetOperatorId": opID}}}
	if r := a.do("POST", "/api/v1/telemetry-intents/"+intentID+"/destination", dest, withCookie(cookie)); r.Code != 200 {
		t.Fatalf("destination: %d %s", r.Code, r.Body.String())
	}
	v := liveView(*a.now, "n1")
	v.connected = true
	v.Ext.Diag = exportHealth("failing", "metrics", "dial tcp 10.20.0.14:31317: i/o timeout", 0, 12)
	a.hub().mu.Lock()
	a.hub().views[agentID] = v
	a.hub().mu.Unlock()

	path := "/api/v1/operators/" + opID + "/senders"
	if r := a.do("GET", path, nil, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer reads node addresses: %d", r.Code)
	}
	r = a.do("GET", path, nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	got := r.json(t)
	list := got["senders"].([]any)
	if len(list) != 1 {
		t.Fatalf("senders: %v", got)
	}
	s := list[0].(map[string]any)
	addrs := s["addresses"].([]any)
	if len(addrs) != 2 {
		t.Fatalf("metrics dial the chosen address and logs the recorded one: %v", addrs)
	}
	bySource := map[string]string{}
	for _, x := range addrs {
		m := x.(map[string]any)
		bySource[m["source"].(string)] = m["address"].(string)
	}
	if bySource["chosen"] != "10.20.0.14:31317" || bySource["recorded"] != "recorded.example.com:4317" {
		t.Fatalf("addresses by source: %v", bySource)
	}
	d := s["delivery"].(map[string]any)
	if d["state"] != "failing" || !strings.Contains(d["lastError"].(string), "i/o timeout") {
		t.Fatalf("delivery: %v", d)
	}
	// Only the metrics exporter is failing, and only metrics and logs go here: logs have no exporter reported yet.
	diag, _ := s["diagnosis"].(map[string]any)
	if diag == nil || diag["where"] == "" {
		t.Fatalf("a failing sender is explained: %v", s)
	}
	// An operator that nothing sends to lists nobody.
	r = a.do("POST", "/api/v1/operators", map[string]any{"name": "other", "sourceClusterIds": []string{}, "destination": map[string]any{"kind": "external", "endpoint": "c.example:4317"}}, withCookie(cookie))
	if r.Code == 201 {
		other := r.json(t)["operator"].(map[string]any)["id"].(string)
		if l := a.do("GET", "/api/v1/operators/"+other+"/senders", nil, withCookie(cookie)).json(t)["senders"].([]any); len(l) != 0 {
			t.Fatalf("senders of an operator nothing sends to: %v", l)
		}
	}
}
