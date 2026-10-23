package server

import (
	"strings"
	"testing"

	"ikhnos/internal/accounts"
	"ikhnos/internal/install"
	"ikhnos/internal/operators"
	"ikhnos/internal/testkit"
)

// The page used to build the block that points an agent at a regional operator and merge what this endpoint returned into it by pattern.
// Asked with the telemetry it is editing, the endpoint writes the whole block. These tests hold it to what the page pasted: the
// renderer's own bytes are pinned against the page's in internal/install (operator_block_test.go); what is pinned here is what only the
// server can decide.

func ptr[T any](v T) *T { return &v }

// blockRig is an admin, an operator, and an agent with an intent whose destination is that operator.
type blockRig struct {
	a        *adminRig
	cookie   opt
	opID     string
	intentID string
	agentID  string
}

func newBlockRig(t *testing.T) blockRig {
	t.Helper()
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", accounts.RoleAdmin)
	r := a.do("POST", "/api/v1/operators", map[string]any{
		"name": "athens-regional", "sourceClusterIds": []string{a.approvedCluster(t, testkit.FP)},
		"destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"},
	}, withCookie(cookie))
	if r.Code != 201 {
		t.Fatalf("create operator: %d %s", r.Code, r.Body.String())
	}
	opID, _ := r.json(t)["operator"].(map[string]any)["id"].(string)
	agentID := a.approvedAgentID(t, testkit.FP2)
	return blockRig{a: a, cookie: withCookie(cookie), opID: opID, agentID: agentID}
}

func (b *blockRig) intent(t *testing.T, destination map[string]any, routes map[string]any) {
	t.Helper()
	r := b.a.do("POST", "/api/v1/telemetry-intents", map[string]any{
		"agentId": b.agentID, "name": "patras-edge", "destination": destination,
		"signals": []map[string]any{{"id": "resourceUsage", "source": "builtin"}, {"id": "systemLogs", "source": "builtin"}, {"id": "traces", "source": "builtin"}},
	}, b.cookie)
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	b.intentID, _ = r.json(t)["id"].(string)
	if routes != nil {
		r = b.a.do("POST", "/api/v1/telemetry-intents/"+b.intentID+"/destination", map[string]any{"destination": destination, "routes": routes}, b.cookie)
		if r.Code != 200 {
			t.Fatalf("set routes: %d %s", r.Code, r.Body.String())
		}
	}
}

func (b blockRig) command(t *testing.T, spec *install.Telemetry) (int, map[string]any) {
	t.Helper()
	var body any
	if spec != nil {
		body = map[string]any{"telemetry": spec}
	}
	r := b.a.do("POST", "/api/v1/telemetry-intents/"+b.intentID+"/command", body, b.cookie)
	if r.Code != 200 {
		return r.Code, nil
	}
	return r.Code, r.json(t)
}

func (b blockRig) certificates(t *testing.T) int {
	t.Helper()
	n, _ := (&env{st: b.a.st, ctx: b.a.ctx}).countAudit(t, "operator-client-cert-reissued")
	return n
}

// what the page holds for a single destination at the operator's own address, with whatever an earlier destination left behind.
func singleSpec(over func(*install.Telemetry)) *install.Telemetry {
	t := install.Telemetry{
		Signals:     install.Signals{ResourceUsage: true},
		Destination: install.Destination{Endpoint: "placeholder.ikhnos-system.svc:4317", Protocol: "http", Insecure: true, AuthHeader: "x-api-key", AuthSecret: "old-token", AuthKey: "k"},
		Processing:  install.Processing{TracesSamplingPercent: ptr(100.0)},
	}
	if over != nil {
		over(&t)
	}
	return &t
}

