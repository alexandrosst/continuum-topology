// Package installcfg is how this server was told to write the commands it prints for installing and upgrading things: the chart it
// names and the versions it asks for, the images it points them at, the address agents dial, and the public address of the server. It is
// the server's own configuration (the flags it was started with), not a module: the agents', the operators' and the telemetry's routes
// all print commands, and all of them ask this.
//
// An organisation's own settings take precedence over the server's for the images (Images); everything else is the server's alone.
package installcfg

import (
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"ikhnos/internal/chart"
	"ikhnos/internal/install"
	"ikhnos/internal/kernel"
)

// Settings is what the server was started with. Zero values are meaningful: no chart reference means the chart file the server serves,
// no image registry means the chart's built-in image names.
type Settings struct {
	AgentAddr string // host:port agents dial, shown in the install command
	// AgentExposure is how AgentAddr is reached (loadbalancer, nodeport, gateway, clusterip), from --agent-exposure.
	// Purely descriptive - the server cannot check it against how the port is actually reachable - and only used to
	// pick which guidance Settings → Installation shows for changing AgentAddr later. Empty on older installs (a
	// server chart from before this flag existed): the UI then shows generic guidance for all three methods.
	AgentExposure string
	// ReleaseName and ReleaseNamespace are this Helm release's own name and namespace, from --release-name and
	// --release-namespace. Also purely descriptive, also empty on an older install; together with AgentExposure they
	// let Settings → Installation print an exact, ready-to-run helm upgrade command instead of one with blanks in it.
	ReleaseName, ReleaseNamespace string
	ChartRef                      string // an explicit chart reference for install commands; empty means the copy the server serves
	// ImageRegistry, ImageTag and ImageDigest are the server-wide defaults (--image-registry, --image-tag,
	// --image-digest) for where install commands pull the agent image and chart from. An organisation's own
	// Settings → Installation wins over them; with neither, the chart's built-in image names apply. There is no
	// built-in default registry: see kernel's images.
	ImageRegistry, ImageTag, ImageDigest string
	// AgentChartVersion is the ikhnos-agent chart version the release pipeline actually published alongside
	// this exact server build (edge: 0.0.0-edge.<sha>; a tagged release: the tag) - set at link time, empty on a
	// build that skipped that (a local `go build`, or an image scripts/publish.sh built without it). Only matters
	// for a `--version` flag against an OCI/registry chart reference; a local ./file.tgz reference (no registry
	// configured) always serves the exact chart this binary was built from and needs no version at all.
	AgentChartVersion string
	// OperatorChartVersion is ikhnos-regional-operator's own equivalent of AgentChartVersion above - the
	// version the release pipeline published this build's regional-operator chart under, when known.
	OperatorChartVersion string
	// ControllerChartVersion is the version the release pipeline published the optional ikhnos-controller chart under, when known. The
	// controller chart is not carried in this binary (a registry is the only place it comes from), so there is no Chart.yaml here to
	// fall back to: unknown, the command leaves --version out and Helm takes the registry's latest release.
	ControllerChartVersion string
	// PublicURL is the address operators and people outside the cluster reach this server at (scheme and host, and a
	// path prefix if it has one), such as https://ikhnos.example.com. Empty means "whatever host the request came in
	// on", which is wrong for anyone who reached the page through a port-forward or an internal name.
	PublicURL string
	// HeartbeatCAPEM is the certificate of the private CA that signed the server's own HTTPS certificate, when it is
	// not signed by a public one: regional operators verify the heartbeat endpoint with it (heartbeat.tls.caSecretName).
	// Empty when the certificate chains to a root every image already trusts.
	HeartbeatCAPEM []byte
}

// ImageDefaults is what the server's flags (or environment) say, used when an organisation has set nothing.
func (s Settings) ImageDefaults() kernel.ImageConfig {
	return kernel.ImageConfig{Registry: strings.TrimRight(s.ImageRegistry, "/"), Tag: s.ImageTag, Digest: s.ImageDigest}
}

