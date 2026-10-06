package server

import (
	"strings"
	"testing"
)

// Creating an operator for two clusters issues each its own certificate, and the ledger says so: one receiver
// certificate and one client certificate per cluster, each naming its holder. The listing is admin-only and carries
// no certificate or key.
func TestOperatorCertificatesLedgerNamesWhoHoldsWhat(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	clA, clB := a.approvedCluster(t, fp), a.approvedCluster(t, fp2)
	id := a.createOperatorDoc(t, admin, extBody("athens", clA, clB))["operator"].(map[string]any)["id"].(string)

	list := func() []any {
		t.Helper()
		r := a.do("GET", "/api/v1/operators/"+id+"/certificates", nil, withCookie(admin))
		if r.Code != 200 {
			t.Fatalf("list: %d %s", r.Code, r.Body.String())
		}
		for _, banned := range []string{"PRIVATE KEY", "BEGIN CERTIFICATE"} {
			if strings.Contains(r.Body.String(), banned) {
				t.Fatalf("the ledger listing carries %s", banned)
			}
		}
		return r.json(t)["certificates"].([]any)
	}
	holders := map[string]int{}
	receivers := 0
	for _, c := range list() {
		m := c.(map[string]any)
		switch m["kind"] {
		case "receiver":
			receivers++
		case "client":
			holders[m["sender"].(string)]++
			if want := id + "-export-" + m["sender"].(string); m["subject"] != want {
				t.Fatalf("subject %v, want %s", m["subject"], want)
			}
		}
		if m["state"] != "ok" || m["issuedBy"] != "alex" {
			t.Fatalf("entry = %v", m)
		}
	}
	if receivers != 1 || holders[clA] != 1 || holders[clB] != 1 || len(holders) != 2 {
		t.Fatalf("receivers %d, holders %v", receivers, holders)
	}

	// Installing again issues new ones and keeps the record of the old (they still work until they end).
	if r := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(admin)); r.Code != 200 {
		t.Fatalf("install again: %d %s", r.Code, r.Body.String())
	}
	if n := len(list()); n != 6 {
		t.Fatalf("ledger has %d entries after installing again, want 6", n)
	}

	if r := a.do("GET", "/api/v1/operators/"+id+"/certificates", nil, withCookie(editor)); r.Code != 403 {
		t.Fatalf("an editor read the ledger: %d", r.Code)
	}
	if r := a.do("GET", "/api/v1/operators/op-nope/certificates", nil, withCookie(admin)); r.Code != 404 {
		t.Fatalf("unknown operator: %d", r.Code)
	}
}

// The central operator's CA cannot be renewed by FUSION's daily certificate loop, so its end must raise the same
// warning an ordinary operator's does.
func TestCentralOperatorCAExpiryRaisesTheWarning(t *testing.T) {
	e := newEnv(t)
	op, _, err := e.core.EnsureCentralOperator(e.ctx, "alex", extDest("c:4317"), []string{"op-central.continuum-system.svc"})
	if err != nil {
		t.Fatal(err)
	}
	caEnd := *certNotAfter(op.ClientCACertPEM)
	*e.now = caEnd.Add(-20 * day)
	n, err := e.core.CheckOperatorCerts(e.ctx)
	if err != nil || n != 1 {
		t.Fatalf("raised %d (%v), want the CA's warning", n, err)
	}
	if _, detail := e.countAudit(t, "operator-cert-expiring"); !strings.Contains(detail, "CA certificate") {
		t.Fatalf("warning = %q", detail)
	}
}
