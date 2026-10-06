package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"continuum/internal/store"
)

// Whatever reaches a command line is validated first: an external destination is a host and a port or a URL, a file
// path, a header name and a Secret name and key, never anything a shell or Helm would read as more than one word.
func TestExternalDestinationsAreValidatedForTheCommandLine(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	good := store.Destination{Kind: store.DestinationExternal, Endpoint: "otlp.example.com:4317", CAFile: "/etc/ssl/ca.pem", AuthHeaderName: "X-Api-Key", AuthSecretName: "export-auth", AuthSecretKey: "token"}
	if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "ok", []string{cl}, good, nil); err != nil {
		t.Fatalf("a well-formed destination: %v", err)
	}
	for _, endpoint := range []string{"https://tempo.example.com/v1/traces", "[2001:db8::1]:4317", "zipkin.tracing.svc:9411/api/v2/spans"} {
		if err := e.core.validateDestination(e.ctx, store.Destination{Kind: store.DestinationExternal, Endpoint: endpoint}); err != nil {
			t.Fatalf("%q was refused: %v", endpoint, err)
		}
	}
	for name, mutate := range map[string]func(*store.Destination){
		"endpoint with a space":         func(d *store.Destination) { d.Endpoint = "a b:4317" },
		"endpoint with a semicolon":     func(d *store.Destination) { d.Endpoint = "a:4317;rm -rf /" },
		"endpoint with a quote":         func(d *store.Destination) { d.Endpoint = "a:4317'" },
		"endpoint with a command":       func(d *store.Destination) { d.Endpoint = "$(id):4317" },
		"endpoint with a comma":         func(d *store.Destination) { d.Endpoint = "a:1,b:2" },
		"endpoint with a newline":       func(d *store.Destination) { d.Endpoint = "a:4317\nb" },
		"endpoint with a leading space": func(d *store.Destination) { d.Endpoint = " a:4317" },
		"caFile with a space":           func(d *store.Destination) { d.CAFile = "/etc/my ca.pem" },
		"caFile with a substitution":    func(d *store.Destination) { d.CAFile = "/etc/`id`" },
		"header with a colon":           func(d *store.Destination) { d.AuthHeaderName = "X: y" },
		"secret name in capitals":       func(d *store.Destination) { d.AuthSecretName = "Export_Auth" },
		"secret name with a quote":      func(d *store.Destination) { d.AuthSecretName = "a'b" },
		"secret key with a slash":       func(d *store.Destination) { d.AuthSecretKey = "a/b" },
	} {
		d := good
		mutate(&d)
		if err := e.core.validateDestination(e.ctx, d); kindOf(err) != KindInvalid {
			t.Errorf("%s: %v", name, err)
		}
		if _, _, _, err := e.core.CreateOperator(e.ctx, "alex", "bad", []string{cl}, d, nil); kindOf(err) != KindInvalid {
			t.Errorf("%s (create): %v", name, err)
		}
	}
}

// Exposure is checked in the core, not only by the one handler that happens to read it.
func TestCreateOperatorChecksExposureInTheCore(t *testing.T) {
	e := newEnv(t)
	cl := e.approvedCluster(t, fp)
	for _, ok := range []string{"", "cluster", "loadbalancer", "nodeport"} {
		if _, _, _, _, err := e.core.CreateOperatorWithOptions(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil, OperatorOptions{Exposure: ok}); err != nil {
			t.Fatalf("exposure %q: %v", ok, err)
		}
	}
	if _, _, _, _, err := e.core.CreateOperatorWithOptions(e.ctx, "alex", "x", []string{cl}, extDest("c:4317"), nil, OperatorOptions{Exposure: "ingress"}); kindOf(err) != KindInvalid {
		t.Fatalf("exposure ingress: %v", err)
	}
}

