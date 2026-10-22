package operatorsapi

import (
	"strings"
	"testing"

	"continuum/internal/connectivity"
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

func TestDiagnosisUsesAnAddressTestFromTheSendersOwnCluster(t *testing.T) {
	base := diagnosisInput{ClusterName: "patras-edge", Connected: true, Address: "10.20.0.14:31317", AddrSource: "chosen", OperatorID: "op-1", OperatorState: operators.HealthOnline}
	down := &connectivity.AddressReachDoc{State: "unreachable", At: "2026-10-12T10:00:00Z"}
	up := &connectivity.AddressReachDoc{State: "reachable", At: "2026-10-12T10:00:00Z"}

	in := base
	in.Reach, in.Delivery = down, &SenderDeliveryDoc{State: "waiting"}
	if d := diagnoseSender(in); d == nil || d.Where != "sender-network" || !strings.Contains(d.Detail, "2026-10-12T10:00:00Z") {
		t.Fatalf("a failed test with nothing sent yet: %+v", d)
	}
	in.Delivery = nil
	if d := diagnoseSender(in); d == nil || d.Where != "sender-network" {
		t.Fatalf("a failed test with nothing reported: %+v", d)
	}
	// Data is getting through: one failed test is not allowed to contradict that.
	in.Delivery = &SenderDeliveryDoc{State: "exporting"}
	if d := diagnoseSender(in); d != nil {
		t.Fatalf("the sender is exporting: %+v", d)
	}
	// A failure that a test from the same cluster could connect to is said to be possibly passing.
	in.Reach, in.Delivery = up, failing("i/o timeout")
	if d := diagnoseSender(in); d == nil || !strings.Contains(d.Detail, "did connect") {
		t.Fatalf("a timeout with a good test: %+v", d)
	}
}

func TestDiagnosisOfASenderToTheCentralOperatorReadsFusionsOwnState(t *testing.T) {
	base := diagnosisInput{ClusterName: "patras-edge", Connected: true, Address: "203.0.113.50:4317", AddrSource: "recorded",
		OperatorID: operators.CentralOperatorID, OperatorName: "FUSION", Central: true, Delivery: failing("rpc error: code = Unavailable desc = connection error: i/o timeout")}
	peerOK := []peerOutcome{{Name: "athens-edge", Address: "203.0.113.50:4317", State: "exporting"}}

	for _, c := range []struct {
		name   string
		state  operators.HealthState
		peers  []peerOutcome
		where  string
		phrase string
	}{
		{"off", operators.HealthOff, nil, "operator", "FUSION is off"},
		{"starting", operators.HealthStarting, nil, "operator", "still starting"},
		{"attention", operators.HealthAttention, nil, "operator", "reports a problem"},
		// Another cluster getting through is stronger evidence than FUSION's own word: the path is what is wrong.
		{"off while a peer exports", operators.HealthOff, peerOK, "sender-network", "athens-edge is exporting"},
		{"online, nobody to compare", operators.HealthOnline, nil, "sender-network", "reports itself online"},
	} {
		in := base
		in.OperatorState, in.Peers = c.state, c.peers
		got := diagnoseSender(in)
		if got == nil || got.Where != c.where || !strings.Contains(got.Title+" "+got.Detail, c.phrase) {
			t.Errorf("%s: %+v, want %s mentioning %q", c.name, got, c.where, c.phrase)
		}
	}

	// A certificate refusal does not send anyone to a "Renew certificates" row the central operator does not have.
	in := base
	in.OperatorState = operators.HealthOnline
	in.Delivery = failing("x509: certificate signed by unknown authority")
	got := diagnoseSender(in)
	if got == nil || got.Where != "certificate" || strings.Contains(got.Detail, "Renew certificates") {
		t.Errorf("certificate, central: %+v", got)
	}
}
