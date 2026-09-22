package server

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/store"
)

const goodDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testAdmin(t *testing.T) *Admin {
	t.Helper()
	e := newEnv(t)
	return &Admin{P: NewPlatform(e.base, nil), C: e.base, AgentAddr: "srv.example.com:8443"}
}

func cmdFor(a *Admin, img ImageConfig) string {
	return a.installCommand(img, "cnt_1", store.Token{AccessTier: 2})
}

// The install command must never point at a directory that exists only in a source checkout.
func TestInstallCommandNamesAChartTheOperatorCanGet(t *testing.T) {
	a := testAdmin(t)
	cmd := cmdFor(a, ImageConfig{})
	if strings.Contains(cmd, "deploy/helm") || !strings.Contains(cmd, "./"+chart.Filename()) {
		t.Fatalf("default command should use the downloadable chart file:\n%s", cmd)
	}
	// The wizard is told which file to offer, and the server really serves it without a session.
	h := a.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/charts/"+chart.Filename(), nil))
	if rr.Code != http.StatusOK || rr.Body.Len() < 500 || !strings.Contains(rr.Header().Get("Content-Disposition"), chart.Filename()) {
		t.Fatalf("chart download: %d, %d bytes, %q", rr.Code, rr.Body.Len(), rr.Header().Get("Content-Disposition"))
	}
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/charts/other.tgz", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("only the chart is served, got %d", rr.Code)
	}
	if a.chartFile() != chart.Filename() || a.chartRef(ImageConfig{}) != "" {
		t.Fatalf("the wizard is told to offer %q", a.chartFile())
	}
}

// The four states an installation can be in: nothing configured, a registry, a registry and tag, and a digest.
func TestInstallCommandForTheFourImageSettings(t *testing.T) {
	a := testAdmin(t)
	ver := " --version " + chart.Version() + " "

	// 1. Nothing set: no image values at all (the chart's own names apply), and the chart is the served file.
	cmd := cmdFor(a, ImageConfig{})
	if strings.Contains(cmd, "image.") || strings.Contains(cmd, "oci://") || strings.Contains(cmd, "--version") || strings.Contains(cmd, "alexandrosst") {
		t.Fatalf("nothing configured must print no image settings and no registry:\n%s", cmd)
	}

	// 2. Registry only: one image, the chart from the same registry, no tag and no digest.
	cmd = cmdFor(a, ImageConfig{Registry: "myteam"})
	for _, want := range []string{"helm install continuum-agent oci://registry-1.docker.io/myteam/continuum-agent" + ver + "\\\n", "--set image.repository=myteam/continuum", "--set enrollment.key=" + a.C.CA.Pin() + ".cnt_1"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in:\n%s", want, cmd)
		}
	}
	for _, not := range []string{"image.tag", "image.digest", "./", "nodeProbe.image", "flowObserver.image", "/agent", "/probe", "/flow"} {
		if strings.Contains(cmd, not) {
			t.Fatalf("registry only must not print %q:\n%s", not, cmd)
		}
	}

	// 3. Registry and tag.
	cmd = cmdFor(a, ImageConfig{Registry: "ghcr.io/me/x", Tag: "1.2.3"})
	for _, want := range []string{"oci://ghcr.io/me/x/continuum-agent" + ver, "--set image.repository=ghcr.io/me/x/continuum", "--set image.tag=1.2.3"} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "image.digest") {
		t.Fatalf("no digest was set:\n%s", cmd)
	}

	// 4. Registry, tag and digest: the digest is printed too (the chart then pins by it).
	cmd = cmdFor(a, ImageConfig{Registry: "reg.example.com:8443/team", Tag: "1.2.3", Digest: goodDigest})
	for _, want := range []string{"--set image.repository=reg.example.com:8443/team/continuum", "--set image.tag=1.2.3", "--set image.digest=" + goodDigest} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in:\n%s", want, cmd)
		}
	}
	cmd = cmdFor(a, ImageConfig{Registry: "myteam", Digest: goodDigest})
	if strings.Contains(cmd, "image.tag") || !strings.Contains(cmd, "image.digest="+goodDigest) {
		t.Fatalf("digest without a tag:\n%s", cmd)
	}
	// The only secret on the command line is the enrollment token, as before; no image value carries one.
	if strings.Count(cmd, "cnt_1") != 1 {
		t.Fatalf("the token appears once:\n%s", cmd)
	}
}

