package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"ikhnos/internal/agents"
	"ikhnos/internal/chart"
	"ikhnos/internal/estate"
	"ikhnos/internal/install"
	"ikhnos/internal/kernel"
	"ikhnos/internal/operators"
	"ikhnos/internal/operatorsapi"
	"ikhnos/internal/store"
	"ikhnos/internal/testkit"
)

// The commands this server prints for a person to paste into a shell are pinned here, as text, in testdata/install_commands.golden.json.
// The commands the web UI builds in TypeScript are pinned in the same way in tests/install_commands.golden.json; the chart package
// reads both files and renders the chart with every `helm` command in them (internal/chart/install_commands_test.go), so a command that
// names a value the chart does not have fails there and not on a person's cluster.
//
// Each case is a name, the chart its helm command is for ("agent" or "regional-operator"; empty for a command that is not helm: a
// Secret, a kubectl line) and the command. The certificate authority's pin differs on every run, so it is replaced by a fixed one of the
// same shape, and every certificate and key is a fixed string.
//
// To accept a change on purpose:  go test ./internal/server -run TestServerInstallCommandsArePinned -update
const serverInstallCommandsGolden = "testdata/install_commands.golden.json"

type pinnedServerCommand struct {
	Name    string `json:"name"`
	Chart   string `json:"chart"`
	Command string `json:"command"`
}

const fixedCAPin = "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="

// pinRig builds the commands: an Admin with the settings a case varies, and the list that collects what it prints.
type pinRig struct {
	t     *testing.T
	cases []pinnedServerCommand
	names map[string]bool
}

func (p *pinRig) admin(tweak func(*Admin)) *Admin {
	a := testAdmin(p.t)
	if tweak != nil {
		tweak(a)
	}
	return a
}

// flags writes values as the fragment of a command the server hands out.
func (p *pinRig) flags(values []install.Value) string { return install.Flags(values) }

func (p *pinRig) reminders(a *Admin, img kernel.ImageConfig, op store.Operator, b operators.OperatorTLSBundle, targets []operatorsapi.SourceTarget) []string {
	p.t.Helper()
	out, err := a.operatorsAPI().Reminders(img, op, b, targets)
	if err != nil {
		p.t.Fatal(err)
	}
	return out
}

// ok is the command, or the end of the test when it could not be written.
func (p *pinRig) ok(command string, err error) string {
	p.t.Helper()
	if err != nil {
		p.t.Fatal(err)
	}
	return command
}

func (p *pinRig) add(a *Admin, name, chart, command string) {
	p.t.Helper()
	if p.names[name] {
		p.t.Fatalf("two cases are called %s", name)
	}
	p.names[name] = true
	if command == "" {
		p.t.Fatalf("%s: prints nothing, which would pin nothing", name)
	}
	if a != nil {
		command = strings.ReplaceAll(command, a.C.CA.SPKIPin(), fixedCAPin)
	}
	command = fixedVersions().Replace(command)
	p.cases = append(p.cases, pinnedServerCommand{Name: name, Chart: chart, Command: command})
}

// fixedVersions replaces the version of each chart and the version of the agent image, which change with every release, by fixed ones,
// so that raising a version does not rewrite the file. The longer one goes first: the app version contains the chart version.
func fixedVersions() *strings.Replacer {
	return strings.NewReplacer(
		chart.Agent.AppVersion(), "0.0.0-app",
		chart.Agent.Version(), "0.0.0-agent",
		chart.RegionalOperator.Version(), "0.0.0-operator",
	)
}

func fakePEM(kind, body string) []byte {
	return []byte("-----BEGIN " + kind + "-----\n" + body + "\n-----END " + kind + "-----\n")
}

var pinBundle = operators.OperatorTLSBundle{
	ReceiverCertPEM: fakePEM("CERTIFICATE", "cmVjZWl2ZXItY2VydA=="),
	ReceiverKeyPEM:  fakePEM("EC PRIVATE KEY", "cmVjZWl2ZXIta2V5"),
	CACertPEM:       fakePEM("CERTIFICATE", "b3BlcmF0b3ItY2E="),
	Senders: []operators.SenderCert{
		{Sender: "cl-a", CertPEM: fakePEM("CERTIFICATE", "c2VuZGVyLWEtY2VydA=="), KeyPEM: fakePEM("EC PRIVATE KEY", "c2VuZGVyLWEta2V5")},
		{Sender: "cl-b", CertPEM: fakePEM("CERTIFICATE", "c2VuZGVyLWItY2VydA=="), KeyPEM: fakePEM("EC PRIVATE KEY", "c2VuZGVyLWIta2V5")},
	},
}

