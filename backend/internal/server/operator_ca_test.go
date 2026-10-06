package server

import (
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"continuum/internal/pki"
	"continuum/internal/store"

	_ "modernc.org/sqlite"
)

// rawDB opens the env's database file directly, to look at (or tamper with) what is really stored.
func rawDB(t *testing.T, e *env) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// withPassphraseCA swaps the env's org CA for one whose key is encrypted at rest, as a production server's is.
func withPassphraseCA(t *testing.T, e *env, passphrase string) {
	t.Helper()
	ca, err := pki.LoadOrCreateWith(filepath.Join(t.TempDir(), "pki"), pki.Options{Passphrase: []byte(passphrase)})
	if err != nil {
		t.Fatal(err)
	}
	e.base.CA = ca
	e.core = e.base.ForOrg("org-1")
}

func certOf(t *testing.T, p []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(p)
	if b == nil {
		t.Fatalf("not PEM: %q", p)
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func poolFrom(t *testing.T, p []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(p) {
		t.Fatal("no certificate in PEM")
	}
	return pool
}

func clientOK(c *x509.Certificate, pool *x509.CertPool) bool {
	_, err := c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	return err == nil
}

// legacyMTLSOperator inserts an mTLS operator the way 95427ee created them: no CA of its own.
func legacyMTLSOperator(t *testing.T, e *env, id, cluster string) store.Operator {
	t.Helper()
	op := store.Operator{ID: id, OrgID: "org-1", Name: id, Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		SourceClusterIDs: []string{cluster}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: *e.now}
	if err := e.st.CreateOperator(e.ctx, op, nil); err != nil {
		t.Fatal(err)
	}
	return op
}

func TestCreateOperatorMintsItsOwnCAAndStoresNoPlaintextKey(t *testing.T) {
	e := newEnv(t)
	withPassphraseCA(t, e, "correct horse battery staple")
	cl := e.approvedCluster(t, fp)
	op, _, bundle, err := e.core.CreateOperator(e.ctx, "alex", "athens", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if op.ClientCAScope() != store.ClientCAScopeOperator {
		t.Fatalf("scope = %q", op.ClientCAScope())
	}
	// The bundle's CA is the operator's own, not the org CA, and it signed BOTH the receiver certificate and
	// the client certificate.
	if string(bundle.CACertPEM) == string(e.core.CA.CertPEM()) {
		t.Fatal("the bundle hands out the org CA for an operator-CA operator")
	}
	opPool := poolFrom(t, bundle.CACertPEM)
	if !clientOK(certOf(t, bundle.ClientCertPEM), opPool) {
		t.Fatal("client certificate does not verify against the operator CA")
	}
	if _, err := certOf(t, bundle.ReceiverCertPEM).Verify(x509.VerifyOptions{Roots: opPool, DNSName: op.ID + ".continuum-system.svc", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("receiver certificate does not verify against the operator CA: %v", err)
	}
	// And by the Service name the chart creates, which is what a sender in the same cluster dials.
	if _, err := certOf(t, bundle.ReceiverCertPEM).Verify(x509.VerifyOptions{Roots: opPool, DNSName: operatorServiceName(op.ID) + ".continuum-system.svc", KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("receiver certificate does not carry the Service name: %v", err)
	}
	if clientOK(certOf(t, bundle.ClientCertPEM), e.core.CA.Pool()) {
		t.Fatal("client certificate verifies against the org CA")
	}
	// Stored: the certificate (public) as read back, and a sealed key - encrypted, no plaintext key block.
	stored, err := e.st.GetOperator(e.ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored.ClientCACertPEM) != string(bundle.CACertPEM) || len(stored.ClientCAKeyPEM) != 0 {
		t.Fatalf("stored cert/key: %q / %d key bytes returned by a plain read", stored.ClientCACertPEM, len(stored.ClientCAKeyPEM))
	}
	key, err := e.st.GetOperatorClientCAKey(e.ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blk, _ := pem.Decode(key); blk == nil || blk.Type != "CONTINUUM ENCRYPTED CA KEY" {
		t.Fatalf("the stored key is not an encrypted CA key block: %q", key)
	}
	// Nothing in the operator row is a plaintext private key or the receiver/client keys that were shown once.
	var plain int
	if err := rawDB(t, e).QueryRow(`SELECT count(*) FROM operators WHERE instr(CAST(client_ca_key AS TEXT),'PRIVATE KEY')>0 OR instr(CAST(client_ca_cert AS TEXT),'PRIVATE KEY')>0`).Scan(&plain); err != nil || plain != 0 {
		t.Fatalf("plaintext key in the database: %d %v", plain, err)
	}
	// The audit trail carries the fact, never key material.
	_, trail := auditActions(t, e)
	if strings.Contains(trail, "PRIVATE KEY") || strings.Contains(trail, string(key)) {
		t.Fatalf("key material in the audit trail:\n%s", trail)
	}
	// Each operator gets a different CA.
	op2, _, b2, err := e.core.CreateOperator(e.ctx, "alex", "sparta", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(b2.CACertPEM) == string(bundle.CACertPEM) || op2.ID == op.ID {
		t.Fatal("two operators share a CA")
	}
}

func TestReissueUsesTheOperatorCAAndRefusesToFallBackToTheOrgCA(t *testing.T) {
	e := newEnv(t)
	withPassphraseCA(t, e, "correct horse battery staple")
	cl := e.approvedCluster(t, fp)
	a, _, bundleA, err := e.core.CreateOperator(e.ctx, "alex", "a", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, bundleB, err := e.core.CreateOperator(e.ctx, "alex", "b", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, caPEM, err := e.core.IssueOperatorClientCert(e.ctx, "alex", a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(caPEM) != string(bundleA.CACertPEM) {
		t.Fatal("reissue returns a CA that is not the operator's own")
	}
	c := certOf(t, certPEM)
	if !clientOK(c, poolFrom(t, bundleA.CACertPEM)) {
		t.Fatal("reissued certificate does not verify against its operator's CA")
	}
	// Cross-operator and org-CA certificates do not verify against another operator's pool.
	if clientOK(c, poolFrom(t, bundleB.CACertPEM)) {
		t.Fatal("A's reissued certificate verifies against B's CA")
	}
	if clientOK(c, e.core.CA.Pool()) {
		t.Fatal("A's reissued certificate verifies against the org CA")
	}
	if c.Subject.CommonName != a.ID+"-export" || c.Subject.Organization[0] != "org-1" {
		t.Fatalf("subject = %v", c.Subject)
	}
	// Audited, with which CA and no key material.
	_, trail := auditActions(t, e)
	if !strings.Contains(trail, "operator-client-cert-reissued|operator|"+a.ID+"|ca=operator") {
		t.Fatalf("reissue not audited as ca=operator:\n%s", trail)
	}
	if strings.Contains(trail, "PRIVATE KEY") || strings.Contains(trail, string(keyPEM)) {
		t.Fatalf("key material in the audit trail:\n%s", trail)
	}
	_ = b
	// If the operator's key goes missing the reissue FAILS; it must never quietly sign with the org CA
	// (the Secret it returns would not match what the receiver trusts, and the org CA is the wrong scope).
	if _, err := rawDB(t, e).Exec(`UPDATE operators SET client_ca_key=NULL WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := e.core.IssueOperatorClientCert(e.ctx, "alex", a.ID); err == nil {
		t.Fatal("reissue succeeded without the operator's CA key")
	}
	// A CA whose key was sealed under a different passphrase cannot be reopened either.
	other := newEnv(t)
	withPassphraseCA(t, other, "an entirely different passphrase")
	e.base.CA = other.base.CA
	e.core = e.base.ForOrg("org-1")
	if _, _, _, err := e.core.IssueOperatorClientCert(e.ctx, "alex", b.ID); !errors.Is(err, pki.ErrWrongPassphrase) {
		t.Fatalf("a server with another CA passphrase issued from operator b's CA: %v", err)
	}
}

func TestLegacyMTLSOperatorKeepsIssuingFromTheOrgCA(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op := legacyMTLSOperator(t, e, "op-legacy000001", cl)
	if got, _ := e.st.GetOperator(e.ctx, op.ID); got.ClientCAScope() != store.ClientCAScopeOrg {
		t.Fatalf("scope = %q", got.ClientCAScope())
	}
	certPEM, _, caPEM, err := e.core.IssueOperatorClientCert(e.ctx, "alex", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(caPEM) != string(e.core.CA.CertPEM()) || !clientOK(certOf(t, certPEM), e.core.CA.Pool()) {
		t.Fatal("a legacy mTLS operator is no longer served from the org CA its receiver trusts")
	}
	if _, trail := auditActions(t, e); !strings.Contains(trail, "operator-client-cert-reissued|operator|"+op.ID+"|ca=org") {
		t.Fatalf("legacy reissue not audited as ca=org:\n%s", trail)
	}
}

func TestRevokeAndDeleteDropTheOperatorCAKey(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	a, _, _, _ := e.core.CreateOperator(e.ctx, "alex", "a", []string{cl}, extDest("c:4317"), nil)
	b, _, _, _ := e.core.CreateOperator(e.ctx, "alex", "b", []string{cl}, extDest("c:4317"), nil)
	for _, id := range []string{a.ID, b.ID} {
		if _, err := e.st.GetOperatorClientCAKey(e.ctx, id); err != nil {
			t.Fatalf("%s: no key to start with: %v", id, err)
		}
	}
	if err := e.core.RevokeOperator(e.ctx, "alex", a.ID, "gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetOperatorClientCAKey(e.ctx, a.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoked operator keeps its CA key: %v", err)
	}
	if _, err := e.st.GetOperatorClientCAKey(e.ctx, b.ID); err != nil {
		t.Fatalf("revoking a erased b's key: %v", err)
	}
	if err := e.core.DeleteOperator(e.ctx, "alex", b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetOperatorClientCAKey(e.ctx, b.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted operator keeps its CA key: %v", err)
	}
}

// operatorDoc.clientCaScope for each kind, through the HTTP API; the commands hand out the operator's CA, and
// only for an operator that has one.
func TestClientCaScopeAndCommandsCarryTheRightCA(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "new", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	created := r.json(t)
	opDoc := created["operator"].(map[string]any)
	if opDoc["clientCaScope"] != "operator" || opDoc["receiverAuth"] != "mtls" {
		t.Fatalf("new operator doc: %v", opDoc)
	}
	newOp, err := a.st.GetOperator(a.ctx, opDoc["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	orgCA, opCA := string(a.core.CA.CertPEM()), string(newOp.ClientCACertPEM)
	if orgCA == opCA {
		t.Fatal("test setup: same CA")
	}
	// Receiver Secret, and every agent-side Secret in the reminders, carry ca.crt = the operator CA.
	tlsCmd := created["tlsSecretCommand"].(string)
	if !strings.Contains(tlsCmd, pemBlock("ca.crt", opCA)) || strings.Contains(tlsCmd, orgCA) {
		t.Fatalf("receiver Secret command does not carry exactly the operator CA:\n%s", tlsCmd)
	}
	reminders := created["reminders"].([]any)
	sawSecret := false
	for _, x := range reminders {
		s := x.(string)
		if strings.Contains(s, "kind: Secret") {
			sawSecret = true
			if !strings.Contains(s, pemBlock("ca.crt", opCA)) || strings.Contains(s, orgCA) {
				t.Fatalf("source reminder Secret does not carry exactly the operator CA:\n%s", s)
			}
		}
	}
	if !sawSecret {
		t.Fatalf("no client Secret in the reminders: %v", reminders)
	}
	// Scope update re-mints the client certificate: still the operator CA (and audited).
	r = a.do("POST", "/api/v1/operators/"+newOp.ID+"/scope", map[string]any{"sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
	if r.Code != 200 {
		t.Fatalf("scope update: %d %s", r.Code, r.Body.String())
	}
	for _, x := range r.json(t)["reminders"].([]any) {
		if s := x.(string); strings.Contains(s, "kind: Secret") && (!strings.Contains(s, pemBlock("ca.crt", opCA)) || strings.Contains(s, orgCA)) {
			t.Fatalf("scope-update reminder does not carry exactly the operator CA:\n%s", s)
		}
	}
	// Telemetry-intent /command: same.
	agentID := a.approvedAgentID(t, fp2)
	ir := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agentID, "name": "e", "destination": map[string]any{"kind": "operator", "targetOperatorId": newOp.ID}}, withCookie(cookie))
	if ir.Code != 201 {
		t.Fatalf("%d %s", ir.Code, ir.Body.String())
	}
	cmd := a.do("POST", "/api/v1/telemetry-intents/"+ir.json(t)["id"].(string)+"/command", nil, withCookie(cookie)).json(t)
	secrets, _ := cmd["secretCommands"].([]any)
	if len(secrets) != 1 || !strings.Contains(secrets[0].(string), pemBlock("ca.crt", opCA)) || strings.Contains(secrets[0].(string), orgCA) {
		t.Fatalf("/command Secret does not carry exactly the operator CA: %v", cmd)
	}

	// Legacy mTLS: "org". Bearer: "". Both listed by the API.
	legacyMTLSOperator(t, a.env, "op-legacy000009", cl)
	bearer := store.Operator{ID: "op-bearer000009", OrgID: "org-1", Name: "bearer", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer,
		SourceClusterIDs: []string{cl}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: *a.now}
	if err := a.st.CreateOperator(a.ctx, bearer, HashSecret("cno_old")); err != nil {
		t.Fatal(err)
	}
	scopes := map[string]any{}
	for _, o := range a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).jsonArray(t) {
		scopes[o["id"].(string)] = o["clientCaScope"]
	}
	if scopes[newOp.ID] != "operator" || scopes["op-legacy000009"] != "org" || scopes["op-bearer000009"] != "" {
		t.Fatalf("clientCaScope by operator = %v", scopes)
	}
}

// A bearer operator is unchanged: its client certificates still come from the org CA, the same as before
// per-operator CAs existed, and nothing about it gains a CA.
func TestBearerOperatorStillUsesTheOrgCA(t *testing.T) {
	failTLSMint(t) // creation falls back to bearer
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	op, tok, _, err := e.core.CreateOperator(e.ctx, "alex", "b", []string{cl}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" || op.ReceiverAuth != store.ReceiverAuthBearer || op.ClientCAScope() != "" || len(op.ClientCACertPEM) != 0 {
		t.Fatalf("%+v", op)
	}
	if _, err := e.st.GetOperatorClientCAKey(e.ctx, op.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a bearer operator has a CA key: %v", err)
	}
	certPEM, _, caPEM, err := e.core.IssueOperatorClientCert(e.ctx, "alex", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(caPEM) != string(e.core.CA.CertPEM()) || !clientOK(certOf(t, certPEM), e.core.CA.Pool()) {
		t.Fatal("a bearer operator's client certificate no longer comes from the org CA")
	}
}

// pemBlock is a PEM value as the generated Secret manifest writes it: a literal block under its key.
func pemBlock(key, pem string) string {
	return "  " + key + ": |\n    " + strings.ReplaceAll(strings.TrimRight(pem, "\n"), "\n", "\n    ") + "\n"
}