func TestTheServerWritesTheWholeBlockForAnOperatorDestination(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), nil)

	// Asked for the parts alone, the answer is the parts alone, as before.
	_, parts := b.command(t, nil)
	if _, ok := parts["command"]; ok {
		t.Fatalf("a command without being asked for one: %v", parts["command"])
	}

	code, doc := b.command(t, singleSpec(nil))
	if code != 200 {
		t.Fatalf("command: %d", code)
	}
	block, _ := doc["command"].(string)
	fragment, _ := doc["installFragment"].(string)
	secrets, _ := doc["secretCommands"].([]any)
	if len(secrets) != 1 {
		t.Fatalf("one operator, so one certificate Secret: %v", secrets)
	}
	// The certificate Secret first, and the upgrade last with the server's fragment as its end.
	if !strings.HasPrefix(block, "{ "+secrets[0].(string)+"\n} && \\\nhelm upgrade ikhnos-agent ") {
		t.Fatalf("the block does not start with the certificate Secret and then the upgrade:\n%.400s", block)
	}
	if !strings.HasSuffix(block, " \\\n  "+fragment) {
		t.Fatalf("the block does not end with the server's own fragment:\n%s", block)
	}
	// The operator's address is stated once, by the server; what the page held (HTTP, skip-verify, a token) is not in the command.
	endpoint := operators.OperatorServiceName(b.opID) + ".ikhnos-system.svc:4317"
	for _, want := range []string{"--set telemetry.export.otlp.endpoint=" + endpoint, "telemetry.export.otlp.protocol=grpc", "telemetry.export.otlp.tls.insecure=false", "telemetry.export.otlp.tls.mtls.enabled=true"} {
		if !strings.Contains(block, want) {
			t.Errorf("the block lacks %q", want)
		}
	}
	for _, not := range []string{"placeholder.ikhnos-system", "otlp.protocol=http", "tls.insecure=true", "old-token", "x-api-key", "mtls.enabled=false"} {
		if strings.Contains(block, not) {
			t.Errorf("the block has %q", not)
		}
	}
	if n := strings.Count(block, "telemetry.export.otlp.endpoint="); n != 1 {
		t.Errorf("the endpoint is stated %d times", n)
	}
	if doc["endpoint"] != endpoint {
		t.Errorf("endpoint = %v", doc["endpoint"])
	}
	if p, _ := doc["problems"].([]any); len(p) != 0 {
		t.Errorf("problems = %v", p)
	}
	// The chart is the one this server serves, not one the page names.
	if !strings.Contains(block, "--namespace ikhnos-system --reset-then-reuse-values") {
		t.Errorf("the upgrade does not say where it runs:\n%.600s", block)
	}
}

func TestADraftThatCannotBeWrittenIsToldAndNoCertificateIsIssued(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), nil)
	before := b.certificates(t)
	code, doc := b.command(t, singleSpec(func(t *install.Telemetry) { t.Processing.TracesSamplingPercent = nil }))
	if code != 200 {
		t.Fatalf("command: %d", code)
	}
	problems, _ := doc["problems"].([]any)
	if len(problems) == 0 || !strings.Contains(problems[0].(string), "Traces sampling") {
		t.Fatalf("problems = %v", problems)
	}
	if doc["command"] != nil && doc["command"] != "" {
		t.Fatalf("a command for a draft with a problem: %v", doc["command"])
	}
	if secrets, _ := doc["secretCommands"].([]any); len(secrets) != 0 {
		t.Fatalf("Secrets for a draft with a problem: %v", secrets)
	}
	if after := b.certificates(t); after != before {
		t.Fatalf("a certificate was issued (and audited) for a command that cannot be written: %d -> %d", before, after)
	}
}

// What a page holds can be stale; which destinations are operators is the intent's word.
func TestTheIntentNotThePageDecidesWhichDestinationsAreOperators(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), nil)
	_, doc := b.command(t, singleSpec(func(t *install.Telemetry) { t.Destination.Operator = "some-other-operator" }))
	block, _ := doc["command"].(string)
	if !strings.Contains(block, "telemetry.export.otlp.endpoint="+operators.OperatorServiceName(b.opID)) || strings.Contains(block, "some-other-operator") {
		t.Fatalf("the page's operator id was believed:\n%s", block)
	}

	// An intent to an external destination: the operator machinery is not applied to what the page says is an operator.
	e := newBlockRig(t)
	e.intent(t, map[string]any{"kind": "external", "endpoint": "otel.example.com:4317"}, nil)
	before := e.certificates(t)
	_, doc = e.command(t, singleSpec(func(t *install.Telemetry) {
		t.Destination.Operator = e.opID
		t.Destination.Endpoint = "otel.example.com:4317"
	}))
	block, _ = doc["command"].(string)
	if after := e.certificates(t); after != before {
		t.Fatalf("a certificate was issued for an external destination")
	}
	if !strings.Contains(block, "otel.example.com:4317") || !strings.Contains(block, "tls.mtls.enabled=false") || strings.Contains(block, "tls.mtls.secretName="+e.opID) {
		t.Fatalf("an external destination was written as an operator:\n%s", block)
	}
}

