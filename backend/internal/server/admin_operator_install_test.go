package server

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"

	"continuum/internal/store"
)

// pemAfter is the PEM value of one key of a generated Secret manifest, as the cluster would read it.
func pemAfter(t *testing.T, manifest, key string) []byte {
	t.Helper()
	_, rest, ok := strings.Cut(manifest, "\n  "+key+": |\n")
	if !ok {
		t.Fatalf("no %s in:\n%s", key, manifest)
	}
	var lines []string
	for _, l := range strings.Split(rest, "\n") {
		if !strings.HasPrefix(l, "    ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(l, "    "))
	}
	return []byte(strings.Join(lines, "\n") + "\n")
}

func (a *adminRig) createOperatorDoc(t *testing.T, cookie string, body map[string]any) map[string]any {
	t.Helper()
	r := a.do("POST", "/api/v1/operators", body, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	return r.json(t)
}

func extBody(name string, clusters ...string) map[string]any {
	return map[string]any{"name": name, "sourceClusterIds": clusters, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}
}

// Installing again hands out the commands with fresh certificates the operator's own CA signed, upgrades the release
// in place, records the new dates and is audited; the first install's certificates keep working until they expire.
func TestInstallAgainReissuesCertificatesFromTheStoredCA(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	created := a.createOperatorDoc(t, cookie, extBody("athens", cl))
	id := created["operator"].(map[string]any)["id"].(string)
	firstRecv := pemAfter(t, created["tlsSecretCommand"].(string), "tls.crt")

	r := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("install again: %d %s", r.Code, r.Body.String())
	}
	doc := r.json(t)
	install := doc["install"].(string)
	if !strings.HasPrefix(install, "helm upgrade --install "+id+" ") || !strings.Contains(install, "--reset-then-reuse-values") || !strings.Contains(install, "--set receiver.tls.secretName="+id+"-receiver-tls") {
		t.Fatalf("install command:\n%s", install)
	}
	// Whole export block stated, since the release is upgraded with --reset-then-reuse-values.
	for _, want := range []string{"--set export.otlp.endpoint=c:4317", "--set export.otlp.tls.insecure=false", "--set export.otlp.tls.caFile=", "--set export.otlp.auth.secretName="} {
		if !strings.Contains(install, want) {
			t.Fatalf("install lacks %q:\n%s", want, install)
		}
	}
	if _, has := doc["token"]; has {
		t.Fatalf("an mTLS operator has no receiver token: %v", doc["token"])
	}
	if _, has := doc["heartbeatToken"]; has {
		t.Fatal("a heartbeat secret for an operator with no heartbeat")
	}
	if doc["restartCommand"] == nil {
		t.Fatal("no restart command")
	}

	// The new receiver certificate is signed by the operator's own CA, for the stable name, and is a new certificate.
	stored, _ := a.st.GetOperator(a.ctx, id)
	recv := pemAfter(t, doc["tlsSecretCommand"].(string), "tls.crt")
	if string(recv) == string(firstRecv) {
		t.Fatal("the receiver certificate was not re-issued")
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(stored.ClientCACertPEM)
	if string(pemAfter(t, doc["tlsSecretCommand"].(string), "ca.crt")) != string(stored.ClientCACertPEM) {
		t.Fatal("the Secret does not carry the operator's CA")
	}
	b, _ := pem.Decode(recv)
	leaf, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: operatorServerName(stored), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("the new receiver certificate does not verify against the operator CA for the stable name: %v", err)
	}
	// ... and the source cluster gets a client certificate in its reminders.
	if reminders := doc["reminders"].([]any); len(reminders) != 2 || !strings.Contains(reminders[0].(string), "-----BEGIN CERTIFICATE-----") {
		t.Fatalf("reminders = %v", reminders)
	}
	if n, detail := (&env{st: a.st, ctx: a.ctx}).countAudit(t, "operator-install-reissued"); n != 1 || strings.Contains(detail, "BEGIN") {
		t.Fatalf("audit entries = %d (%q)", n, detail)
	}
	if got := doc["operator"].(map[string]any)["certs"]; got == nil {
		t.Fatal("the operator document has no certs")
	}
}

// Rotating what is hashed: the heartbeat secret of an operator that has one is replaced and the old one stops working.
func TestInstallAgainRotatesTheHeartbeatSecret(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := extBody("athens", cl)
	body["heartbeat"] = true
	created := a.createOperatorDoc(t, cookie, body)
	id := created["operator"].(map[string]any)["id"].(string)
	first := created["heartbeatToken"].(string)
	if a.beat(first, nil).Code != 200 {
		t.Fatal("the first heartbeat secret does not work")
	}
	doc := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(cookie)).json(t)
	second, _ := doc["heartbeatToken"].(string)
	if second == "" || second == first {
		t.Fatalf("heartbeat token = %q", second)
	}
	if a.beat(first, nil).Code != 401 || a.beat(second, nil).Code != 200 {
		t.Fatal("the heartbeat secret was not swapped")
	}
	install := doc["install"].(string)
	if !strings.Contains(install, "--set heartbeat.enabled=true") || !strings.Contains(install, "--set heartbeat.auth.secretName="+id+"-heartbeat-auth") {
		t.Fatalf("install command carries no heartbeat flags:\n%s", install)
	}
	if sc, _ := doc["heartbeatSecretCommand"].(string); !strings.Contains(sc, second) || strings.Contains(sc, "--from-literal") {
		t.Fatalf("heartbeat secret command = %q", sc)
	}
}

