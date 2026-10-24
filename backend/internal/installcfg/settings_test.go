package installcfg

import (
	"strings"
	"testing"

	"ikhnos/internal/install"
	"ikhnos/internal/kernel"
)

func TestOCIBase(t *testing.T) {
	for in, want := range map[string]string{
		"myteam":                    "registry-1.docker.io/myteam",
		"docker.io/myteam/":         "registry-1.docker.io/myteam",
		"index.docker.io/team/sub":  "registry-1.docker.io/team/sub",
		"ghcr.io/me/ikhnos":         "ghcr.io/me/ikhnos",
		"localhost:5000/x":          "localhost:5000/x",
		"reg.example.com:8443/team": "reg.example.com:8443/team",
	} {
		if got := OCIBase(in); got != want {
			t.Errorf("OCIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTheOperatorChartIsDerivedFromTheAgentsOnlyWhereThatIsSafe(t *testing.T) {
	for ref, want := range map[string]string{
		"oci://ghcr.io/acme/charts/ikhnos-agent":         "oci://ghcr.io/acme/charts/ikhnos-regional-operator",
		"https://charts.example.com/stable/ikhnos-agent": "https://charts.example.com/stable/ikhnos-regional-operator",
		"acme/ikhnos-agent":                              "acme/ikhnos-regional-operator",
		"./ikhnos-agent-0.4.0.tgz":                       "", // a packaged file of one chart says nothing about where the other is
		"oci://ghcr.io/acme/charts/something-else":       "",
		"local": "",
	} {
		if got := (Settings{ChartRef: ref}).OperatorRef(kernel.ImageConfig{}); got != want {
			t.Errorf("operator chart for %q = %q, want %q", ref, got, want)
		}
	}
	// With no explicit reference, a configured registry holds both charts side by side.
	if got := (Settings{}).OperatorRef(kernel.ImageConfig{Registry: "ghcr.io/me"}); got != "oci://ghcr.io/me/ikhnos-regional-operator" {
		t.Errorf("from the registry: %q", got)
	}
	if got := (Settings{}).AgentRef(kernel.ImageConfig{}); got != "" {
		t.Errorf("with nothing configured the chart is the file the server serves: %q", got)
	}
}

func TestImagesTakeTheOrganisationsWholeSettingOrTheServersAndNeverAMix(t *testing.T) {
	s := Settings{ImageRegistry: "flagreg/", ImageTag: "flagtag", ImageDigest: "sha256:flag"}
	if got := s.Images(nil); got != (kernel.ImageConfig{Registry: "flagreg", Tag: "flagtag", Digest: "sha256:flag"}) {
		t.Errorf("server defaults: %+v", got)
	}
	if got := s.ImageDefaults(); got.Registry != "flagreg" {
		t.Errorf("the trailing slash is not part of a registry: %q", got.Registry)
	}
}

func TestACommandThatCannotNameItsChartIsRefusedNotPrinted(t *testing.T) {
	s := Settings{AgentAddr: "srv:8443", ChartRef: "--post-renderer=/tmp/x"}
	if cmd, err := s.AgentInstallCommand(kernel.ImageConfig{}, "sha256/pin", "ikh_1", 2); err == nil {
		t.Errorf("install: %s", cmd)
	}
	if cmd, err := s.AgentUpgradeCommand(kernel.ImageConfig{}, 2, "ns", "rel"); err == nil {
		t.Errorf("upgrade: %s", cmd)
	}
}

func TestTheInstallCommandPacksThePinAndTheTokenAndOmitsTheDefaultTier(t *testing.T) {
	s := Settings{AgentAddr: "srv:8443"}
	cmd, err := s.AgentInstallCommand(kernel.ImageConfig{}, "sha256/PIN", "ikh_secret", ChartDefaultAccessTier)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cmd, "sha256/PIN.ikh_secret") || !strings.Contains(cmd, "server.address=srv:8443") {
		t.Errorf("the pin and the token are one flag, joined by a dot:\n%s", cmd)
	}
	if strings.Contains(cmd, "access.tier") {
		t.Errorf("the chart's own default tier is not printed:\n%s", cmd)
	}
	if cmd, _ = s.AgentInstallCommand(kernel.ImageConfig{}, "p", "t", 1); !strings.Contains(cmd, "access.tier=1") {
		t.Errorf("a tier other than the default is:\n%s", cmd)
	}
}

func TestThePublicAddressIsAPrefixOrNothing(t *testing.T) {
	for in, want := range map[string]string{
		"https://ikhnos.example.com/":     "https://ikhnos.example.com",
		"https://ikhnos.example.com/x/y/": "https://ikhnos.example.com/x/y",
		"":                                "",
		"ikhnos.example.com":              "",
		"ftp://ikhnos.example.com":        "",
		"https://u:p@ikhnos.example.com":  "",
		"https://ikhnos.example.com/?q=1": "",
	} {
		if got := (Settings{PublicURL: in}).PublicBase(); got != want {
			t.Errorf("PublicBase(%q) = %q, want %q", in, got, want)
		}
	}
}

// The controller's chart is found the way the operator's is, and unlike the other two it is nowhere in this binary: no reference is no
// controller, never a file to download.
func TestTheControllerChartIsOnlyEverNamedWhereItIsKnownToBe(t *testing.T) {
	for ref, want := range map[string]string{
		"oci://ghcr.io/acme/charts/ikhnos-agent":         "oci://ghcr.io/acme/charts/ikhnos-controller",
		"https://charts.example.com/stable/ikhnos-agent": "https://charts.example.com/stable/ikhnos-controller",
		"acme/ikhnos-agent":                              "acme/ikhnos-controller",
		"ikhnos-agent":                                   "ikhnos-controller",
		"./ikhnos-agent-0.4.0.tgz":                       "",
		"oci://ghcr.io/acme/charts/not-ikhnos-agent":     "",
		"local": "",
	} {
		if got := (Settings{ChartRef: ref}).ControllerRef(kernel.ImageConfig{}); got != want {
			t.Errorf("controller chart for %q = %q, want %q", ref, got, want)
		}
	}
	if got := (Settings{}).ControllerRef(kernel.ImageConfig{Registry: "ghcr.io/me"}); got != "oci://ghcr.io/me/ikhnos-controller" {
		t.Errorf("from the registry: %q", got)
	}
	if got := (Settings{}).ControllerRef(kernel.ImageConfig{}); got != "" {
		t.Errorf("with no registry there is no chart to name: %q", got)
	}
}

func TestTheControllerVersionIsTheOneThePipelinePublishedOrNone(t *testing.T) {
	img := kernel.ImageConfig{Registry: "ghcr.io/me"}
	if _, v := (Settings{ControllerChartVersion: "0.0.0-edge.abc"}).ControllerChart(img); v != "0.0.0-edge.abc" {
		t.Errorf("version %q", v)
	}
	// Not the agent's, which is a different chart's: a guess here would name a version that may not exist.
	if _, v := (Settings{AgentChartVersion: "1.2.3"}).ControllerChart(img); v != "" {
		t.Errorf("the agent chart's version was used for the controller's: %q", v)
	}
}

// What the install command is made of is what the resource is held to: AgentValues is the one list.
func TestTheAgentInstallCommandIsMadeOfAgentValues(t *testing.T) {
	s := Settings{AgentAddr: "srv:8443", ImageRegistry: "reg.example"}
	img := s.Images(nil)
	extra := []install.Value{install.Set("nodeProbe.enabled", "true")}
	cmd, err := s.AgentInstallCommand(img, "sha256/pin", "tok", 1, extra...)
	if err != nil {
		t.Fatal(err)
	}
	ref, version := s.AgentChart(img)
	want, err := install.Helm{Verb: install.Install, Release: "ikhnos-agent", Chart: ref, Version: version, Namespace: "ikhnos-system", CreateNamespace: true,
		Values: s.AgentValues(img, "sha256/pin", "tok", 1, extra...), Layout: install.Wrapped}.Render()
	if err != nil || cmd != want {
		t.Fatalf("%v\n%s\nwant\n%s", err, cmd, want)
	}
	for _, w := range []string{"server.address=srv:8443", "enrollment.key=sha256/pin.tok", "access.tier=1", "image.repository=reg.example/ikhnos", "nodeProbe.enabled=true"} {
		if !strings.Contains(cmd, w) {
			t.Errorf("%q is not in\n%s", w, cmd)
		}
	}
}