func (p *pinRig) agentCommands() {
	images := []struct {
		name string
		img  kernel.ImageConfig
	}{
		{"no-registry", kernel.ImageConfig{}},
		{"registry", kernel.ImageConfig{Registry: "registry.example.com/team"}},
		{"registry-tag", kernel.ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0"}},
		{"registry-digest", kernel.ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0", Digest: goodDigest}},
		{"hub-namespace", kernel.ImageConfig{Registry: "myteam"}},
	}
	charts := []struct {
		name  string
		tweak func(*Admin)
	}{
		{"default", nil},
		{"local", func(a *Admin) { a.ChartRef = "local" }},
		{"repository", func(a *Admin) {
			a.ChartRef = "oci://charts.example.com/stable/ikhnos-agent"
			a.AgentChartVersion = "2.3.4"
		}},
		{"packaged", func(a *Admin) { a.ChartRef = "./ikhnos-agent-1.0.0.tgz" }},
	}
	for _, c := range charts {
		a := p.admin(c.tweak)
		for _, i := range images {
			for _, tier := range []int{0, 1, 2} {
				p.add(a, fmt.Sprintf("agent/install/%s/%s/tier%d", c.name, i.name, tier), "agent", p.ok(a.installCommand(i.img, "ikh_SECRET", store.Token{AccessTier: tier})))
			}
		}
	}
	a := p.admin(nil)
	for _, i := range images {
		for _, tier := range []int{0, 1, 2} {
			p.add(a, fmt.Sprintf("agent/upgrade/%s/tier%d", i.name, tier), "agent", p.ok(a.upgradeCommand(i.img, tier, "", "")))
		}
		p.add(a, "agent/upgrade/"+i.name+"/named", "agent", p.ok(a.upgradeCommand(i.img, 2, "apps-team", "obs-agent")))
	}
	p.add(a, "agent/upgrade/unsafe-names-are-not-trusted", "agent", p.ok(a.upgradeCommand(kernel.ImageConfig{}, 2, "ns; rm -rf /", "rel$(id)")))
	for _, c := range []struct{ name, ns, rel string }{{"unreported", "", ""}, {"namespace-only", "apps-team", ""}, {"named", "apps-team", "obs-agent"}} {
		helm, secret, _, err := agents.TeardownCommands(c.ns, c.rel)
		if err != nil {
			p.t.Fatal(err)
		}
		p.add(nil, "agent/teardown/"+c.name+"/helm", "", helm)
		p.add(nil, "agent/teardown/"+c.name+"/secret", "", secret)
	}
}

func (p *pinRig) operatorCommands() {
	ext := store.Destination{Kind: store.DestinationExternal, Endpoint: "collector.example.com:4317"}
	extFull := store.Destination{Kind: store.DestinationExternal, Endpoint: "collector.example.com:4317", Insecure: true, CAFile: "/etc/ssl/ca.pem",
		AuthHeaderName: "Authorization", AuthSecretName: "collector-auth", AuthSecretKey: "token"}
	viaOperator := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: "central"}
	viaAddress := store.Destination{Kind: store.DestinationOperator, TargetOperatorID: "tgt", Address: "10.0.0.7:4317"}
	mtls := func(d store.Destination) store.Operator {
		return store.Operator{ID: "athens", Name: "athens", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS, Destination: d}
	}
	bearer := func(d store.Destination) store.Operator {
		return store.Operator{ID: "oslo", Name: "oslo", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer, Destination: d}
	}
	exposed := func(op store.Operator, exposure string) store.Operator { op.Exposure = exposure; return op }
	tgt := store.Operator{ID: "tgt", Name: "tgt", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS, Address: "tgt.example.com:4317"}
	central := store.Operator{ID: operators.CentralOperatorID, Name: "central", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS}

	type opCase struct {
		name   string
		op     store.Operator
		bundle operators.OperatorTLSBundle
		hb     string
		target *store.Operator
	}
	ops := []opCase{
		{"mtls-external", mtls(ext), pinBundle, "", nil},
		{"mtls-external-full", mtls(extFull), pinBundle, "", nil},
		{"mtls-external-heartbeat", mtls(ext), pinBundle, "https://ikhnos.example.com/api/v1/operator-heartbeat", nil},
		{"mtls-to-central", mtls(viaOperator), pinBundle, "", &central},
		{"mtls-to-operator-id-only", mtls(viaOperator), pinBundle, "", nil},
		{"mtls-to-operator-address", mtls(viaAddress), pinBundle, "", &tgt},
		{"bearer-external", bearer(ext), operators.OperatorTLSBundle{}, "", nil},
		{"bearer-external-tls", bearer(extFull), pinBundle, "", nil},
		{"bearer-to-operator", bearer(viaOperator), operators.OperatorTLSBundle{}, "", &central},
		{"mtls-exposed-loadbalancer", exposed(mtls(ext), "loadbalancer"), pinBundle, "", nil},
		{"mtls-exposed-nodeport", exposed(mtls(ext), "nodeport"), pinBundle, "", nil},
	}
	images := []struct {
		name  string
		img   kernel.ImageConfig
		tweak func(*Admin)
	}{
		{"no-registry", kernel.ImageConfig{}, nil},
		{"registry", kernel.ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0"}, nil},
		{"repository", kernel.ImageConfig{}, func(a *Admin) {
			a.ChartRef = "oci://charts.example.com/stable/ikhnos-agent"
			a.OperatorChartVersion = "0.9.1"
		}},
		{"no-server", kernel.ImageConfig{}, func(a *Admin) { a.AgentAddr = "" }},
	}
	for _, i := range images {
		a := p.admin(i.tweak)
		for _, o := range ops {
			for _, reissue := range []bool{false, true} {
				install, secret, err := a.operatorsAPI().InstallCommand(i.img, "cno_SECRET", o.op, o.bundle, o.hb, o.target, reissue)
				if err != nil {
					p.t.Fatal(err)
				}
				base := fmt.Sprintf("operator/install/%s/%s/reissue=%v", i.name, o.name, reissue)
				p.add(a, base, "regional-operator", install)
				if secret != "" {
					p.add(a, base+"/secret", "", secret)
				}
			}
		}
	}

	a := p.admin(nil)
	p.add(a, "operator/receiver-secret/mtls", "", p.ok(operatorsapi.TLSSecretCommand(mtls(ext), pinBundle)))
	p.add(a, "operator/client-secret", "", p.ok(operatorsapi.ClientSecretCommand(mtls(ext), pinBundle.Senders[0].CertPEM, pinBundle.Senders[0].KeyPEM, pinBundle.CACertPEM, "apps-team", false)))
	p.add(a, "operator/restart", "", p.ok(operatorsapi.RestartCommand(mtls(ext))))
	p.add(a, "operator/uninstall", "", p.ok(operatorsapi.UninstallCommand("athens")))
	p.add(a, "operator/expose/nodeport", "", p.ok(estate.ExposeCommand("ikhnos-system", "athens-regional-operator", install.NodePort)))
	p.add(a, "operator/expose/loadbalancer", "", p.ok(estate.ExposeCommand("ikhnos-system", "athens-regional-operator", install.LoadBalancer)))

	// The heartbeat: the upgrade that turns it on, the Secret that carries its token and, for a private CA, the Secret for that.
	hbURL := "https://ikhnos.example.com/api/v1/operator-heartbeat"
	for _, i := range images[:3] {
		a := p.admin(i.tweak)
		p.add(a, "operator/heartbeat/"+i.name+"/upgrade", "regional-operator", p.ok(a.operatorsAPI().HeartbeatUpgradeCommand(i.img, mtls(ext), hbURL)))
	}
	p.add(a, "operator/heartbeat/secret", "", p.ok(operatorsapi.HeartbeatSecretCommand(mtls(ext), "hb_SECRET")))
	private := p.admin(func(a *Admin) { a.HeartbeatCAPEM = fakePEM("CERTIFICATE", "aGVhcnRiZWF0LWNh") })
	p.add(private, "operator/heartbeat/private-ca/upgrade", "regional-operator", p.ok(private.operatorsAPI().HeartbeatUpgradeCommand(kernel.ImageConfig{}, mtls(ext), hbURL)))
	p.add(private, "operator/heartbeat/private-ca/secret", "", p.ok(private.operatorsAPI().HeartbeatCASecretCommand(mtls(ext))))

	// What the source clusters of an operator are told: a Secret for each distinct namespace and an upgrade of each cluster's agent.
	targets := []operatorsapi.SourceTarget{{Cluster: "cl-a", NS: "ikhnos-system", Name: "ikhnos-agent"}, {Cluster: "cl-b", NS: "apps-team", Name: "obs-agent"}}
	for _, i := range images[:2] {
		a := p.admin(i.tweak)
		for n, line := range p.reminders(a, i.img, mtls(ext), pinBundle, targets) {
			chart := ""
			if strings.HasPrefix(line, "helm ") {
				chart = "agent"
			}
			p.add(a, fmt.Sprintf("operator/sources/%s/%d", i.name, n), chart, line)
		}
	}
	a = p.admin(nil)
	for n, line := range p.reminders(a, kernel.ImageConfig{}, bearer(ext), operators.OperatorTLSBundle{}, targets[:1]) {
		chart := ""
		if strings.HasPrefix(line, "helm ") {
			chart = "agent"
		}
		p.add(a, fmt.Sprintf("operator/sources/bearer-no-certificate/%d", n), chart, line)
	}
}

// fragments are the pieces of a command the server hands out to be joined to a helm command the caller writes (the flags that point an
// exporter at a regional operator, the ones that clear a route, the heartbeat's, the provenance stamp): none is a command alone, so none
// has a chart.
func (p *pinRig) fragments() {
	mtls := store.Operator{ID: "athens", Name: "athens", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS}
	bearer := store.Operator{ID: "oslo", Name: "oslo", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthBearer}
	central := store.Operator{ID: operators.CentralOperatorID, Name: "central", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS}
	a := p.admin(nil)
	for _, o := range []store.Operator{mtls, bearer, central} {
		for _, withMTLS := range []bool{true, false} {
			p.add(a, fmt.Sprintf("fragment/export-block/%s/mtls=%v", o.ID, withMTLS), "", p.flags(operatorsapi.ExportBlock("telemetry.export.otlp", o, "op.example.com:4317", withMTLS)))
		}
	}
	for _, m := range operatorsapi.RouteModalities {
		p.add(a, "fragment/route/"+string(m), "", p.flags(operatorsapi.RouteValues(mtls, "op.example.com:4317", m)))
	}
	p.add(a, "fragment/clear-routes/all", "", p.flags(operatorsapi.ClearRoutes(operatorsapi.RouteModalities...)))
	p.add(a, "fragment/clear-routes/logs-traces", "", p.flags(operatorsapi.ClearRoutes(store.ModalityLogs, store.ModalityTraces)))
	for _, c := range []struct {
		name string
		op   store.Operator
		cert []byte
	}{{"mtls-new-certificate", mtls, pinBundle.Senders[0].CertPEM}, {"mtls-certificate-in-place", mtls, nil}, {"bearer-new-certificate", bearer, pinBundle.Senders[0].CertPEM}, {"bearer", bearer, nil}} {
		values, secret, err := operatorsapi.DestinationCommand(c.op, "op.example.com:4317", c.cert, pinBundle.Senders[0].KeyPEM, pinBundle.CACertPEM, "apps-team")
		if err != nil {
			p.t.Fatal(err)
		}
		p.add(a, "fragment/destination/"+c.name, "", p.flags(values))
		if secret != "" {
			p.add(a, "fragment/destination/"+c.name+"/secret", "", secret)
		}
	}
	p.add(a, "fragment/heartbeat", "", p.flags(a.operatorsAPI().HeartbeatValues(mtls, "https://ikhnos.example.com/api/v1/operator-heartbeat")))
	private := p.admin(func(a *Admin) { a.HeartbeatCAPEM = fakePEM("CERTIFICATE", "aGVhcnRiZWF0LWNh") })
	p.add(private, "fragment/heartbeat-private-ca", "", p.flags(private.operatorsAPI().HeartbeatValues(mtls, "https://ikhnos.example.com/api/v1/operator-heartbeat")))
	p.add(a, "fragment/intent-provenance", "", p.flags(operatorsapi.IntentProvenanceValues("org-1", "cl-a", "ti_1")))
}

func TestServerInstallCommandsArePinned(t *testing.T) {
	p := &pinRig{t: t, names: map[string]bool{}}
	p.agentCommands()
	p.operatorCommands()
	p.fragments()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(p.cases); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(serverInstallCommandsGolden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(serverInstallCommandsGolden)
	if err != nil {
		t.Fatalf("%v (create it with -update)", err)
	}
	if string(want) != got {
		t.Errorf("a command this server prints changed. The difference, as lines only in one side:\n%s\nIf this is intended, run: go test ./internal/server -run TestServerInstallCommandsArePinned -update", testkit.LineDiff(string(want), got))
	}
}