// A bearer operator has a token instead of certificates: it is replaced, and the old one no longer matches.
func TestInstallAgainRotatesABearerOperatorsToken(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	const id = "op-bearer000007"
	legacy := store.Operator{ID: id, OrgID: "org-1", Name: "bearer", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer,
		SourceClusterIDs: []string{cl}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: a.a.C.Now()}
	if err := a.st.CreateOperator(a.ctx, legacy, HashSecret("cno_old")); err != nil {
		t.Fatal(err)
	}
	created := map[string]any{"token": "cno_old"}
	before, _ := a.st.GetOperator(a.ctx, id)
	resp := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(cookie))
	if resp.Code != 200 {
		t.Fatalf("%d %s", resp.Code, resp.Body.String())
	}
	doc := resp.json(t)
	tok, _ := doc["token"].(string)
	if !strings.HasPrefix(tok, operatorPrefix) || tok == created["token"] || doc["secretCommand"] == nil {
		t.Fatalf("token = %q, secretCommand = %v", tok, doc["secretCommand"])
	}
	after, _ := a.st.GetOperator(a.ctx, id)
	if string(after.ReceiverAuthTokenHash) != string(HashSecret(tok)) || string(after.ReceiverAuthTokenHash) == string(before.ReceiverAuthTokenHash) {
		t.Fatal("the stored hash is not the new token's")
	}
	if _, has := doc["tlsSecretCommand"]; has {
		t.Fatal("certificates for a bearer operator")
	}
}

func TestInstallAgainIsForActiveOperatorsAdminsOnly(t *testing.T) {
	a := newAdminRig(t)
	_, admin := a.user(t, "alex", RoleAdmin)
	_, editor := a.user(t, "ed", RoleEditor)
	cl := a.approvedCluster(t, fp)
	id := a.createOperatorDoc(t, admin, extBody("athens", cl))["operator"].(map[string]any)["id"].(string)
	if r := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(editor)); r.Code != 403 {
		t.Fatalf("editor: %d", r.Code)
	}
	if r := a.do("POST", "/api/v1/operators/op-nope/install", nil, withCookie(admin)); r.Code != 404 {
		t.Fatalf("unknown: %d", r.Code)
	}
	if r := a.do("POST", "/api/v1/operators/"+CentralOperatorID+"/install", nil, withCookie(admin)); r.Code != 409 {
		t.Fatalf("central: %d", r.Code)
	}
	if r := a.do("POST", "/api/v1/operators/"+id+"/revoke", map[string]any{"reason": "x"}, withCookie(admin)); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	if r := a.do("POST", "/api/v1/operators/"+id+"/install", nil, withCookie(admin)); r.Code != 409 {
		t.Fatalf("revoked: %d", r.Code)
	}
}

