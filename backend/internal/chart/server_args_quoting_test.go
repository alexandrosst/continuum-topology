package chart

import "testing"

// Every free-text value the server chart puts into the container args is quoted. Unquoted, `org: "Acme: Lab"` renders
// `- --org=Acme: Lab`, which YAML reads as a one-entry map (the API server rejects the Deployment), and `Acme #1`
// renders `- --org=Acme #1`, which YAML reads as `--org=Acme` plus a comment (the server silently starts with another org).
func TestServerArgsKeepFreeTextValuesIntact(t *testing.T) {
	const tricky = "A: b # c"
	flags := map[string]string{
		"org":                          "org",
		"agentInstall.imageRegistry":   "image-registry",
		"agentInstall.imageTag":        "image-tag",
		"agentInstall.chartRef":        "chart-ref",
		"geoip.path":                   "geoip-db",
		"geoip.publicIpService":        "geoip-public-ip-service",
		"geoip.asnPath":                "geoip-asn-db",
		"admin.sso.headerName":         "admin-sso-header",
		"fusionControl.centralAddress": "fusion-central-address",
	}
	extra := []string{"--set", "fusion.enabled=true"}
	for key := range flags {
		extra = append(extra, "--set-string", key+"="+tricky)
	}
	args, _ := serverContainer(t, serverRenderWith(t, "ikhnos", extra...))
	have := map[string]bool{}
	for _, a := range args {
		have[a] = true
	}
	for key, flag := range flags {
		if want := "--" + flag + "=" + tricky; !have[want] {
			t.Errorf("%s=%q: no container arg %q in %q", key, tricky, want, args)
		}
	}
}
