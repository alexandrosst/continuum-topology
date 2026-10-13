package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func capArgs(extra ...string) []string {
	return probeArgs(append([]string{"telemetry.clusterLinks.probe.capacity.enabled=true"}, extra...)...)
}

func containerNamed(t *testing.T, cs []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no container %q", name)
	return corev1.Container{}
}

func proberEnv(t *testing.T, r rendered) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range containerNamed(t, r.deployments["continuum-link-prober"].Spec.Template.Spec.Containers, "prober").Env {
		out[e.Name] = e.Value
	}
	return out
}

func TestThroughputTestsAreOffByDefault(t *testing.T) {
	r := render(t, probeArgs()...)
	rs := r.daemonsets["continuum-link-responder"].Spec.Template.Spec
	if len(rs.Containers) != 1 {
		t.Errorf("the responder runs %d containers with the throughput tests off", len(rs.Containers))
	}
	for k := range proberEnv(t, r) {
		if strings.HasPrefix(k, "CONTINUUM_LINK_CAPACITY") {
			t.Errorf("the prober is told %s with the tests off", k)
		}
	}
}

func TestTheServerRunsBesideOwampWithTheSameRestrictions(t *testing.T) {
	r := render(t, capArgs()...)
	rs := r.daemonsets["continuum-link-responder"].Spec.Template.Spec
	if len(rs.Containers) != 2 {
		t.Fatalf("%d containers", len(rs.Containers))
	}
	c := containerNamed(t, rs.Containers, "iperf3")
	if got := strings.Join(c.Command, " ") + " " + strings.Join(c.Args, " "); got != "/usr/local/bin/iperf3 -s -p 8862 --idle-timeout 60" {
		t.Errorf("command = %q", got)
	}
	sc := c.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation || sc.Capabilities.Drop[0] != "ALL" || len(sc.Capabilities.Add) != 0 {
		t.Errorf("security context = %+v", sc)
	}
	if !rs.HostNetwork || len(c.Ports) != 1 || c.Ports[0].ContainerPort != 8862 {
		t.Errorf("hostNetwork %v ports %+v", rs.HostNetwork, c.Ports)
	}
	if len(c.VolumeMounts) != 0 {
		t.Errorf("an open server mounts %+v", c.VolumeMounts)
	}
	for _, v := range rs.Volumes {
		if v.Name == "iperf-auth" {
			t.Error("credentials mounted without being configured")
		}
	}
	env := proberEnv(t, r)
	for k, want := range map[string]string{
		"CONTINUUM_LINK_CAPACITY": "true", "CONTINUUM_LINK_CAPACITY_PORT": "8862", "CONTINUUM_LINK_CAPACITY_INTERVAL": "12h",
		"CONTINUUM_LINK_CAPACITY_DURATION": "10s", "CONTINUUM_LINK_CAPACITY_OMIT": "2s", "CONTINUUM_LINK_CAPACITY_PARALLEL": "4",
		"CONTINUUM_LINK_CAPACITY_REVERSE": "true", "CONTINUUM_LINK_CAPACITY_BIDIR": "false", "CONTINUUM_LINK_CAPACITY_UDP_MAX": "200M",
		"CONTINUUM_LINK_CAPACITY_UDP_START": "5M", "CONTINUUM_LINK_CAPACITY_UDP_STEP": "5s", "CONTINUUM_LINK_CAPACITY_UDP_LOSS": "1",
		"CONTINUUM_LINK_CAPACITY_DSCP": "0", "CONTINUUM_LINK_CAPACITY_TCP_MAX_RATE": "0", "CONTINUUM_LINK_CAPACITY_BUDGET": "10Gi",
		"CONTINUUM_LINK_CAPACITY_BUSY": "0.3", "CONTINUUM_LINK_CAPACITY_SKIP_ABOVE": "0", "CONTINUUM_LINK_CAPACITY_WINDOW": "",
	} {
		if got, ok := env[k]; !ok || got != want {
			t.Errorf("%s = %q (set %v), want %q", k, got, ok, want)
		}
	}
	if _, ok := env["CONTINUUM_LINK_CAPACITY_AUTH_USER"]; ok {
		t.Error("authentication is set without a secret")
	}
}