// Images is the image configuration an organisation's install commands use. Precedence: the organisation's own
// setting (Settings → Installation), then the server's --image-* flags, then nothing (the chart's built-in names).
// The three parts are taken together from one source, so a digest saved for a registry is never combined with a
// different registry from a flag.
func (s Settings) Images(c *kernel.Core) kernel.ImageConfig {
	if c != nil {
		if o := c.Settings(); o.ImageRegistry != "" {
			return kernel.ImageConfig{Registry: o.ImageRegistry, Tag: o.ImageTag, Digest: o.ImageDigest}
		}
	}
	return s.ImageDefaults()
}

// ChartDefaultAccessTier is the tier an install gets when the command says nothing: the chart's own default.
const ChartDefaultAccessTier = 2

// AgentInstallCommand is what the operator runs on the cluster to install the agent. The token appears here once, in the
// response to its creation, and is never retrievable again. caPin is the server's CA pin and secret the enrollment token, tier the
// ceiling it was made for.
//
// With an image registry configured it also points the chart at the one image (image.repository, plus image.tag and
// image.digest when set; with a digest the chart pulls repository@digest and ignores the tag) and fetches the chart
// from the same registry. Without one it names the chart file the server serves and leaves every image value to the
// chart's own defaults.
func (s Settings) AgentInstallCommand(img kernel.ImageConfig, caPin, secret string, tier int, extra ...install.Value) (string, error) {
	ref, version := s.AgentChart(img)
	return install.Helm{Verb: install.Install, Release: "ikhnos-agent", Chart: ref, Version: version, Namespace: "ikhnos-system", CreateNamespace: true,
		Values: s.AgentValues(img, caPin, secret, tier, extra...), Layout: install.Wrapped}.Render()
}

// AgentValues are the Helm values of the install command: what every install has (the server, the one flag that holds the CA pin and
// the token, the access tier when it is not the chart's, the image) and then what the person chose to switch on or narrow. A resource
// for the controller is held to the same values (see controller's tests).
func (s Settings) AgentValues(img kernel.ImageConfig, caPin, secret string, tier int, extra ...install.Value) []install.Value {
	// enrollment.key packs the CA pin and the one-time token into the single flag the chart splits back apart at
	// render time (see ikhnos-agent's _helpers.tpl): one thing to paste instead of two, without hiding either
	// value's own text - the pin is the CA's public-key pin (sha256/<base64>, which survives re-issuing the CA
	// certificate), the token cannot contain ".", and base64 has no ".", so joining them is unambiguous.
	values := []install.Value{
		install.Set("server.address", s.AgentAddr),
		install.Set("enrollment.key", caPin+"."+secret),
	}
	if tier != ChartDefaultAccessTier {
		values = append(values, install.Set("access.tier", strconv.Itoa(tier)))
	}
	if img.Configured() {
		values = append(values, install.Set("image.repository", img.Registry+"/ikhnos"))
		if img.Tag != "" {
			values = append(values, install.Set("image.tag", img.Tag))
		}
		if img.Digest != "" {
			values = append(values, install.Set("image.digest", img.Digest))
		}
	}
	// What the person chose to switch on or narrow, after what every install has.
	return append(values, extra...)
}

// AgentUpgradeCommand is what the owner of a cluster runs there to change the ceiling of an agent's install to a tier: raise it
// (the only way widening ever happens; the server cannot do it) or, just as validly, lower it for real, which the tier
// slider in the UI on its own does not do. ns and release name the install, already resolved to names that are safe in a command.
//
// Every upgrade command this server prints uses --reset-then-reuse-values (Helm 3.14 or newer), never --reuse-values. The
// older flag replaces the NEW chart's defaults with the values the OLD release was installed with, so a value block a
// newer chart added after the release was installed does not exist at all and a template that reads it fails with a
// nil-pointer error. The newer flag starts from the new chart's defaults and then puts the person's own values from the old
// release on top, which is what "keep what I set" was always meant to say.
func (s Settings) AgentUpgradeCommand(img kernel.ImageConfig, tier int, ns, release string) (string, error) {
	ref, version := s.AgentChart(img)
	return install.Helm{Verb: install.Upgrade, Release: release, Chart: ref, Version: version, Namespace: ns, ResetThenReuse: true,
		Values: []install.Value{install.Set("access.tier", strconv.Itoa(tier))}}.Render()
}

