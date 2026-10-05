package server

import (
	"errors"
	"strings"
	"testing"

	"continuum/internal/store"
)

// failTLSMint makes every operator TLS mint fail for the rest of the test.
func failTLSMint(t *testing.T) {
	t.Helper()
	old := mintOperatorTLS
	mintOperatorTLS = func(*Core, string, []string) (OperatorTLSBundle, []byte, error) {
		return OperatorTLSBundle{}, nil, errors.New("simulated mint failure")
	}
	t.Cleanup(func() { mintOperatorTLS = old })
}

func TestInstallCommandForAnMTLSOperatorHasNoBearerGate(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	created := r.json(t)
	install := created["install"].(string)
	id := created["operator"].(map[string]any)["id"].(string)
	for _, want := range []string{
		"--set receiver.auth.enabled=false",
		"--set receiver.requireAuth=true",
		"--set receiver.tls.enabled=true",
		"--set receiver.tls.secretName=" + id + "-receiver-tls",
		"--set receiver.tls.mtls=true",
	} {
		if !strings.Contains(install, want) {
			t.Fatalf("install command lacks %q:\n%s", want, install)
		}
	}
	for _, bad := range []string{"receiver.auth.enabled=true", "receiver.auth.secretName", "-receiver-auth"} {
		if strings.Contains(install, bad) {
			t.Fatalf("an mTLS operator's install command mentions %q:\n%s", bad, install)
		}
	}
	if created["tlsSecretCommand"] == nil {
		t.Fatal("the receiver certificate Secret command - the gate itself - is missing")
	}
}

// A bearer operator's commands are exactly what they were before ReceiverAuth existed, plus the one
// --set-json operator=... that stamps who it is (id, name, labels) on what it forwards.
func TestInstallCommandForABearerOperatorIsUnchanged(t *testing.T) {
	a := newAdminRig(t)
	op := store.Operator{ID: "op-abc123", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "c:4317"}}
	withTLS := OperatorTLSBundle{ReceiverCertPEM: []byte("x")}

	got, secretCmd := a.a.operatorInstallCommand(ImageConfig{}, "cno_SECRET", op, withTLS, "")
	want := "helm install op-abc123 chart --version 0.1.0 \\\n  --namespace continuum-system --create-namespace \\\n  --set export.otlp.endpoint=c:4317" +
		" \\\n  --set receiver.auth.enabled=true \\\n  --set receiver.auth.secretName=op-abc123-receiver-auth" +
		" \\\n  --set receiver.tls.enabled=true \\\n  --set receiver.tls.secretName=op-abc123-receiver-tls \\\n  --set receiver.tls.mtls=true" +
		" \\\n  --set-json operator='{\"id\":\"op-abc123\",\"name\":\"\",\"labels\":[]}'"
	if got != want {
		t.Fatalf("bearer install command changed:\n got: %q\nwant: %q", got, want)
	}
	if secretCmd != "kubectl create secret generic op-abc123-receiver-auth --namespace continuum-system \\\n  --from-literal=token=cno_SECRET \\\n  --dry-run=client -o yaml | kubectl apply -f -" {
		t.Fatalf("secret command = %q", secretCmd)
	}
	// No certificates minted: bearer only, as before.
	got, _ = a.a.operatorInstallCommand(ImageConfig{}, "cno_SECRET", op, OperatorTLSBundle{}, "")
	if strings.Contains(got, "receiver.tls") || !strings.Contains(got, "receiver.auth.enabled=true") || strings.Contains(got, "requireAuth") {
		t.Fatalf("bearer operator without certificates: %s", got)
	}
	// A row that predates the field (zero value) is a bearer operator too.
	op.ReceiverAuth = ""
	if again, _ := a.a.operatorInstallCommand(ImageConfig{}, "cno_SECRET", op, OperatorTLSBundle{}, ""); again != got {
		t.Fatalf("a zero ReceiverAuth is not treated as bearer:\n%s", again)
	}
}