func TestEverySettingReachesTheProberAndTheServer(t *testing.T) {
	r := render(t, capArgs(
		"telemetry.clusterLinks.probe.capacity.port=9000", "telemetry.clusterLinks.probe.capacity.interval=6h",
		"telemetry.clusterLinks.probe.capacity.dscp={0,46}", "telemetry.clusterLinks.probe.capacity.window=02:00-06:00",
		"telemetry.clusterLinks.probe.capacity.server.bitrateLimit=10G", "telemetry.clusterLinks.probe.capacity.server.idleTimeout=30",
		"telemetry.clusterLinks.probe.capacity.udp.lossPercent=0.5", "telemetry.clusterLinks.probe.capacity.busyFraction=0",
	)...)
	env := proberEnv(t, r)
	for k, want := range map[string]string{
		"CONTINUUM_LINK_CAPACITY_PORT": "9000", "CONTINUUM_LINK_CAPACITY_INTERVAL": "6h", "CONTINUUM_LINK_CAPACITY_DSCP": "0,46",
		"CONTINUUM_LINK_CAPACITY_WINDOW": "02:00-06:00", "CONTINUUM_LINK_CAPACITY_UDP_LOSS": "0.5", "CONTINUUM_LINK_CAPACITY_BUSY": "0",
	} {
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
	c := containerNamed(t, r.daemonsets["continuum-link-responder"].Spec.Template.Spec.Containers, "iperf3")
	if got := strings.Join(c.Args, " "); got != "-s -p 9000 --idle-timeout 30 --server-bitrate-limit 10G" {
		t.Errorf("args = %q", got)
	}
}

func TestZeroMeansOffWhetherYouTypeItAsANumberOrAString(t *testing.T) {
	for _, v := range []string{
		"telemetry.clusterLinks.probe.capacity.budget=0", "telemetry.clusterLinks.probe.capacity.udp.maxRate=0",
		"telemetry.clusterLinks.probe.capacity.omit=0", "telemetry.clusterLinks.probe.capacity.tcpMaxRate=0",
		"telemetry.clusterLinks.probe.capacity.skipAbove=0", "telemetry.clusterLinks.probe.capacity.parallel=0",
	} {
		if out, err := helmTemplate(t, capArgs(v)...); err != nil {
			t.Errorf("%s refused: %v\n%s", v, err, out)
		}
	}
	env := proberEnv(t, render(t, capArgs("telemetry.clusterLinks.probe.capacity.budget=0", "telemetry.clusterLinks.probe.capacity.udp.maxRate=0")...))
	if env["CONTINUUM_LINK_CAPACITY_BUDGET"] != "0" || env["CONTINUUM_LINK_CAPACITY_UDP_MAX"] != "0" {
		t.Errorf("%q %q", env["CONTINUUM_LINK_CAPACITY_BUDGET"], env["CONTINUUM_LINK_CAPACITY_UDP_MAX"])
	}
}

func TestTheCredentialsAreSplitBetweenServerAndClient(t *testing.T) {
	r := render(t, capArgs("telemetry.clusterLinks.probe.capacity.auth.existingSecret=iperf-keys")...)

	rs := r.daemonsets["continuum-link-responder"].Spec.Template.Spec
	srv := containerNamed(t, rs.Containers, "iperf3")
	if got := strings.Join(srv.Args, " "); !strings.Contains(got, "--rsa-private-key-path /iperf-auth/private.pem --authorized-users-path /iperf-auth/users.csv") {
		t.Errorf("server args = %q", got)
	}
	if rs.SecurityContext.FSGroup == nil {
		t.Error("the server cannot read the credentials as a non-root user without an fsGroup")
	}
	items := func(vs []corev1.Volume) string {
		for _, v := range vs {
			if v.Name == "iperf-auth" {
				if v.Secret == nil || v.Secret.SecretName != "iperf-keys" {
					t.Fatalf("volume = %+v", v)
				}
				var keys []string
				for _, it := range v.Secret.Items {
					keys = append(keys, it.Key)
				}
				return strings.Join(keys, ",")
			}
		}
		t.Fatal("no iperf-auth volume")
		return ""
	}
	if got := items(rs.Volumes); got != "private.pem,users.csv" {
		t.Errorf("the server gets %q: it needs the private key and the user list, and no password", got)
	}

	ps := r.deployments["continuum-link-prober"].Spec.Template.Spec
	if got := items(ps.Volumes); got != "public.pem,password" {
		t.Errorf("the prober gets %q: it must never hold the private key", got)
	}
	env := map[string]corev1.EnvVar{}
	for _, e := range containerNamed(t, ps.Containers, "prober").Env {
		env[e.Name] = e
	}
	u := env["CONTINUUM_LINK_CAPACITY_AUTH_USER"]
	if u.ValueFrom == nil || u.ValueFrom.SecretKeyRef == nil || u.ValueFrom.SecretKeyRef.Name != "iperf-keys" || u.ValueFrom.SecretKeyRef.Key != "username" {
		t.Errorf("user = %+v", u)
	}
	if env["CONTINUUM_LINK_CAPACITY_AUTH_KEY_FILE"].Value != "/iperf-auth/public.pem" || env["CONTINUUM_LINK_CAPACITY_AUTH_PASSWORD_FILE"].Value != "/iperf-auth/password" {
		t.Errorf("files = %+v / %+v", env["CONTINUUM_LINK_CAPACITY_AUTH_KEY_FILE"], env["CONTINUUM_LINK_CAPACITY_AUTH_PASSWORD_FILE"])
	}
	// OWAMP's own container is untouched by it.
	owamp := containerNamed(t, rs.Containers, "owampd")
	if len(owamp.VolumeMounts) != 3 {
		t.Errorf("owampd mounts %+v", owamp.VolumeMounts)
	}
}

func TestSettingsThatCannotWorkAreRefusedAtRenderTime(t *testing.T) {
	for name, args := range map[string][]string{
		"same port as owamp":     capArgs("telemetry.clusterLinks.probe.capacity.port=8861"),
		"privileged port":        capArgs("telemetry.clusterLinks.probe.capacity.port=862"),
		"allow-list but no auth": capArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24"),
		"bad budget":             capArgs("telemetry.clusterLinks.probe.capacity.budget=lots"),
		"bad rate":               capArgs("telemetry.clusterLinks.probe.capacity.udp.maxRate=fast"),
		"bad window":             capArgs("telemetry.clusterLinks.probe.capacity.window=night"),
		"repeated dscp":          capArgs("telemetry.clusterLinks.probe.capacity.dscp={8,8}"),
		"dscp range":             capArgs("telemetry.clusterLinks.probe.capacity.dscp={64}"),
		"too many classes":       capArgs("telemetry.clusterLinks.probe.capacity.dscp={1,2,3,4,5}"),
		"loss over half":         capArgs("telemetry.clusterLinks.probe.capacity.udp.lossPercent=60"),
		"busy over one":          capArgs("telemetry.clusterLinks.probe.capacity.busyFraction=2"),
		"unknown key":            capArgs("telemetry.clusterLinks.probe.capacity.speed=fast"),
	} {
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
	// An allow-list is fine once the tests authenticate.
	if out, err := helmTemplate(t, capArgs("telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24", "telemetry.clusterLinks.probe.capacity.auth.existingSecret=k")...); err != nil {
		t.Errorf("allow-list with auth refused: %v\n%s", err, out)
	}
}

func TestTheResponderRestartsWhenTheServerSettingsChange(t *testing.T) {
	sum := func(extra ...string) string {
		return render(t, capArgs(extra...)...).daemonsets["continuum-link-responder"].Spec.Template.Annotations["checksum/config"]
	}
	base := sum()
	if base == sum("telemetry.clusterLinks.probe.capacity.server.bitrateLimit=5G") || base == sum("telemetry.clusterLinks.probe.capacity.auth.existingSecret=k") {
		t.Error("a changed server setting did not change the pod's checksum")
	}
}