// What `kubectl get svc` prints cannot be reached from another cluster, however well it is spelled.
func TestAddressRefusesNamesThatOnlyResolveInsideOneCluster(t *testing.T) {
	for _, in := range []string{"op-a.continuum-system.svc", "op-a.continuum-system.svc:4317", "op-a.continuum-system.svc.cluster.local:4317", "x.cluster.local", "OP-A.NS.SVC.", "svc"} {
		if _, err := validOperatorAddress(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	for in, want := range map[string]string{"op-a.continuum-system.svc.clusterset.local": "op-a.continuum-system.svc.clusterset.local:4317", "svc.example.com:4317": "svc.example.com:4317", "mysvc.example.com": "mysvc.example.com:4317"} {
		if got, err := validOperatorAddress(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
}

// addressState is derived: nothing to record for a cluster-only operator, pending for one exposed as a load balancer
// or node port until its address is recorded.
func TestAddressStateIsDerived(t *testing.T) {
	for _, c := range []struct {
		op   store.Operator
		want string
	}{
		{store.Operator{}, "none"},
		{store.Operator{Exposure: "cluster"}, "none"},
		{store.Operator{Exposure: "loadbalancer"}, "pending"},
		{store.Operator{Exposure: "nodeport"}, "pending"},
		{store.Operator{Exposure: "loadbalancer", Address: "a.example.com:4317"}, "set"},
		{store.Operator{Address: "a.example.com:4317"}, "set"},
	} {
		if got := addressStateOf(c.op); got != c.want {
			t.Errorf("%+v: %s, want %s", c.op, got, c.want)
		}
	}
}

// A hand-edited or old row cannot break out of the command: every interpolated value is one shell word.
func TestInstallCommandQuotesEveryInterpolatedValue(t *testing.T) {
	a := newAdminRig(t)
	op := store.Operator{ID: "op-q", Name: "n", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "c:4317; touch /tmp/x", CAFile: "/etc/ca $(id).pem", AuthHeaderName: "A b", AuthSecretName: "s'1", AuthSecretKey: "k"}}
	for _, reissue := range []bool{false, true} {
		got, _ := a.a.operatorInstallCommandWith(ImageConfig{}, "", op, OperatorTLSBundle{ReceiverCertPEM: []byte("x")}, "https://h.example/x'; id", nil, reissue)
		for _, want := range []string{"'export.otlp.endpoint=c:4317; touch /tmp/x'", "'export.otlp.tls.caFile=/etc/ca $(id).pem'", `'export.otlp.auth.secretName=s'\''1'`, "'export.otlp.auth.headerName=A b'", `'heartbeat.url=https://h.example/x'\''; id'`} {
			if !strings.Contains(got, want) {
				t.Fatalf("reissue=%v: command lacks %s:\n%s", reissue, want, got)
			}
		}
	}
}

// Source clusters are told the whole destination block, and the client certificate Secret goes once per namespace:
// two clusters sharing a namespace name do not repeat the private key.
func TestSourceRemindersStateTheWholeBlockAndOneSecretPerNamespace(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	clA, clB := a.approvedCluster(t, fp), a.approvedCluster(t, fp2)
	created := a.createOperatorDoc(t, cookie, extBody("athens", clA, clB))
	id := created["operator"].(map[string]any)["id"].(string)
	reminders := created["reminders"].([]any)
	if len(reminders) != 3 {
		t.Fatalf("want one Secret and two upgrades, got %d: %v", len(reminders), reminders)
	}
	secret := reminders[0].(string)
	if !strings.HasPrefix(secret, "# in cluster "+clA+", "+clB+" (namespace continuum-system)") || strings.Count(secret, "BEGIN EC PRIVATE KEY") != 1 {
		t.Fatalf("secret reminder:\n%s", secret)
	}
	for _, r := range reminders[1:] {
		up := r.(string)
		for _, want := range []string{"--reuse-values", "--set telemetry.export.otlp.endpoint=" + operatorServiceName(id) + ".continuum-system.svc:4317", "--set telemetry.export.otlp.protocol=grpc",
			"--set telemetry.export.otlp.tls.insecure=false", "--set telemetry.export.otlp.tls.caFile= ", "--set telemetry.export.otlp.tls.mtls.enabled=true",
			"--set telemetry.export.otlp.tls.mtls.secretName=" + id + "-export-mtls", "--set telemetry.export.otlp.tls.serverName=" + id + ".continuum-system.svc", "--set telemetry.export.otlp.auth.secretName="} {
			if !strings.Contains(up, want) {
				t.Fatalf("upgrade lacks %q:\n%s", want, up)
			}
		}
		if strings.Contains(up, "PRIVATE KEY") {
			t.Fatal("a private key on a helm command line")
		}
	}
}

// The /command of an intent states the whole block too, and the certificate it issues says in the audit trail which
// intent and cluster it was for.
func TestIntentCommandStatesTheWholeBlockAndAuditsWhatTheCertIsFor(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	opID := a.createOperatorDoc(t, cookie, extBody("athens", cl))["operator"].(map[string]any)["id"].(string)
	agent := a.approvedAgentID(t, fp2)
	iid := a.do("POST", "/api/v1/telemetry-intents", map[string]any{"agentId": agent, "name": "i", "destination": map[string]any{"kind": "operator", "targetOperatorId": opID}}, withCookie(cookie)).json(t)["id"].(string)
	doc := a.do("POST", "/api/v1/telemetry-intents/"+iid+"/command", nil, withCookie(cookie)).json(t)
	frag := doc["installFragment"].(string)
	for _, want := range []string{"telemetry.export.otlp.protocol=grpc", "telemetry.export.otlp.tls.insecure=false", "telemetry.export.otlp.tls.caFile= ", "telemetry.export.otlp.tls.mtls.enabled=true", "telemetry.export.otlp.auth.secretName="} {
		if !strings.Contains(frag, want) {
			t.Fatalf("installFragment lacks %q: %s", want, frag)
		}
	}
	found := false
	for _, line := range (&env{st: a.st, ctx: a.ctx}).auditActions(t) {
		if strings.HasPrefix(line, "operator-client-cert-reissued:") && strings.Contains(line, "intent="+iid) && strings.Contains(line, "cluster=") {
			found = true
		}
	}
	if !found {
		t.Fatal("the certificate's audit entry does not say which intent and cluster it was for")
	}
}

// A scope change that adds no cluster issues no certificate; one that adds a cluster issues exactly one, and the
// audit entry names the cluster.
func TestScopeUpdateIssuesAClientCertOnlyWhenAClusterIsGained(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	clA, clB := a.approvedCluster(t, fp), a.approvedCluster(t, fp2)
	id := a.createOperatorDoc(t, cookie, extBody("athens", clA))["operator"].(map[string]any)["id"].(string)
	e := &env{st: a.st, ctx: a.ctx}
	scope := func(clusters ...string) map[string]any {
		r := a.do("POST", "/api/v1/operators/"+id+"/scope", map[string]any{"sourceClusterIds": clusters, "destination": map[string]any{"kind": "external", "endpoint": "c:4317"}}, withCookie(cookie))
		if r.Code != 200 {
			t.Fatalf("scope: %d %s", r.Code, r.Body.String())
		}
		return r.json(t)
	}
	before, _ := e.countAudit(t, "operator-client-cert-reissued")
	doc := scope(clA)
	if n, _ := e.countAudit(t, "operator-client-cert-reissued"); n != before {
		t.Fatalf("a certificate was issued for a scope that gained no cluster")
	}
	rem := doc["reminders"].([]any)
	if len(rem) != 1 || strings.Contains(rem[0].(string), "kind: Secret") || !strings.Contains(rem[0].(string), "--set telemetry.export.otlp.tls.mtls.enabled=true") {
		t.Fatalf("reminders for an unchanged scope = %v", rem)
	}
	doc = scope(clA, clB)
	n, detail := e.countAudit(t, "operator-client-cert-reissued")
	if n != before+1 || !strings.Contains(detail, "clusters="+clB) {
		t.Fatalf("audit entries %d (%q)", n-before, detail)
	}
	if rem := doc["reminders"].([]any); len(rem) != 3 || !strings.Contains(rem[0].(string), "kind: Secret") {
		t.Fatalf("reminders after gaining a cluster = %v", rem)
	}
}

// The heartbeat address: a configured public URL wins over the host the request came in on, and an address that
// cannot work from another cluster comes with a warning the page can show.
func TestHeartbeatURLPrefersThePublicURLAndWarnsAboutAddressesThatCannotWork(t *testing.T) {
	a := newAdminRig(t)
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = "localhost:8443"
	if got := a.a.heartbeatURL(req); got != "http://localhost:8443"+OperatorHeartbeatPath {
		t.Fatalf("without a public URL: %s", got)
	}
	a.a.PublicURL = "https://ikhnos.example.com/"
	if got := a.a.heartbeatURL(req); got != "https://ikhnos.example.com"+OperatorHeartbeatPath {
		t.Fatalf("with a public URL: %s", got)
	}
	a.a.PublicURL = "https://example.com/ikhnos"
	if got := a.a.heartbeatURL(req); got != "https://example.com/ikhnos"+OperatorHeartbeatPath {
		t.Fatalf("with a path prefix: %s", got)
	}
	a.a.PublicURL = "not a url"
	if got := a.a.heartbeatURL(req); got != "http://localhost:8443"+OperatorHeartbeatPath {
		t.Fatalf("an unusable public URL falls back to the request: %s", got)
	}
	for url, wantWarning := range map[string]string{
		"https://ikhnos.example.com" + OperatorHeartbeatPath:          "",
		"https://203.0.113.9" + OperatorHeartbeatPath:                 "",
		"https://localhost:8443" + OperatorHeartbeatPath:              "this machine",
		"https://127.0.0.1" + OperatorHeartbeatPath:                   "this machine",
		"https://ikhnos.continuum-system.svc" + OperatorHeartbeatPath: "only exists inside one Kubernetes cluster",
		"https://x.ns.svc.cluster.local:8443" + OperatorHeartbeatPath: "only exists inside one Kubernetes cluster",
		"https://10.1.2.3" + OperatorHeartbeatPath:                    "a private address",
		"https://192.168.1.5" + OperatorHeartbeatPath:                 "a private address",
		"https://172.16.0.9" + OperatorHeartbeatPath:                  "a private address",
	} {
		got := heartbeatURLWarning(url)
		if (wantWarning == "") != (got == "") || !strings.Contains(got, wantWarning) {
			t.Errorf("%s: warning %q, want one mentioning %q", url, got, wantWarning)
		}
	}
	if got := heartbeatURLWarning("http://localhost:8443" + OperatorHeartbeatPath); !strings.Contains(got, "plain HTTP") || !strings.Contains(got, "this machine") {
		t.Errorf("both problems at once: %q", got)
	}
}

// When the server's certificate is signed by a private CA, the install says which Secret holds it, and gives the command to create it.
func TestHeartbeatCASecretIsEmittedOnlyForAPrivateServerCertificate(t *testing.T) {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	cl := a.approvedCluster(t, fp)
	body := extBody("athens", cl)
	body["heartbeat"] = true
	doc := a.createOperatorDoc(t, cookie, body)
	if _, has := doc["heartbeatCaSecretCommand"]; has || strings.Contains(doc["install"].(string), "heartbeat.tls.caSecretName") {
		t.Fatal("a CA Secret for a server whose certificate is public")
	}
	a.a.HeartbeatCAPEM = a.core.CA.CertPEM()
	doc = a.createOperatorDoc(t, cookie, body)
	id := doc["operator"].(map[string]any)["id"].(string)
	if !strings.Contains(doc["install"].(string), "--set heartbeat.tls.caSecretName="+id+"-heartbeat-ca") {
		t.Fatalf("install lacks the CA Secret name:\n%s", doc["install"])
	}
	cmd, _ := doc["heartbeatCaSecretCommand"].(string)
	if !strings.Contains(cmd, "name: "+id+"-heartbeat-ca") || !strings.Contains(cmd, "BEGIN CERTIFICATE") || !strings.HasPrefix(cmd, "kubectl create namespace") {
		t.Fatalf("CA Secret command:\n%s", cmd)
	}
}