// If the TLS material cannot be minted the receiver must still have a gate: the operator is a bearer one,
// its token is minted, and it is installed with receiver.auth on.
func TestCreateOperatorFallsBackToBearerWhenTheTLSMintFails(t *testing.T) {
	failTLSMint(t)
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	r := a.do("POST", "/api/v1/operators", map[string]any{"name": "x", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	created := r.json(t)
	op := created["operator"].(map[string]any)
	if op["receiverAuth"] != "bearer" {
		t.Fatalf("receiverAuth = %v, want bearer after a failed mint", op["receiverAuth"])
	}
	tok, _ := created["token"].(string)
	if !strings.HasPrefix(tok, operatorPrefix) || created["secretCommand"] == nil {
		t.Fatalf("no receiver token minted for the fallback: %v", created)
	}
	install := created["install"].(string)
	if !strings.Contains(install, "receiver.auth.enabled=true") || strings.Contains(install, "receiver.auth.enabled=false") || strings.Contains(install, "receiver.tls") {
		t.Fatalf("fallback install command leaves the receiver without its bearer gate:\n%s", install)
	}
	stored, _ := a.st.GetOperator(a.ctx, op["id"].(string))
	if stored.ReceiverAuth != store.ReceiverAuthBearer || len(stored.ReceiverAuthTokenHash) == 0 || string(stored.ReceiverAuthTokenHash) == tok {
		t.Fatalf("stored = %+v", stored)
	}
	if _, has := created["tlsSecretCommand"]; has {
		t.Fatal("a receiver TLS Secret command without certificates")
	}
}

// operatorDestinationCommand builds the same flags for both kinds - neither needs a bearer in the agent's
// export flags - and the /command response says which kind it was talking to, so a caller knows whether a
// receiver token is its own business.
func TestOperatorDestinationCommandIsTheSameForBothAuthModes(t *testing.T) {
	for _, mode := range []store.ReceiverAuth{store.ReceiverAuthMTLS, store.ReceiverAuthBearer} {
		op := store.Operator{ID: "op-abc123", ReceiverAuth: mode}
		flags, secret := operatorDestinationCommand(op, []byte("CERT"), []byte("KEY"), []byte("CA"), "ns1")
		wantFlags := "--set telemetry.export.otlp.endpoint=op-abc123.continuum-system.svc:4317 --set telemetry.export.otlp.tls.mtls.enabled=true --set telemetry.export.otlp.tls.mtls.secretName=op-abc123-export-mtls"
		wantSecret := "kubectl create secret generic op-abc123-export-mtls --namespace ns1 \\\n  --from-literal=tls.crt=\"CERT\" \\\n  --from-literal=tls.key=\"KEY\" \\\n  --from-literal=ca.crt=\"CA\" \\\n  --dry-run=client -o yaml | kubectl apply -f -"
		if flags != wantFlags || secret != wantSecret {
			t.Fatalf("%s: flags=%q secret=%q", mode, flags, secret)
		}
		if strings.Contains(flags, "auth") {
			t.Fatalf("%s: the export flags mention auth: %q", mode, flags)
		}
	}
}

func TestIntentCommandReportsTheOperatorsReceiverAuth(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	opCluster := a.approvedCluster(t, fp)
	mtls, _, _, err := a.core.CreateOperator(a.ctx, "alex", "new", []string{opCluster}, extDest("c:4317"), nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := store.Operator{ID: "op-legacy000001", OrgID: "org-1", Name: "legacy", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer,
		SourceClusterIDs: []string{opCluster}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: *a.now}
	if err := a.st.CreateOperator(a.ctx, legacy, HashSecret("cno_old")); err != nil {
		t.Fatal(err)
	}
	agentID := a.approvedAgentID(t, fp2)
	for _, c := range []struct {
		id   string
		want string
	}{{mtls.ID, "mtls"}, {legacy.ID, "bearer"}} {
		r := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agentID, "name": "e-" + c.want, "destination": map[string]any{"kind": "operator", "targetOperatorId": c.id}}, withCookie(cookie))
		if r.Code != 201 {
			t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
		}
		iid := r.json(t)["id"].(string)
		r = a.do("POST", "/api/v1/telemetry-intents/"+iid+"/command", nil, withCookie(cookie))
		doc := r.json(t)
		if r.Code != 200 || doc["receiverAuth"] != c.want {
			t.Fatalf("%s: %d %v", c.want, r.Code, doc)
		}
		if frag := doc["installFragment"].(string); !strings.Contains(frag, "--set telemetry.export.otlp.endpoint="+c.id+".continuum-system.svc:4317") {
			t.Fatalf("%s: %s", c.want, frag)
		}
		// Retire the intent so the next operator can be targeted (one active intent per agent).
		a.do("POST", "/api/v1/telemetry-intents/"+iid+"/revoke", map[string]any{}, withCookie(cookie))
	}
	// An external destination has no operator, so no receiverAuth.
	r := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agentID, "name": "ext", "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("%d %s", r.Code, r.Body.String())
	}
	cmd := a.do("POST", "/api/v1/telemetry-intents/"+r.json(t)["id"].(string)+"/command", nil, withCookie(cookie)).json(t)
	if _, has := cmd["receiverAuth"]; has {
		t.Fatalf("receiverAuth on an external destination: %v", cmd)
	}
}

func TestOperatorDocCarriesReceiverAuthForEveryOperator(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	if _, _, _, err := a.core.CreateOperator(a.ctx, "alex", "new", []string{cl}, extDest("c:4317"), nil); err != nil {
		t.Fatal(err)
	}
	legacy := store.Operator{ID: "op-legacy000002", OrgID: "org-1", Name: "legacy", Status: store.OperatorActive,
		SourceClusterIDs: []string{cl}, Destination: extDest("c:4317"), CreatedBy: "alex", CreatedAt: *a.now}
	// ReceiverAuth left empty, as a caller predating the field would: stored and read back as bearer.
	if err := a.st.CreateOperator(a.ctx, legacy, HashSecret("cno_old")); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{}
	for _, o := range a.do("GET", "/api/v1/operators", nil, withCookie(cookie)).jsonArray(t) {
		got[o["name"].(string)] = o["receiverAuth"]
	}
	if got["new"] != "mtls" || got["legacy"] != "bearer" {
		t.Fatalf("receiverAuth by operator = %v", got)
	}
}

// The operator's name and labels reach the chart as one quoted --set-json value: a single quote in either
// must be escaped, never close the shell quoting around the command.
func TestInstallCommandQuotesOperatorNameAndLabels(t *testing.T) {
	a := newAdminRig(t)
	op := store.Operator{ID: "op-q", Name: "O'Brien; rm -rf /", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "c:4317"},
		Labels:      []store.OperatorLabel{{Key: "region", Value: "eu 'south'"}}}
	got, _ := a.a.operatorInstallCommand(ImageConfig{}, "cno_SECRET", op, OperatorTLSBundle{}, "")
	want := `--set-json operator='{"id":"op-q","name":"O'\''Brien; rm -rf /","labels":[{"key":"region","value":"eu '\''south'\''"}]}'`
	if !strings.Contains(got, want) {
		t.Fatalf("install command lacks %s:\n%s", want, got)
	}
}