// AgentChartFile is the packaged chart's file name. The server always serves its own copy, whether or not the
// install command uses it, so an operator can fall back to it.
// AgentChartFile is the packaged chart's file name. The server always serves its own copy, whether or not the
// install command uses it, so an operator can fall back to it.
func (s Settings) AgentChartFile() string { return chart.Agent.Filename() }

// AgentRef is the chart the install command names, or "" when it names the downloaded file. An explicit
// --chart-ref wins ("local" forces the file). Otherwise a configured image registry also holds the chart, as an
// OCI artifact next to the images, so the printed command needs nothing downloaded first.
func (s Settings) AgentRef(img kernel.ImageConfig) string {
	switch {
	case s.ChartRef == "local":
		return ""
	case s.ChartRef != "":
		return s.ChartRef
	case img.Configured():
		return "oci://" + OCIBase(img.Registry) + "/ikhnos-agent"
	}
	return ""
}

// OperatorChartFile/OperatorRef are AgentChartFile/AgentRef's own twins for the regional-operator chart -
// same rules, parameterized on chart.RegionalOperator and the "ikhnos-regional-operator" OCI artifact
// name instead of chart.Agent and "ikhnos-agent". There is no separate --chart-ref flag for it, and s.ChartRef
// (set once, server-wide) names the AGENT chart - the flag's own help says so, and every agent install command uses it
// as it is. Used verbatim here it installed the agent chart with the regional operator's flags (export.otlp.endpoint,
// receiver.tls...): wrong chart, wrong values, a failed or - worse - half-working install. So the operator chart is derived
// from it, on the one assumption that is safe to make: the two charts are published side by side under their own names.
//
//	oci://ghcr.io/acme/charts/ikhnos-agent          -> oci://ghcr.io/acme/charts/ikhnos-regional-operator
//	https://charts.example.com/stable/ikhnos-agent  -> https://charts.example.com/stable/ikhnos-regional-operator
//	acme/ikhnos-agent (a configured repo alias)     -> acme/ikhnos-regional-operator
//
// Only a reference whose last path segment is exactly the agent chart's name is rewritten. Everything else - a .tgz (a
// packaged file of one chart, which says nothing about where the other is), a name that is not the agent chart's, "local" -
// cannot be turned into the operator chart's address by guessing, so the command names the operator chart file this server
// serves (the "./ikhnos-regional-operator-<version>.tgz" the page offers as a download, which the operator chart's install arguments
// prints and which needs no --version), exactly as it does when there is no registry. That fallback is deliberate:
// it is the only reference that is known to be the right chart.
func (s Settings) OperatorChartFile() string { return chart.RegionalOperator.Filename() }

func (s Settings) OperatorRef(img kernel.ImageConfig) string {
	return s.siblingRef(img, "ikhnos-regional-operator")
}

// ControllerRef is the controller chart's address, by the same rules as OperatorRef: published next to the agent chart under its own name
// (oci://<registry>/ikhnos-controller), or derived from an explicit --chart-ref that names the agent chart. Unlike the other two charts
// it is not carried in this binary, so with no registry to name there is no reference at all: "" means the controller cannot be
// installed from here.
func (s Settings) ControllerRef(img kernel.ImageConfig) string {
	return s.siblingRef(img, "ikhnos-controller")
}

