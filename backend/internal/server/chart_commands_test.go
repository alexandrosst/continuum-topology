package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/store"

	"k8s.io/apimachinery/pkg/util/yaml"
	sigyaml "sigs.k8s.io/yaml"
)

// This file closes the gap between what the server PRINTS (the --set flags of an install, an upgrade, a telemetry
// command) and what the charts DO with them. Every command here is parsed the way a shell would read it, handed to
// `helm template` against the real packaged chart - so the chart's values.schema.json judges every key - and the
// rendered OpenTelemetry Collector configuration is then compared with what the intent, the operator or FUSION said:
// the exporter's endpoint, TLS and client certificate are the declared ones, and nothing an earlier destination set
// (--reset-then-reuse-values keeps it) is left half pointing at the old one.

// shellWords splits a printed command into the words a POSIX shell would hand to the program: single and double quotes,
// backslash-newline continuations, and a comment after whitespace.
func shellWords(s string) ([]string, error) {
	var words []string
	var cur strings.Builder
	have := false
	flush := func() {
		if have {
			words = append(words, cur.String())
			cur.Reset()
			have = false
		}
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case c == '\\' && i+1 < len(rs) && rs[i+1] == '\n':
			i++ // a continuation line
		case c == '\\' && i+1 < len(rs):
			i++
			cur.WriteRune(rs[i])
			have = true
		case c == '\'':
			j := i + 1
			for j < len(rs) && rs[j] != '\'' {
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			cur.WriteString(string(rs[i+1 : j]))
			have = true
			i = j
		case c == '"':
			j := i + 1
			for j < len(rs) && rs[j] != '"' {
				if rs[j] == '\\' && j+1 < len(rs) {
					j++
				}
				cur.WriteRune(rs[j])
				j++
			}
			if j >= len(rs) {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
			have = true
			i = j
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '#' && !have:
			for i < len(rs) && rs[i] != '\n' {
				i++
			}
		default:
			cur.WriteRune(c)
			have = true
		}
	}
	flush()
	return words, nil
}

// helmValueFlags is what of a printed helm command `helm template` can be given: the value flags, in order. Anything
// else that is a flag must be one a render does not need; an unknown flag fails the test, so a new one is a decision.
func helmValueFlags(t *testing.T, cmd string) []string {
	t.Helper()
	words, err := shellWords(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := 0; i < len(words); i++ {
		switch w := words[i]; {
		case w == "--set" || w == "--set-string" || w == "--set-json" || w == "--set-file":
			if i+1 >= len(words) {
				t.Fatalf("%s without a value in %q", w, cmd)
			}
			out = append(out, w, words[i+1])
			i++
		case w == "--namespace" || w == "--version":
			i++
		case w == "--install" || w == "--create-namespace" || w == "--reset-then-reuse-values":
		case strings.HasPrefix(w, "--"):
			t.Fatalf("unknown helm flag %s in %q", w, cmd)
		}
	}
	return out
}

// renderedChart is `helm template`'s documents by "Kind/name".
type renderedChart map[string]map[string]any

func helmRenderChart(t *testing.T, c *chart.Chart, flags ...string) (renderedChart, string, error) {
	t.Helper()
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkg, err := c.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), c.Filename())
	if err := os.WriteFile(tgz, pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(h, append([]string{"template", "ct", tgz}, flags...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return nil, errb.String(), err
	}
	docs := renderedChart{}
	dec := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(out.Bytes()), 4096)
	for {
		var d map[string]any
		if err := dec.Decode(&d); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if d == nil {
			continue
		}
		meta, _ := d["metadata"].(map[string]any)
		docs[fmt.Sprintf("%v/%v", d["kind"], meta["name"])] = d
	}
	return docs, "", nil
}

// collectors are every rendered OpenTelemetry Collector configuration, by ConfigMap name.
func (r renderedChart) collectors(t *testing.T) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for key, d := range r {
		if !strings.HasPrefix(key, "ConfigMap/") {
			continue
		}
		data, _ := d["data"].(map[string]any)
		raw, ok := data["otel-collector-config.yaml"].(string)
		if !ok {
			continue
		}
		var cfg map[string]any
		if err := sigyaml.Unmarshal([]byte(raw), &cfg); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		out[strings.TrimPrefix(key, "ConfigMap/")] = cfg
	}
	return out
}