func TestSettingsThatDisagreeWithTheIntentAboutRoutesAreTurnedBack(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), nil)
	before := b.certificates(t)
	code, _ := b.command(t, singleSpec(func(t *install.Telemetry) { t.Routes.Split = true }))
	if code != 409 {
		t.Fatalf("split settings against an intent without routes: %d", code)
	}
	if after := b.certificates(t); after != before {
		t.Fatalf("a certificate was issued for settings the intent does not hold")
	}

	r := newBlockRig(t)
	r.intent(t, opDest(r.opID), map[string]any{"metrics": opDest(r.opID), "logs": opDest(r.opID), "traces": map[string]any{"kind": "external", "endpoint": "z:9411/api/v2/spans", "insecure": true}})
	if code, _ := r.command(t, singleSpec(nil)); code != 409 {
		t.Fatalf("single settings against an intent with routes: %d", code)
	}
}

func TestEachSignalTypeGoesWhereTheIntentSays(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), map[string]any{
		"metrics": opDest(b.opID), "logs": opDest(b.opID),
		"traces": map[string]any{"kind": "external", "endpoint": "t.example.com:4317", "authSecretName": "trace-key"},
	})
	spec := &install.Telemetry{
		Signals: install.Signals{ResourceUsage: true, SystemLogs: true, Traces: true},
		Routes: install.Routes{
			Split:   true,
			Metrics: install.Destination{Endpoint: "x:1", Protocol: "http", Insecure: true, AuthSecret: "leftover"},
			Logs:    install.Destination{Endpoint: "x:1", Protocol: "http"},
			Traces:  install.Destination{Endpoint: "t.example.com:4317", Protocol: "grpc", AuthSecret: "trace-key"},
		},
		Processing: install.Processing{TracesSamplingPercent: ptr(100.0)},
	}
	before := b.certificates(t)
	code, doc := b.command(t, spec)
	if code != 200 {
		t.Fatalf("command: %d", code)
	}
	block, _ := doc["command"].(string)
	if after := b.certificates(t); after != before+1 {
		t.Fatalf("two signal types to one operator are one certificate: %d -> %d", before, after)
	}
	svc := operators.OperatorServiceName(b.opID) + ".ikhnos-system.svc:4317"
	for _, want := range []string{"telemetry.export.routes.metrics.endpoint=" + svc, "telemetry.export.routes.logs.endpoint=" + svc, "routes.metrics.tls.mtls.enabled=true", "routes.logs.tls.mtls.enabled=true", "--set telemetry.export.routes.traces.tls.mtls.enabled=false"} {
		if !strings.Contains(block, want) {
			t.Errorf("the block lacks %q", want)
		}
	}
	for _, not := range []string{"x:1", "leftover", "routes.metrics.tls.mtls.enabled=false", "routes.metrics.protocol=http"} {
		if strings.Contains(block, not) {
			t.Errorf("the block has %q", not)
		}
	}
	// The external route keeps its own credential: the Secret that holds it is created before the upgrade runs.
	if !strings.Contains(block, "trace-key") || strings.Index(block, "trace-key") > strings.Index(block, "helm upgrade") {
		t.Errorf("the external route's credential Secret is not created first:\n%.500s", block)
	}
	if n := strings.Count(block, "telemetry.export.routes.metrics.endpoint="); n != 1 {
		t.Errorf("the metrics route is stated %d times", n)
	}
}

func TestTheBlockIsAskedForByAdministratorsOnly(t *testing.T) {
	b := newBlockRig(t)
	b.intent(t, opDest(b.opID), nil)
	_, editor := b.a.user(t, "jamie", accounts.RoleEditor)
	if r := b.a.do("POST", "/api/v1/telemetry-intents/"+b.intentID+"/command", map[string]any{"telemetry": singleSpec(nil)}, withCookie(editor)); r.Code != 403 {
		t.Fatalf("an editor asked for the block: %d", r.Code)
	}
	if r := b.a.do("POST", "/api/v1/telemetry-intents/"+b.intentID+"/command", map[string]any{"telemetry": singleSpec(nil), "extra": 1}, b.cookie); r.Code != 400 {
		t.Fatalf("an unknown field: %d", r.Code)
	}
}