// The CA pin and the one-time token print as one enrollment.key flag ("<pin>.<token>"), not the two separate
// server.caPin / enrollment.token flags the chart also still accepts on its own.
func TestInstallCommandBundlesTheCAPinAndToken(t *testing.T) {
	a := testAdmin(t)
	cmd := cmdFor(a, ImageConfig{})
	want := "--set enrollment.key=" + a.C.CA.Pin() + ".cnt_1"
	if !strings.Contains(cmd, want) {
		t.Fatalf("missing %q in:\n%s", want, cmd)
	}
	for _, not := range []string{"server.caPin", "enrollment.token"} {
		if strings.Contains(cmd, not) {
			t.Fatalf("the bundled command must not also print %q:\n%s", not, cmd)
		}
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(a.C.CA.Pin()) {
		t.Fatalf("the pin half must be 64 lowercase hex characters (the chart's schema requires this): %q", a.C.CA.Pin())
	}
}

// The --version flag on an OCI/registry chart reference must say what the release pipeline actually published
// under, when the binary knows it (AgentChartVersion, set at link time for a CI build) - not chart.Version()'s
// Chart.yaml literal, which a CI-published chart's real OCI version routinely disagrees with (see
// Admin.agentChartVersion's doc comment). A local build that was never linked with that information (the zero
// value) still falls back to chart.Version(), same as before this existed.
func TestInstallCommandUsesThePublishedChartVersionWhenKnown(t *testing.T) {
	a := testAdmin(t)
	img := ImageConfig{Registry: "myteam"}

	// Unset: falls back to chart.Version(), unchanged from before.
	if cmd := cmdFor(a, img); !strings.Contains(cmd, " --version "+chart.Version()+" ") {
		t.Fatalf("no AgentChartVersion should fall back to chart.Version():\n%s", cmd)
	}

	// Set (as a CI-built binary would be, via -X main.agentChartVersion=...): wins outright, even though it looks
	// nothing like chart.Version()'s "0.1.0"-shaped literal.
	a.AgentChartVersion = "0.0.0-edge.5fb6022"
	cmd := cmdFor(a, img)
	if !strings.Contains(cmd, " --version 0.0.0-edge.5fb6022 ") {
		t.Fatalf("AgentChartVersion should win:\n%s", cmd)
	}
	if strings.Contains(cmd, chart.Version()) {
		t.Fatalf("chart.Version() must not leak in once AgentChartVersion is set:\n%s", cmd)
	}

	// A local ./file.tgz reference never prints --version at all, whichever is set: the file already is the
	// exact chart this binary was built from.
	a.ChartRef = "local"
	if cmd := cmdFor(a, img); strings.Contains(cmd, "--version") {
		t.Fatalf("a local chart reference must not print --version:\n%s", cmd)
	}

	// upgradeCommand (the tier-ceiling "harden" hint) must agree with installCommand.
	a.ChartRef = ""
	if cmd := a.upgradeCommand(img, 2, "continuum-system", "continuum-agent"); !strings.Contains(cmd, " --version 0.0.0-edge.5fb6022 ") {
		t.Fatalf("upgradeCommand should also use AgentChartVersion:\n%s", cmd)
	}
}

// An explicit --chart-ref wins over the registry's own chart, and "local" forces the served file.
func TestExplicitChartRef(t *testing.T) {
	a := testAdmin(t)
	img := ImageConfig{Registry: "reg.example.com/team/", Tag: "1.2.3"}
	img, _ = img.Normalize()
	a.ChartRef = "oci://reg.example.com/charts/continuum-agent"
	cmd := cmdFor(a, img)
	if !strings.Contains(cmd, "oci://reg.example.com/charts/continuum-agent --version "+chart.Version()+" ") || strings.Contains(cmd, "./continuum-agent") || !strings.Contains(cmd, "image.repository=reg.example.com/team/continuum") {
		t.Fatalf("an explicit chart reference replaces the local file but not the image:\n%s", cmd)
	}
	a.ChartRef = "local"
	if cmd = cmdFor(a, img); !strings.Contains(cmd, "./"+chart.Filename()) || strings.Contains(cmd, "--version") {
		t.Fatalf("local should use the file:\n%s", cmd)
	}
}

func TestOCIBase(t *testing.T) {
	for in, want := range map[string]string{
		"myteam":                    "registry-1.docker.io/myteam",
		"docker.io/myteam/":         "registry-1.docker.io/myteam",
		"index.docker.io/team/sub":  "registry-1.docker.io/team/sub",
		"ghcr.io/me/continuum":      "ghcr.io/me/continuum",
		"localhost:5000/x":          "localhost:5000/x",
		"reg.example.com:8443/team": "reg.example.com:8443/team",
	} {
		if got := OCIBase(in); got != want {
			t.Errorf("OCIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImageValidation(t *testing.T) {
	good := []string{"myteam", "docker.io/myteam", "ghcr.io/me/x", "reg.example.com:8443/team", "localhost:5000", "localhost:5000/a/b-c/d_e/f.g", "10.0.0.5:5000/team", "reg.example.com/team/"}
	for _, g := range good {
		if _, err := CheckImageRegistry(g); err != nil {
			t.Errorf("registry %q should be valid: %v", g, err)
		}
	}
	bad := []string{"https://reg.example.com/x", "Reg.Example.com/x", "reg example.com", "reg.example.com/x y", "reg.example.com/x@sha256:abc", "reg.example.com/x?y=1", "reg.example.com/x#f", "reg.example.com//x", "/x", "reg.example.com:99999999/x", "reg.example.com/-x", "reg.example.com/x,y", "reg.example.com/x;rm -rf", "reg.example.com/$(id)", "-reg.example.com", "reg.example.com/x'y", strings.Repeat("a", 256)}
	for _, b := range bad {
		if _, err := CheckImageRegistry(b); err == nil {
			t.Errorf("registry %q should be rejected", b)
		}
	}
	if v, _ := CheckImageRegistry("  reg.example.com/team//  "); v != "reg.example.com/team" {
		t.Errorf("registry is trimmed, got %q", v)
	}
	for _, g := range []string{"1.2.3", "latest", "v1_2-3.rc1", "_x", strings.Repeat("a", 128)} {
		if _, err := CheckImageTag(g); err != nil {
			t.Errorf("tag %q should be valid: %v", g, err)
		}
	}
	for _, b := range []string{"-1", ".1", "a b", "a:b", "a@b", "a/b", "a,b", strings.Repeat("a", 129), "é"} {
		if _, err := CheckImageTag(b); err == nil {
			t.Errorf("tag %q should be rejected", b)
		}
	}
	if _, err := CheckImageDigest(goodDigest); err != nil {
		t.Errorf("digest: %v", err)
	}
	for _, b := range []string{"sha256:abc", "sha512:" + strings.Repeat("a", 64), strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("g", 64), goodDigest + "0"} {
		if _, err := CheckImageDigest(b); err == nil {
			t.Errorf("digest %q should be rejected", b)
		}
	}
	// A tag or digest names something inside a registry.
	for _, c := range []ImageConfig{{Tag: "1.0"}, {Digest: goodDigest}} {
		if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "registry") {
			t.Errorf("%+v needs a registry, got %v", c, err)
		}
	}
	if _, err := (Settings{ImageRegistry: "https://x"}).Normalize(); err == nil {
		t.Error("settings must validate the registry")
	}
}

// The organisation's own setting beats the server's flags; the flags beat nothing; the parts never mix sources.
func TestImagePrecedenceAndTheEndpoints(t *testing.T) {
	a := newAdminRig(t)
	a.a.ChartRef = "" // the rig pins one; here the registry decides
	_, admin := a.user(t, "root", RoleAdmin)
	_, viewer := a.user(t, "eve", RoleViewer)
	install := func() string {
		r := a.do("POST", "/api/v1/tokens", map[string]any{"name": "c1", "tier": 2}, withCookie(admin))
		if r.Code != 201 {
			t.Fatalf("token: %d %s", r.Code, r.Body.String())
		}
		return r.json(t)["install"].(string)
	}
	info := func() map[string]any {
		r := a.do("GET", "/api/v1/info", nil, withCookie(viewer))
		return r.json(t)["install"].(map[string]any)
	}

	// Nothing set anywhere.
	if c := install(); strings.Contains(c, "image.") || !strings.Contains(c, "./"+chart.Filename()) {
		t.Fatalf("nothing set:\n%s", c)
	}
	if i := info(); i["imagesConfigured"] != false || i["imageRegistry"] != "" || i["imageDigest"] != "" || i["chartRef"] != "" || i["chartFile"] != chart.Filename() {
		t.Fatalf("info with nothing set: %v", i)
	}

	// Flags only.
	a.a.ImageRegistry, a.a.ImageTag, a.a.ImageDigest = "flagreg/team", "9.9.9", ""
	if c := install(); !strings.Contains(c, "image.repository=flagreg/team/continuum") || !strings.Contains(c, "image.tag=9.9.9") {
		t.Fatalf("flags:\n%s", c)
	}
	if r := a.do("GET", "/api/v1/settings", nil, withCookie(admin)).json(t); r["imageRegistry"] != "" {
		t.Fatalf("the flag is a default, not the organisation's setting: %v", r)
	} else if d := r["imageDefaults"].(map[string]any); d["registry"] != "flagreg/team" || d["tag"] != "9.9.9" {
		t.Fatalf("the settings document shows the server's defaults: %v", r)
	}

	// The organisation's setting wins, as a whole: the flag's tag is not mixed in.
	put := a.do("PUT", "/api/v1/settings", Settings{ImageRegistry: "uireg/x", ImageDigest: goodDigest}, withCookie(admin))
	if put.Code != 200 || put.json(t)["imageRegistry"] != "uireg/x" {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	c := install()
	if !strings.Contains(c, "image.repository=uireg/x/continuum") || !strings.Contains(c, "image.digest="+goodDigest) || strings.Contains(c, "flagreg") || strings.Contains(c, "9.9.9") || strings.Contains(c, "image.tag") {
		t.Fatalf("the setting must beat the flag:\n%s", c)
	}
	if i := info(); i["imageRegistry"] != "uireg/x" || i["imageDigest"] != goodDigest || i["imageTag"] != "" || i["imagesConfigured"] != true || i["chartRef"] != "oci://registry-1.docker.io/uireg/x/continuum-agent" {
		t.Fatalf("info follows the setting: %v", i)
	}

	// Clearing it falls back to the flags.
	if r := a.do("PUT", "/api/v1/settings", Settings{}, withCookie(admin)); r.Code != 200 {
		t.Fatalf("clear: %d %s", r.Code, r.Body.String())
	}
	if c := install(); !strings.Contains(c, "flagreg/team/continuum") || strings.Contains(c, "uireg") {
		t.Fatalf("cleared setting falls back to the flag:\n%s", c)
	}

	// Server-side validation: a bad value is a 400 with words, nothing is stored, and only admins may write.
	for _, s := range []Settings{{ImageRegistry: "https://x.io/y"}, {ImageRegistry: "X.io"}, {ImageRegistry: "x.io", ImageTag: "a b"}, {ImageRegistry: "x.io", ImageDigest: "sha256:zz"}, {ImageTag: "1.0"}} {
		r := a.do("PUT", "/api/v1/settings", s, withCookie(admin))
		if r.Code != 400 || r.json(t)["error"] == "" {
			t.Fatalf("%+v: %d %s", s, r.Code, r.Body.String())
		}
	}
	if r := a.do("PUT", "/api/v1/settings", Settings{ImageRegistry: "evil.io/x"}, withCookie(viewer)); r.Code != 403 {
		t.Fatalf("a viewer changed the registry: %d", r.Code)
	}
	tn, err := a.a.P.Tenant(a.ctx, "org-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := tn.C.Settings().ImageRegistry; got != "" {
		t.Fatalf("a rejected save stored %q", got)
	}
}

// The setting is per organisation: another organisation keeps using the server's flags.
func TestImageSettingIsPerOrganisation(t *testing.T) {
	e := newEnv(t)
	a := &Admin{P: NewPlatform(e.base, nil), C: e.base, AgentAddr: "x:1", ImageRegistry: "flagreg"}
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{ImageRegistry: "org1reg"}); err != nil {
		t.Fatal(err)
	}
	if got := a.images(e.core); got.Registry != "org1reg" {
		t.Fatalf("org-1: %+v", got)
	}
	other := e.base.ForOrg("org-2")
	if got := a.images(other); got.Registry != "flagreg" {
		t.Fatalf("another organisation must not inherit org-1's registry: %+v", got)
	}
}

// Changing what clusters are told to pull is a supply-chain decision: it is audited with both values, and it
// survives a restart.
func TestImageChangesAreAuditedAndPersist(t *testing.T) {
	e := newEnv(t)
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{ImageRegistry: "old.io/a", ImageTag: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{ImageRegistry: "new.io/b", ImageDigest: goodDigest}); err != nil {
		t.Fatal(err)
	}
	evs, _ := e.st.ListAudit(e.ctx, "org-1", 20)
	var details []string
	for _, ev := range evs {
		if ev.Action == "settings-changed" {
			details = append(details, ev.Detail)
		}
	}
	joined := strings.Join(details, "\n")
	for _, want := range []string{"image registry (none) → old.io/a", "image tag (none) → 1", "image registry old.io/a → new.io/b", "image tag 1 → (none)", "image digest (none) → " + goodDigest} {
		if !strings.Contains(joined, want) {
			t.Fatalf("audit rows lack %q:\n%s", want, joined)
		}
	}
	// Saving the same values again writes no new row.
	n := len(details)
	if _, err := e.core.SaveSettings(e.ctx, "alex", Settings{ImageRegistry: "new.io/b", ImageDigest: goodDigest}); err != nil {
		t.Fatal(err)
	}
	evs, _ = e.st.ListAudit(e.ctx, "org-1", 20)
	m := 0
	for _, ev := range evs {
		if ev.Action == "settings-changed" {
			m++
		}
	}
	if m != n {
		t.Fatalf("an unchanged save wrote an audit row (%d → %d)", n, m)
	}
	c2 := NewCore(e.st, e.core.CA, "org-1", nil)
	c2.LoadSettings(e.ctx)
	if s := c2.Settings(); s.ImageRegistry != "new.io/b" || s.ImageDigest != goodDigest {
		t.Fatalf("after restart: %+v", s)
	}
}