// podVolumes maps a volume name to the Secret behind it for every workload the chart rendered.
func (r renderedChart) secretVolumes() map[string]string {
	out := map[string]string{}
	for key, d := range r {
		if !strings.HasPrefix(key, "Deployment/") && !strings.HasPrefix(key, "DaemonSet/") {
			continue
		}
		b, _ := json.Marshal(d)
		var w struct {
			Spec struct {
				Template struct {
					Spec struct {
						Volumes []struct {
							Name   string `json:"name"`
							Secret *struct {
								SecretName string `json:"secretName"`
							} `json:"secret"`
						} `json:"volumes"`
					} `json:"spec"`
				} `json:"template"`
			} `json:"spec"`
		}
		_ = json.Unmarshal(b, &w)
		for _, v := range w.Spec.Template.Spec.Volumes {
			if v.Secret != nil {
				out[v.Name] = v.Secret.SecretName
			}
		}
	}
	return out
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// exporterOf is the exporter definition a collector's pipeline of one signal sends to (the first one that is not the
// debug exporter), and its name.
func exporterOf(t *testing.T, cfg map[string]any, signal string) (name string, def map[string]any, ok bool) {
	t.Helper()
	pl := asMap(asMap(asMap(cfg["service"])["pipelines"])[signal])
	if pl == nil {
		return "", nil, false
	}
	exps, _ := pl["exporters"].([]any)
	for _, e := range exps {
		n, _ := e.(string)
		if n == "debug" {
			continue
		}
		return n, asMap(asMap(cfg["exporters"])[n]), true
	}
	return "", nil, false
}

// wantExport is what one signal's exporter must say.
type wantExport struct {
	endpoint   string // as the collector is given it (a gRPC endpoint is host:port)
	serverName string // "" = none
	mtlsSecret string // "" = no client certificate
	insecure   bool
	name       string // the exporter's name in the pipeline: otlp, or otlp/<signal> for a route
}

// checkExporter compares a rendered exporter with what was declared. volumes maps the rendered Secret volumes.
func checkExporter(t *testing.T, where string, def map[string]any, name string, want wantExport, volumes map[string]string) {
	t.Helper()
	if name != want.name {
		t.Errorf("%s: exporter %q, want %q", where, name, want.name)
	}
	if got := def["endpoint"]; got != want.endpoint {
		t.Errorf("%s: endpoint %v, want %q", where, got, want.endpoint)
	}
	tls := asMap(def["tls"])
	if got, _ := tls["insecure"].(bool); got != want.insecure {
		t.Errorf("%s: tls.insecure %v, want %v", where, tls["insecure"], want.insecure)
	}
	if got, _ := tls["server_name_override"].(string); got != want.serverName {
		t.Errorf("%s: tls.server_name_override %q, want %q", where, got, want.serverName)
	}
	cert, _ := tls["cert_file"].(string)
	if want.mtlsSecret == "" {
		if cert != "" {
			t.Errorf("%s: presents a client certificate (%s) the declaration does not name", where, cert)
		}
		return
	}
	// /export-mtls[-route]/tls.crt: the volume mounted there must hold the Secret the declaration names.
	dir := strings.Trim(filepath.Dir(cert), "/")
	if got := volumes[dir]; got != want.mtlsSecret {
		t.Errorf("%s: client certificate %q comes from Secret %q, want %q", where, cert, got, want.mtlsSecret)
	}
	if ca, _ := tls["ca_file"].(string); ca != "/"+dir+"/ca.crt" {
		t.Errorf("%s: the destination is verified against %q, want the CA in the same Secret", where, ca)
	}
	if _, has := def["headers"]; has {
		t.Errorf("%s: carries a credential header although the destination's only gate is the client certificate: %v", where, def["headers"])
	}
}

// ---------------------------------------------------------------------------------------------------------------

// agentBase is what a telemetry command is applied on top of: the install itself, every signal on, and the
// placeholder endpoint the browser writes before the server's fragment (see the front end's operatorCommandBlock).
var agentBase = []string{
	"--set", "server.address=a.example:8443", "--set", "server.caPin=ab", "--set", "enrollment.token=t",
	"--set", "telemetry.resourceUsage.metrics.enabled=true", "--set", "telemetry.kubernetesState.metrics.enabled=true",
	"--set", "telemetry.systemLogs.logs.enabled=true", "--set", "telemetry.kubernetesEvents.logs.enabled=true",
	"--set", "telemetry.traces.traces.enabled=true",
}

// staleDestination is what an earlier, different destination left in the release: every value the next command has to
// overwrite or clear for the collectors to stop pointing at it (--reset-then-reuse-values keeps them).
var staleDefault = []string{
	"--set", "telemetry.export.otlp.endpoint=old.example:4318", "--set", "telemetry.export.otlp.protocol=http",
	"--set", "telemetry.export.otlp.tls.insecure=true", "--set", "telemetry.export.otlp.tls.caFile=/old/ca.pem",
	"--set", "telemetry.export.otlp.tls.caSecretName=old-ca", "--set", "telemetry.export.otlp.tls.serverName=old.example",
	"--set", "telemetry.export.otlp.auth.secretName=old-token", "--set", "telemetry.export.otlp.auth.headerName=X-Old",
}

// staleRoutes is the same for a release that sent each signal type to a destination of its own before.
var staleRoutes = []string{
	"--set", "telemetry.export.routes.metrics.endpoint=old-m.example:4318", "--set", "telemetry.export.routes.metrics.protocol=http",
	"--set", "telemetry.export.routes.metrics.tls.insecure=true", "--set", "telemetry.export.routes.metrics.auth.secretName=old-token",
	"--set", "telemetry.export.routes.logs.endpoint=old-l.example:4318", "--set", "telemetry.export.routes.logs.protocol=http",
	"--set", "telemetry.export.routes.logs.tls.caSecretName=old-ca",
	"--set", "telemetry.export.routes.traces.endpoint=old-t.example:4318", "--set", "telemetry.export.routes.traces.tls.insecure=true",
}

// What an earlier release can have left: nothing, a different default destination, or that and per-signal routes.
const (
	staleNothing = iota
	staleDefaultOnly
	staleDefaultAndRoutes
)

// agentRender renders the agent chart with the base, optionally the stale destination, and the printed flags last.
func agentRender(t *testing.T, stale int, printed ...string) renderedChart {
	t.Helper()
	flags := append([]string{}, agentBase...)
	if stale >= staleDefaultOnly {
		flags = append(flags, staleDefault...)
	}
	if stale >= staleDefaultAndRoutes {
		flags = append(flags, staleRoutes...)
	}
	for _, p := range printed {
		flags = append(flags, helmValueFlags(t, p)...)
	}
	r, stderr, err := helmRenderChart(t, chart.Agent, flags...)
	if err != nil {
		t.Fatalf("the chart refuses what the server printed: %v\n%s\nflags: %v", err, stderr, printed)
	}
	return r
}

// everyPipelineExports asserts, for every rendered collector and every signal pipeline it has, the declared exporter.
func everyPipelineExports(t *testing.T, r renderedChart, want map[string]wantExport) {
	t.Helper()
	vols := r.secretVolumes()
	seen := 0
	for cm, cfg := range r.collectors(t) {
		for _, signal := range []string{"metrics", "logs", "traces"} {
			name, def, ok := exporterOf(t, cfg, signal)
			if !ok {
				continue
			}
			seen++
			checkExporter(t, cm+" "+signal, def, name, want[signal], vols)
		}
	}
	if seen < 3 {
		t.Fatalf("only %d signal pipelines were rendered; the base enables all three signal types", seen)
	}
}

// assertAgentExports renders the printed flags over a release with nothing, with another default destination, and with
// that and per-signal routes already set, and expects the declared exporters every time.
func assertAgentExports(t *testing.T, want map[string]wantExport, printed ...string) {
	t.Helper()
	for level, name := range map[int]string{staleNothing: "fresh install", staleDefaultOnly: "after another default destination", staleDefaultAndRoutes: "after a destination per signal type"} {
		t.Run(name, func(t *testing.T) { everyPipelineExports(t, agentRender(t, level, printed...), want) })
	}
}

// intentRig creates one approved agent per case (one active intent per agent) and answers its command.
type intentRig struct {
	*adminRig
	cookie string
	n      int
}

func (a *intentRig) agent(t *testing.T) (agentID string) {
	a.n++
	return a.approvedAgentID(t, fmt.Sprintf("8f3c2a9e-%04d-4222-8333-944455556666", 100+a.n))
}

func (a *intentRig) command(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	r := a.do("POST", "/api/v1/telemetry-intents", body, withCookie(a.cookie))
	if r.Code != 201 {
		t.Fatalf("create intent: %d %s", r.Code, r.Body.String())
	}
	id := r.json(t)["id"].(string)
	c := a.do("POST", "/api/v1/telemetry-intents/"+id+"/command", nil, withCookie(a.cookie))
	if c.Code != 200 {
		t.Fatalf("command: %d %s", c.Code, c.Body.String())
	}
	return c.json(t)
}

func (a *intentRig) operator(t *testing.T, name string) store.Operator {
	t.Helper()
	cl := a.approvedCluster(t, fmt.Sprintf("8f3c2a9e-%04d-4222-8333-944455556666", 900+a.n))
	a.n++
	id := a.createOperatorDoc(t, a.cookie, extBody(name, cl))["operator"].(map[string]any)["id"].(string)
	op, err := a.st.GetOperator(a.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func newIntentRig(t *testing.T) *intentRig {
	a := newAdminRig(t)
	_, cookie := a.user(t, "alex", RoleAdmin)
	return &intentRig{adminRig: a, cookie: cookie}
}

func opDest(id string) map[string]any {
	return map[string]any{"kind": "operator", "targetOperatorId": id}
}

// What the server prints for an intent that sends to a regional operator - one destination for every signal, or one
// per signal type - is accepted by the chart, and the collectors export where the intent says, verifying the operator
// by its stable name and presenting the Secret the command creates, whatever an earlier destination left behind.
func TestPrintedAgentCommandsRenderTheDeclaredExporters(t *testing.T) {
	a := newIntentRig(t)
	opA, opB := a.operator(t, "athens"), a.operator(t, "patras")
	mtls := func(op store.Operator, endpoint, name string) wantExport {
		return wantExport{endpoint: endpoint, serverName: operatorServerName(op), mtlsSecret: operatorClientTLSSecretName(op), name: name}
	}
	inCluster := func(op store.Operator) string { return operatorInClusterEndpoint(op) }

	t.Run("one regional operator for everything", func(t *testing.T) {
		doc := a.command(t, map[string]any{"agentId": a.agent(t), "name": "one", "destination": opDest(opA.ID)})
		want := map[string]wantExport{}
		for _, s := range []string{"metrics", "logs", "traces"} {
			want[s] = mtls(opA, inCluster(opA), "otlp")
		}
		assertAgentExports(t, want, "--set telemetry.export.otlp.endpoint=placeholder:4317", doc["installFragment"].(string))
	})

	t.Run("a recorded address is what is dialled, the name on the certificate is still the stable one", func(t *testing.T) {
		if r := a.do("POST", "/api/v1/operators/"+opB.ID+"/address", map[string]any{"address": "203.0.113.9:4317"}, withCookie(a.cookie)); r.Code != 200 {
			t.Fatalf("address: %d %s", r.Code, r.Body.String())
		}
		doc := a.command(t, map[string]any{"agentId": a.agent(t), "name": "addr", "destination": opDest(opB.ID)})
		want := map[string]wantExport{}
		for _, s := range []string{"metrics", "logs", "traces"} {
			want[s] = mtls(opB, "203.0.113.9:4317", "otlp")
		}
		assertAgentExports(t, want, "--set telemetry.export.otlp.endpoint=placeholder:4317", doc["installFragment"].(string))
	})

	t.Run("each signal type to its own operator", func(t *testing.T) {
		doc := a.command(t, map[string]any{
			"agentId": a.agent(t), "name": "routes", "destination": opDest(opA.ID),
			"routes": map[string]any{"metrics": opDest(opA.ID), "logs": opDest(opB.ID), "traces": opDest(opA.ID)},
		})
		want := map[string]wantExport{
			"metrics": mtls(opA, inCluster(opA), "otlp/metrics"),
			"logs":    mtls(opB, "203.0.113.9:4317", "otlp/logs"),
			"traces":  mtls(opA, inCluster(opA), "otlp/traces"),
		}
		placeholders := "--set telemetry.export.otlp.endpoint=placeholder:4317 --set telemetry.export.routes.metrics.endpoint=p:1 --set telemetry.export.routes.logs.endpoint=p:1 --set telemetry.export.routes.traces.endpoint=p:1"
		assertAgentExports(t, want, placeholders, doc["installFragment"].(string))
		// One certificate and one Secret per operator, however many signal types go to it.
		if n := len(doc["secretCommands"].([]any)); n != 2 {
			t.Errorf("%d Secret commands for two operators: %v", n, doc["secretCommands"])
		}
	})

	t.Run("one signal type routed elsewhere, the rest to the default operator", func(t *testing.T) {
		doc := a.command(t, map[string]any{
			"agentId": a.agent(t), "name": "partial", "destination": opDest(opA.ID),
			"routes": map[string]any{"logs": map[string]any{"kind": "external", "endpoint": "logs.example:4317"}},
		})
		want := map[string]wantExport{
			"metrics": mtls(opA, inCluster(opA), "otlp"),
			"logs":    {endpoint: "logs.example:4317", name: "otlp/logs"},
			"traces":  mtls(opA, inCluster(opA), "otlp"),
		}
		// The browser states the external route itself; the default operator and the cleared routes are the server's.
		assertAgentExports(t, want, "--set telemetry.export.otlp.endpoint=placeholder:4317 --set telemetry.export.routes.logs.endpoint=logs.example:4317 --set telemetry.export.routes.logs.protocol=grpc --set telemetry.export.routes.logs.tls.insecure=false --set telemetry.export.routes.logs.tls.caSecretName= --set telemetry.export.routes.logs.auth.secretName=", doc["installFragment"].(string))
	})

	t.Run("the provenance flags reach the collectors", func(t *testing.T) {
		doc := a.command(t, map[string]any{"agentId": a.agent(t), "name": "prov", "destination": opDest(opA.ID)})
		r := agentRender(t, staleNothing, "--set telemetry.export.otlp.endpoint=placeholder:4317", doc["installFragment"].(string))
		var all string
		for _, cfg := range r.collectors(t) {
			b, _ := json.Marshal(cfg)
			all += string(b)
		}
		for _, want := range []string{`"continuum.org.id"`, `"continuum.cluster.id"`, `"continuum.intent.id"`} {
			if !strings.Contains(all, want) {
				t.Errorf("the rendered collectors never stamp %s", want)
			}
		}
	})
}

// A destination that is only an external endpoint gets from the server nothing beyond provenance; those flags must
// still be accepted by the chart.
func TestPrintedExternalIntentCommandIsAcceptedByTheChart(t *testing.T) {
	a := newIntentRig(t)
	doc := a.command(t, map[string]any{"agentId": a.agent(t), "name": "ext", "destination": map[string]any{"kind": "external", "endpoint": "collector.example:4317"}})
	r := agentRender(t, staleNothing, "--set telemetry.export.otlp.endpoint=collector.example:4317", doc["installFragment"].(string))
	want := map[string]wantExport{}
	for _, s := range []string{"metrics", "logs", "traces"} {
		want[s] = wantExport{endpoint: "collector.example:4317", name: "otlp"}
	}
	everyPipelineExports(t, r, want)
}

// The central operator in front of FUSION: reached by its in-cluster Service while FUSION has no public address (and
// then verified by the name it is dialled by), and at the public address - verified by its stable name - once it has one.
func TestPrintedAgentCommandsForTheCentralOperator(t *testing.T) {
	a := newIntentRig(t)
	f, _ := newFusion(t, a.adminRig)
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	central, err := a.st.GetOperator(a.ctx, CentralOperatorID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, address string
		want          wantExport
	}{
		{"in-cluster", "", wantExport{endpoint: f.centralHost() + ":4317", mtlsSecret: operatorClientTLSSecretName(central), name: "otlp"}},
		{"public", "203.0.113.7:30317", wantExport{endpoint: "203.0.113.7:30317", serverName: operatorServerName(central), mtlsSecret: operatorClientTLSSecretName(central), name: "otlp"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f.SetPublicAddress(c.address)
			doc := a.command(t, map[string]any{"agentId": a.agent(t), "name": c.name, "destination": opDest(CentralOperatorID)})
			want := map[string]wantExport{"metrics": c.want, "logs": c.want, "traces": c.want}
			assertAgentExports(t, want, "--set telemetry.export.otlp.endpoint=placeholder:4317", doc["installFragment"].(string))
		})
	}
}

// ---------------------------------------------------------------------------------------------------------------
// The regional operator's own commands.

func operatorRender(t *testing.T, printed ...string) renderedChart {
	t.Helper()
	var flags []string
	for _, p := range printed {
		flags = append(flags, helmValueFlags(t, p)...)
	}
	r, stderr, err := helmRenderChart(t, chart.RegionalOperator, flags...)
	if err != nil {
		t.Fatalf("the operator chart refuses what the server printed: %v\n%s\nflags: %v", err, stderr, printed)
	}
	return r
}

func onlyCollector(t *testing.T, r renderedChart) map[string]any {
	t.Helper()
	cs := r.collectors(t)
	if len(cs) != 1 {
		t.Fatalf("%d collector configurations rendered, want 1", len(cs))
	}
	for _, c := range cs {
		return c
	}
	return nil
}

// Every way of creating or installing a regional operator again prints flags the operator chart accepts, and the
// collector then receives the way the operator says (mutual TLS only, or a bearer token) and exports where it says.
func TestPrintedOperatorInstallsRenderTheDeclaredReceiverAndExporter(t *testing.T) {
	a := newIntentRig(t)
	a.a.PublicURL = "https://continuum.example.com" // the chart refuses a plain-HTTP heartbeat, and a test server's own address is plain HTTP
	f, _ := newFusion(t, a.adminRig)
	a.a.Fusion = f
	if _, err := f.Enable(a.ctx, a.core, "alex"); err != nil {
		t.Fatal(err)
	}
	central, _ := a.st.GetOperator(a.ctx, CentralOperatorID)
	cl := a.approvedCluster(t, "8f3c2a9e-0777-4222-8333-944455556666")

	creates := map[string]map[string]any{
		"external": {"name": "ext", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "backend.example:4317"}},
		"external insecure with CA file and a credential": {"name": "ext2", "sourceClusterIds": []string{cl}, "destination": map[string]any{
			"kind": "external", "endpoint": "backend.example:4317", "insecure": true, "caFile": "/etc/ssl/certs/ca.pem",
			"authHeaderName": "Authorization", "authSecretName": "backend-token", "authSecretKey": "token"}},
		"to the central operator": {"name": "toc", "sourceClusterIds": []string{cl}, "destination": opDest(CentralOperatorID), "heartbeat": true},
		"with an exposure":        {"name": "exp", "sourceClusterIds": []string{cl}, "destination": map[string]any{"kind": "external", "endpoint": "backend.example:4317"}, "exposure": "nodeport", "heartbeat": true},
	}
	for name, body := range creates {
		for _, pub := range []struct{ label, address string }{{"central in-cluster", ""}, {"central public", "203.0.113.7:30317"}} {
			t.Run(name+"/"+pub.label, func(t *testing.T) {
				f.SetPublicAddress(pub.address)
				doc := a.createOperatorDoc(t, a.cookie, body)
				op, err := a.st.GetOperator(a.ctx, doc["operator"].(map[string]any)["id"].(string))
				if err != nil {
					t.Fatal(err)
				}
				installs := []string{doc["install"].(string)}
				// Installing again states the same destination, in full.
				again := a.do("POST", "/api/v1/operators/"+op.ID+"/install", nil, withCookie(a.cookie))
				if again.Code != 200 {
					t.Fatalf("install again: %d %s", again.Code, again.Body.String())
				}
				installs = append(installs, again.json(t)["install"].(string))
				for _, install := range installs {
					r := operatorRender(t, install)
					cfg := onlyCollector(t, r)
					recv := asMap(asMap(asMap(asMap(cfg["receivers"])["otlp"])["protocols"])["grpc"])
					tls := asMap(recv["tls"])
					if tls == nil || tls["client_ca_file"] == nil || recv["auth"] != nil {
						t.Errorf("an mTLS-only operator must receive over TLS with a required client certificate and no token: %v", recv)
					}
					want := wantExport{endpoint: "backend.example:4317", name: "otlp"}
					if d := body["destination"].(map[string]any); d["kind"] == "operator" {
						want = wantExport{endpoint: f.CentralEndpoint(), mtlsSecret: operatorClientTLSSecretName(central), name: "otlp"}
						if pub.address != "" {
							want.serverName = operatorServerName(central)
						}
					} else if d["insecure"] == true {
						want.insecure = true
					}
					vols := r.secretVolumes()
					for _, signal := range []string{"metrics", "logs", "traces"} {
						n, def, ok := exporterOf(t, cfg, signal)
						if !ok {
							t.Fatalf("no %s pipeline", signal)
						}
						checkExporter(t, "operator "+signal, def, n, want, vols)
						if d := body["destination"].(map[string]any); d["kind"] == "external" && d["authSecretName"] != nil {
							if asMap(def["headers"]) == nil {
								t.Errorf("the credential the destination names is not sent: %v", def)
							}
						}
					}
					if body["heartbeat"] == true {
						exp := asMap(asMap(cfg["exporters"])["otlphttp/heartbeat"])
						if exp == nil || !strings.HasSuffix(fmt.Sprint(exp["metrics_endpoint"]), OperatorHeartbeatPath) {
							t.Errorf("the heartbeat does not report to this server: %v", exp)
						}
					}
				}
			})
		}
	}
}

// The reminders a regional operator prints for each of its source clusters (the agent chart, upgraded) point that
// cluster's agent at the operator exactly as an intent's command does.
func TestPrintedSourceRemindersRenderTheOperatorAsDestination(t *testing.T) {
	a := newIntentRig(t)
	clA, clB := a.approvedCluster(t, "8f3c2a9e-0801-4222-8333-944455556666"), a.approvedCluster(t, "8f3c2a9e-0802-4222-8333-944455556666")
	doc := a.createOperatorDoc(t, a.cookie, extBody("athens", clA, clB))
	op, _ := a.st.GetOperator(a.ctx, doc["operator"].(map[string]any)["id"].(string))
	n := 0
	for _, r := range doc["reminders"].([]any) {
		s := r.(string)
		if !strings.HasPrefix(s, "helm upgrade") {
			continue
		}
		n++
		want := wantExport{endpoint: operatorInClusterEndpoint(op), serverName: operatorServerName(op), mtlsSecret: operatorClientTLSSecretName(op), name: "otlp"}
		everyPipelineExports(t, agentRender(t, staleDefaultAndRoutes, s), map[string]wantExport{"metrics": want, "logs": want, "traces": want})
	}
	if n != 2 {
		t.Fatalf("%d upgrade reminders for two clusters", n)
	}
}
