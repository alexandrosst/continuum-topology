package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/store"
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

var pinBundle = OperatorTLSBundle{
	ReceiverCertPEM: fakePEM("CERTIFICATE", "cmVjZWl2ZXItY2VydA=="),
	ReceiverKeyPEM:  fakePEM("EC PRIVATE KEY", "cmVjZWl2ZXIta2V5"),
	CACertPEM:       fakePEM("CERTIFICATE", "b3BlcmF0b3ItY2E="),
	Senders: []SenderCert{
		{Sender: "cl-a", CertPEM: fakePEM("CERTIFICATE", "c2VuZGVyLWEtY2VydA=="), KeyPEM: fakePEM("EC PRIVATE KEY", "c2VuZGVyLWEta2V5")},
		{Sender: "cl-b", CertPEM: fakePEM("CERTIFICATE", "c2VuZGVyLWItY2VydA=="), KeyPEM: fakePEM("EC PRIVATE KEY", "c2VuZGVyLWIta2V5")},
	},
}

func (p *pinRig) agentCommands() {
	images := []struct {
		name string
		img  ImageConfig
	}{
		{"no-registry", ImageConfig{}},
		{"registry", ImageConfig{Registry: "registry.example.com/team"}},
		{"registry-tag", ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0"}},
		{"registry-digest", ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0", Digest: goodDigest}},
		{"hub-namespace", ImageConfig{Registry: "myteam"}},
	}
	charts := []struct {
		name  string
		tweak func(*Admin)
	}{
		{"default", nil},
		{"local", func(a *Admin) { a.ChartRef = "local" }},
		{"repository", func(a *Admin) {
			a.ChartRef = "oci://charts.example.com/stable/continuum-agent"
			a.AgentChartVersion = "2.3.4"
		}},
		{"packaged", func(a *Admin) { a.ChartRef = "./continuum-agent-1.0.0.tgz" }},
	}
	for _, c := range charts {
		a := p.admin(c.tweak)
		for _, i := range images {
			for _, tier := range []int{0, 1, 2} {
				p.add(a, fmt.Sprintf("agent/install/%s/%s/tier%d", c.name, i.name, tier), "agent", a.installCommand(i.img, "cnt_SECRET", store.Token{AccessTier: tier}))
			}
		}
	}
	a := p.admin(nil)
	for _, i := range images {
		for _, tier := range []int{0, 1, 2} {
			p.add(a, fmt.Sprintf("agent/upgrade/%s/tier%d", i.name, tier), "agent", a.upgradeCommand(i.img, tier, "", ""))
		}
		p.add(a, "agent/upgrade/"+i.name+"/named", "agent", a.upgradeCommand(i.img, 2, "apps-team", "obs-agent"))
	}
	p.add(a, "agent/upgrade/unsafe-names-are-not-trusted", "agent", a.upgradeCommand(ImageConfig{}, 2, "ns; rm -rf /", "rel$(id)"))
	for _, c := range []struct{ name, ns, rel string }{{"unreported", "", ""}, {"namespace-only", "apps-team", ""}, {"named", "apps-team", "obs-agent"}} {
		helm, secret, _ := teardownCommands(c.ns, c.rel)
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
	tgt := store.Operator{ID: "tgt", Name: "tgt", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS, Address: "tgt.example.com:4317"}
	central := store.Operator{ID: CentralOperatorID, Name: "central", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS}

	type opCase struct {
		name   string
		op     store.Operator
		bundle OperatorTLSBundle
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
		{"bearer-external", bearer(ext), OperatorTLSBundle{}, "", nil},
		{"bearer-external-tls", bearer(extFull), pinBundle, "", nil},
		{"bearer-to-operator", bearer(viaOperator), OperatorTLSBundle{}, "", &central},
	}
	images := []struct {
		name  string
		img   ImageConfig
		tweak func(*Admin)
	}{
		{"no-registry", ImageConfig{}, nil},
		{"registry", ImageConfig{Registry: "registry.example.com/team", Tag: "1.4.0"}, nil},
		{"repository", ImageConfig{}, func(a *Admin) {
			a.ChartRef = "oci://charts.example.com/stable/continuum-agent"
			a.OperatorChartVersion = "0.9.1"
		}},
		{"no-server", ImageConfig{}, func(a *Admin) { a.AgentAddr = "" }},
	}
	for _, i := range images {
		a := p.admin(i.tweak)
		for _, o := range ops {
			for _, reissue := range []bool{false, true} {
				install, secret := a.operatorInstallCommandWith(i.img, "cno_SECRET", o.op, o.bundle, o.hb, o.target, reissue)
				base := fmt.Sprintf("operator/install/%s/%s/reissue=%v", i.name, o.name, reissue)
				p.add(a, base, "regional-operator", install)
				if secret != "" {
					p.add(a, base+"/secret", "", secret)
				}
			}
		}
	}

	a := p.admin(nil)
	p.add(a, "operator/receiver-secret/mtls", "", operatorTLSSecretCommand(mtls(ext), pinBundle))
	p.add(a, "operator/client-secret", "", operatorClientSecretCommand(mtls(ext), pinBundle.Senders[0].CertPEM, pinBundle.Senders[0].KeyPEM, pinBundle.CACertPEM, "apps-team"))
	p.add(a, "operator/restart", "", operatorRestartCommand(mtls(ext)))
	p.add(a, "operator/uninstall", "", operatorUninstallCommand("athens"))
	p.add(a, "operator/expose/nodeport", "", exposeCommand("continuum-system", "athens-regional-operator", "NodePort"))
	p.add(a, "operator/expose/loadbalancer", "", exposeCommand("continuum-system", "athens-regional-operator", "LoadBalancer"))

	// The heartbeat: the upgrade that turns it on, the Secret that carries its token and, for a private CA, the Secret for that.
	hbURL := "https://ikhnos.example.com/api/v1/operator-heartbeat"
	for _, i := range images[:3] {
		a := p.admin(i.tweak)
		p.add(a, "operator/heartbeat/"+i.name+"/upgrade", "regional-operator", a.heartbeatUpgradeCommand(i.img, mtls(ext), hbURL))
	}
	p.add(a, "operator/heartbeat/secret", "", operatorHeartbeatSecretCommand(mtls(ext), "hb_SECRET"))
	private := p.admin(func(a *Admin) { a.HeartbeatCAPEM = fakePEM("CERTIFICATE", "aGVhcnRiZWF0LWNh") })
	p.add(private, "operator/heartbeat/private-ca/upgrade", "regional-operator", private.heartbeatUpgradeCommand(ImageConfig{}, mtls(ext), hbURL))
	p.add(private, "operator/heartbeat/private-ca/secret", "", private.heartbeatCASecretCommand(mtls(ext)))

	// What the source clusters of an operator are told: a Secret for each distinct namespace and an upgrade of each cluster's agent.
	targets := []sourceTarget{{"cl-a", "continuum-system", "continuum-agent"}, {"cl-b", "apps-team", "obs-agent"}}
	for _, i := range images[:2] {
		a := p.admin(i.tweak)
		for n, line := range a.sourceReminders(i.img, mtls(ext), pinBundle, targets) {
			chart := ""
			if strings.HasPrefix(line, "helm ") {
				chart = "agent"
			}
			p.add(a, fmt.Sprintf("operator/sources/%s/%d", i.name, n), chart, line)
		}
	}
	a = p.admin(nil)
	for n, line := range a.sourceReminders(ImageConfig{}, bearer(ext), OperatorTLSBundle{}, targets[:1]) {
		chart := ""
		if strings.HasPrefix(line, "helm ") {
			chart = "agent"
		}
		p.add(a, fmt.Sprintf("operator/sources/bearer-no-certificate/%d", n), chart, line)
	}
}

func TestServerInstallCommandsArePinned(t *testing.T) {
	p := &pinRig{t: t, names: map[string]bool{}}
	p.agentCommands()
	p.operatorCommands()

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
		t.Errorf("a command this server prints changed. The difference, as lines only in one side:\n%s\nIf this is intended, run: go test ./internal/server -run TestServerInstallCommandsArePinned -update", lineDiff(string(want), got))
	}
}