// Legacy operators (no stored dates, the org CA) can be installed again too, and get dates recorded.
func TestInstallAgainRecordsDatesForAnOperatorThatHadNone(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	legacy := store.Operator{ID: "op-legacy000003", OrgID: "org-1", Name: "legacy", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		SourceClusterIDs: []string{cl}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: a.now.Add(-300 * day)}
	if err := a.st.CreateOperator(a.ctx, legacy, nil); err != nil {
		t.Fatal(err)
	}
	r := a.do("POST", "/api/v1/operators/"+legacy.ID+"/install", nil, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	// Signed by the org CA it already trusts.
	if string(pemAfter(t, r.json(t)["tlsSecretCommand"].(string), "ca.crt")) != string(a.core.CA.CertPEM()) {
		t.Fatal("a legacy operator's receiver trusts the org CA")
	}
	got, _ := a.st.GetOperator(a.ctx, legacy.ID)
	if got.ReceiverNotAfter == nil || got.ReceiverNotAfter.Before(time.Now().Add(25*day)) {
		t.Fatalf("receiver until %v", got.ReceiverNotAfter)
	}
}

// An administrator of one organisation can read, reissue, change, revoke or delete none of another's operators, by
// either door: through the other organisation's path (not a member) or through their own with the other's operator id
// (not found there). Nothing is minted, nothing is audited in the wrong trail, and the destination read model lists
// only the caller's own.
func TestAnotherOrganisationsOperatorIsOutOfReachOfEveryOperatorRoute(t *testing.T) {
	a := newAdminRig(t)
	alice, aOrg := a.register(t, "alice", "Alice Lab")
	bob, bOrg := a.register(t, "bob", "Bob Works")
	r := a.do("POST", org(aOrg, "operators"), extBody("alice-op"), withCookie(alice))
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body.String())
	}
	id := r.json(t)["operator"].(map[string]any)["id"].(string)
	for i, rt := range []struct {
		m, p string
		body map[string]any
	}{
		{"GET", "operators/" + id, nil}, {"POST", "operators/" + id + "/install", nil},
		{"POST", "operators/" + id + "/revoke", map[string]any{"reason": "x", "force": true}},
		{"DELETE", "operators/" + id, nil}, {"DELETE", "operators/" + id + "?force=true", nil},
		{"POST", "operators/" + id + "/scope", map[string]any{"sourceClusterIds": []string{}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}},
		{"POST", "operators/" + id + "/address", map[string]any{"address": "x.example.com:4317"}}, {"POST", "operators/" + id + "/heartbeat", nil},
	} {
		body := rt.body
		if c := a.do(rt.m, org(aOrg, rt.p), body, withCookie(bob), fromIP(fmt.Sprintf("10.30.0.%d", i+1))).Code; c != 404 {
			t.Errorf("Bob %s %s through Alice's organisation: %d, want 404", rt.m, rt.p, c)
		}
		if c := a.do(rt.m, org(bOrg, rt.p), body, withCookie(bob)).Code; c != 404 {
			t.Errorf("Bob %s %s through his own organisation with Alice's operator: %d, want 404", rt.m, rt.p, c)
		}
	}
	if got, err := a.st.GetOperator(a.ctx, id); err != nil || got.Status != store.OperatorActive || got.Address != "" || len(got.HeartbeatHash) != 0 {
		t.Fatalf("Alice's operator was touched: %+v %v", got, err)
	}
	if evs, _ := a.st.ListAudit(a.ctx, bOrg, 50); len(evs) != 0 {
		for _, e := range evs {
			if strings.HasPrefix(e.Action, "operator-") {
				t.Errorf("Bob's trail holds %+v", e)
			}
		}
	}
	for who, cookie := range map[string]string{"alice": alice, "bob": bob} {
		body := a.do("GET", org(map[string]string{"alice": aOrg, "bob": bOrg}[who], "operator-destinations"), nil, withCookie(cookie)).Body.String()
		if has := strings.Contains(body, id); has != (who == "alice") {
			t.Errorf("%s's destination list contains Alice's operator = %v", who, has)
		}
	}
}