// siblingRef is where a chart published next to the agent chart under name is.
func (s Settings) siblingRef(img kernel.ImageConfig, name string) string {
	switch {
	case s.ChartRef == "local":
		return ""
	case s.ChartRef != "":
		ref := strings.TrimSpace(s.ChartRef)
		if strings.HasSuffix(ref, ".tgz") {
			return "" // a packaged agent chart: the other's is the file this server serves, or none
		}
		if i := strings.LastIndexByte(ref, '/'); ref[i+1:] == "ikhnos-agent" {
			return ref[:i+1] + name
		}
		return "" // not recognisably the agent chart's address: serve the file instead of guessing
	case img.Configured():
		return "oci://" + OCIBase(img.Registry) + "/" + name
	}
	return ""
}

// OCIBase turns an image registry setting into where an OCI registry keeps things under it. A bare name
// ("myteam") is a Docker Hub namespace, and Docker Hub serves Helm charts from registry-1.docker.io.
func OCIBase(registry string) string {
	r := strings.Trim(strings.TrimSpace(registry), "/")
	for _, p := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		if rest, ok := strings.CutPrefix(r, p); ok {
			return "registry-1.docker.io/" + rest
		}
	}
	first, _, _ := strings.Cut(r, "/")
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return r
	}
	return "registry-1.docker.io/" + r
}

// AgentVersion is what a `--version` flag against an OCI/registry chart reference should say: the version the
// release pipeline actually published this build's chart under, when the binary was linked with that information,
// else chart.Version() (the chart's own Chart.yaml literal) as the best available guess. The two agree for a chart
// packaged straight from a checkout with no `helm package --version` override (scripts/publish.sh does this); they
// can disagree for a CI-published chart, whose OCI version is a separate, deliberately-overridden string (edge:
// 0.0.0-edge.<sha>; a tagged release: the tag) that Chart.yaml's checked-in text was never meant to track.
func (s Settings) AgentVersion() string {
	if s.AgentChartVersion != "" {
		return s.AgentChartVersion
	}
	return chart.Agent.Version()
}

// OperatorVersion is AgentVersion's own twin for the regional-operator chart.
func (s Settings) OperatorVersion() string {
	if s.OperatorChartVersion != "" {
		return s.OperatorChartVersion
	}
	return chart.RegionalOperator.Version()
}

// AgentChart is where the agent chart is, as the commands that install and upgrade it name it: the chart reference and, for one that
// is a repository holding many versions (not a packaged file, which is one), the version to fetch.
func (s Settings) AgentChart(img kernel.ImageConfig) (ref, version string) {
	ref = s.AgentRef(img)
	if ref == "" {
		return "./" + chart.Filename(), "" // the file the wizard offers to download
	}
	if !strings.HasSuffix(ref, ".tgz") {
		version = s.AgentVersion()
	}
	return ref, version
}

// imageDefaults is what the server's flags (or environment) say, used when an organisation has set nothing.

// OperatorChart is where the regional-operator chart is, as AgentChart is for the agent's: the chart reference and, for a repository
// (not a packaged file), the version to fetch.
func (s Settings) OperatorChart(img kernel.ImageConfig) (ref, version string) {
	ref = s.OperatorRef(img)
	if ref == "" {
		return "./" + chart.RegionalOperator.Filename(), ""
	}
	if !strings.HasSuffix(ref, ".tgz") {
		version = s.OperatorVersion()
	}
	return ref, version
}

// ControllerChart is where the controller chart is and the version to fetch (empty when not known), or "" for the reference when this
// server cannot name it.
func (s Settings) ControllerChart(img kernel.ImageConfig) (ref, version string) {
	return s.ControllerRef(img), s.ControllerChartVersion
}

// publicBase is PublicURL cleaned up for use as a prefix ("" when it is not set, or is not an http(s) URL with a host).
func (s Settings) PublicBase() string {
	u, err := url.Parse(strings.TrimSpace(s.PublicURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
}

// ConfigError is a command this server cannot write, because of how it was set up (its chart reference or version), not because of
// anything in the request: logged in full, and told to the caller without the setting.
func ConfigError(log *slog.Logger, err error) error {
	log.Error("an install command cannot be written: check --chart-ref and the chart versions", "err", err)
	return kernel.Errf(kernel.KindInternal, "this server is set up with a chart it cannot name in an install command; see its log")
}
