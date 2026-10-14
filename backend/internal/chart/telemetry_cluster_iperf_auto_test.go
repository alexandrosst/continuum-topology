package chart

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The throughput tests' iperf3 credentials are issued by the Ikhnos server by default (capacity.auth.automatic). These tests pin what
// that means for the pods: who mounts which Secret, that the responder never runs an open server, and that the manual mode
// (auth.existingSecret) is untouched.

const iperfSecretName = "continuum-agent-link-iperf"

func mountedSecrets(spec corev1.PodSpec) (out []string) {
	for _, v := range spec.Volumes {
		if v.Secret != nil {
			out = append(out, v.Secret.SecretName)
		}
	}
	return out
}

func iperfPodEnv(c corev1.Container) map[string]string {
	m := map[string]string{}
	for _, e := range c.Env {
		m[e.Name] = e.Value
	}
	return m
}

func TestIssuedCredentialsReachOnlyTheAgentAndTheResponder(t *testing.T) {
	r := render(t, autoArgs()...)

	sec, ok := r.secrets[iperfSecretName]
	if !ok || len(sec.StringData["secret"]) < 32 {
		t.Fatalf("the request secret %q is missing or short", sec.StringData["secret"])
	}
	for _, other := range []string{"continuum-agent-link-peers", "continuum-agent-flows"} {
		if sec.StringData["secret"] == r.secrets[other].StringData["secret"] {
			t.Errorf("the request secret equals %s: a pod holding that one could ask for the private key", other)
		}
	}

	resp := r.daemonsets["continuum-link-responder"].Spec.Template.Spec
	if got := mountedSecrets(resp); len(got) != 1 || got[0] != iperfSecretName {
		t.Errorf("the responder mounts secrets %v, want only %s", got, iperfSecretName)
	}
	owamp := containerNamed(t, resp.Containers, "owampd")
	for _, m := range owamp.VolumeMounts {
		if m.Name == "iperf-request" || m.Name == "iperf-auth" {
			t.Errorf("the owampd container mounts %s", m.Name)
		}
	}
	srv := containerNamed(t, resp.Containers, "iperf3")
	if got := strings.Join(append(srv.Command, srv.Args...), " "); got != "link-iperf-server" {
		t.Errorf("the responder's iperf3 container runs %q: it must be the supervisor that fails closed, never iperf3 -s directly", got)
	}
	e := iperfPodEnv(srv)
	if e["CONTINUUM_LINK_IPERF_SECRET_FILE"] != "/iperf-request/secret" || e["CONTINUUM_LINK_CAPACITY_PORT"] != "8862" {
		t.Errorf("supervisor env = %v", e)
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range srv.VolumeMounts {
		mounts[m.Name] = m
	}
	if m := mounts["iperf-request"]; m.MountPath != "/iperf-request" || !m.ReadOnly {
		t.Errorf("request secret mount = %+v", m)
	}
	var mem bool
	for _, v := range resp.Volumes {
		if v.Name == "iperf-auth" {
			mem = v.EmptyDir != nil && v.EmptyDir.Medium == corev1.StorageMediumMemory
		}
	}
	if !mem {
		t.Error("the fetched key and users do not live in a memory volume")
	}
	if sc := srv.SecurityContext; sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("security context = %+v", sc)
	}

	// The prober holds the peers secret only, and gets the public side through the agent.
	pr := r.deployments["continuum-link-prober"].Spec.Template.Spec
	if got := mountedSecrets(pr); len(got) != 1 || got[0] != "continuum-agent-link-peers" {
		t.Errorf("the prober mounts secrets %v, want only continuum-agent-link-peers", got)
	}
	pe := proberEnv(t, r)
	if pe["CONTINUUM_LINK_CAPACITY_AUTH_AUTO"] != "true" || pe["CONTINUUM_LINK_CAPACITY_AUTH_KEY_DIR"] == "" {
		t.Errorf("the prober is not told to use issued credentials: %v", pe)
	}
	for _, c := range pr.Containers {
		for _, e := range c.Env {
			if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef != nil && e.ValueFrom.SecretKeyRef.Name == iperfSecretName {
				t.Errorf("the prober reads the request secret: %+v", e)
			}
			if e.Name == "CONTINUUM_LINK_IPERF_SECRET_FILE" {
				t.Errorf("the prober is told where the request secret is: %+v", e)
			}
		}
	}
	for _, v := range pr.Volumes {
		if v.Name == "iperf-keys" && (v.EmptyDir == nil || v.EmptyDir.Medium != corev1.StorageMediumMemory) {
			t.Errorf("the prober's key directory is not a memory volume: %+v", v)
		}
	}

	// The agent verifies with it and mounts it, plus the others it had.
	ag := r.deployments["continuum-agent"].Spec.Template
	ae := iperfPodEnv(ag.Spec.Containers[0])
	if ae["CONTINUUM_LINK_SERVE_IPERF"] != "true" || ae["CONTINUUM_LINK_IPERF_SECRET_FILE"] != "/link-iperf/secret" {
		t.Errorf("agent env = %v", ae)
	}
	if got := mountedSecrets(ag.Spec); len(got) != 3 {
		t.Errorf("the agent mounts secrets %v", got)
	}
	if ag.Annotations["checksum/link-iperf-secret"] == "" {
		t.Error("the agent does not roll when the request secret is replaced")
	}
	if r.daemonsets["continuum-link-responder"].Spec.Template.Annotations["checksum/config"] == r2sum(t) {
		t.Error("the responder's checksum does not tell the issued mode from the manual one")
	}

	// Nothing else in the release sees it: no collector, no other pod.
	for n, d := range r.daemonsets {
		if n == "continuum-link-responder" {
			continue
		}
		for _, s := range mountedSecrets(d.Spec.Template.Spec) {
			if s == iperfSecretName {
				t.Errorf("daemonset %s mounts the request secret", n)
			}
		}
	}
	for n, d := range r.deployments {
		if n == "continuum-agent" {
			continue
		}
		for _, s := range mountedSecrets(d.Spec.Template.Spec) {
			if s == iperfSecretName {
				t.Errorf("deployment %s mounts the request secret", n)
			}
		}
	}
}

// Where the agent has a network policy (it does when a receiver asks for one), the host-network responder's request must be allowed on 9104
// by source address, within flowObserver.nodeCIDRs.
func TestTheNetworkPolicyLetsTheResponderAskTheAgent(t *testing.T) {
	r := render(t, autoArgs("flowObserver.enabled=true", "flowObserver.networkPolicy=true", "flowObserver.nodeCIDRs={10.42.0.0/16}")...)
	p, ok := r.policies["continuum-agent-probe"]
	if !ok {
		t.Fatal("no agent network policy")
	}
	found := false
	for _, in := range p.Spec.Ingress {
		for _, f := range in.From {
			if f.IPBlock != nil && f.IPBlock.CIDR == "10.42.0.0/16" && len(in.Ports) == 1 && in.Ports[0].Port.IntValue() == 9104 {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("no ingress rule for the responder on 9104 from the node CIDRs: %+v", p.Spec.Ingress)
	}
}

// r2sum is the responder checksum with a Secret of its own named.
func r2sum(t *testing.T) string {
	return render(t, capArgs("telemetry.clusterLinks.probe.capacity.auth.existingSecret=k")...).daemonsets["continuum-link-responder"].Spec.Template.Annotations["checksum/config"]
}

func TestAnOwnSecretWinsAndLeavesTheManualModeAsItWas(t *testing.T) {
	r := render(t, autoArgs("telemetry.clusterLinks.probe.capacity.auth.existingSecret=iperf-keys")...)
	if _, ok := r.secrets[iperfSecretName]; ok {
		t.Error("a request secret is generated although the credentials are the user's own")
	}
	resp := r.daemonsets["continuum-link-responder"].Spec.Template.Spec
	srv := containerNamed(t, resp.Containers, "iperf3")
	if got := strings.Join(srv.Args, " "); !strings.Contains(got, "--rsa-private-key-path /iperf-auth/private.pem --authorized-users-path /iperf-auth/users.csv") {
		t.Errorf("args = %q", got)
	}
	if got := mountedSecrets(resp); len(got) != 1 || got[0] != "iperf-keys" {
		t.Errorf("the responder mounts %v", got)
	}
	pe := proberEnv(t, r)
	if _, ok := pe["CONTINUUM_LINK_CAPACITY_AUTH_AUTO"]; ok {
		t.Error("the prober fetches credentials although it is given a Secret")
	}
	ae := iperfPodEnv(r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0])
	if _, ok := ae["CONTINUUM_LINK_SERVE_IPERF"]; ok {
		t.Error("the agent serves issued credentials alongside a manual Secret")
	}
	// And the manual render is the same as it was before the automatic mode existed: same checksum with automatic on or off.
	off := render(t, capArgs("telemetry.clusterLinks.probe.capacity.auth.existingSecret=iperf-keys")...)
	if r.daemonsets["continuum-link-responder"].Spec.Template.Annotations["checksum/config"] != off.daemonsets["continuum-link-responder"].Spec.Template.Annotations["checksum/config"] {
		t.Error("the manual mode's pod checksum depends on the automatic setting, so upgrading would restart every responder")
	}
}

func TestAutomaticOffWithoutASecretIsTheOpenServerAsBefore(t *testing.T) {
	r := render(t, capArgs()...)
	if _, ok := r.secrets[iperfSecretName]; ok {
		t.Error("a request secret exists with automatic off")
	}
	srv := containerNamed(t, r.daemonsets["continuum-link-responder"].Spec.Template.Spec.Containers, "iperf3")
	if got := strings.Join(srv.Command, " ") + " " + strings.Join(srv.Args, " "); got != "/usr/local/bin/iperf3 -s -p 8862 --idle-timeout 60" {
		t.Errorf("command = %q", got)
	}
}

func TestAllowFromNeedsIssuedOrOwnCredentials(t *testing.T) {
	const allow = "telemetry.clusterLinks.probe.responder.allowFrom[0]=203.0.113.0/24"
	if out, err := helmTemplate(t, autoArgs(allow)...); err != nil {
		t.Errorf("allow-list with issued credentials refused: %v\n%s", err, out)
	}
	if out, err := helmTemplate(t, capArgs(allow)...); err == nil {
		t.Errorf("allow-list with an open throughput server accepted\n%s", out)
	}
	if out, err := helmTemplate(t, capArgs(allow, "telemetry.clusterLinks.probe.capacity.auth.existingSecret=k")...); err != nil {
		t.Errorf("allow-list with an own Secret refused: %v\n%s", err, out)
	}
}

func TestTheRequestSecretMustNotBeOneThePeersHold(t *testing.T) {
	const same = "0123456789012345678901234567890123456789"
	for name, args := range map[string][]string{
		"the peers secret": autoArgs("telemetry.clusterLinks.probe.peersSecret="+same, "telemetry.clusterLinks.probe.capacity.auth.requestSecret="+same),
		"the flow secret":  autoArgs("flowObserver.secret="+same, "telemetry.clusterLinks.probe.capacity.auth.requestSecret="+same),
	} {
		if out, err := helmTemplate(t, args...); err == nil {
			t.Errorf("%s: accepted\n%s", name, out)
		}
	}
	r := render(t, autoArgs("telemetry.clusterLinks.probe.capacity.auth.requestSecret=another-secret-of-at-least-32-chars")...)
	if r.secrets[iperfSecretName].StringData["secret"] != "another-secret-of-at-least-32-chars" {
		t.Error("the given request secret is not used")
	}
}

func TestNothingIsIssuedWithTheThroughputTestsOff(t *testing.T) {
	r := render(t, probeArgs()...)
	if _, ok := r.secrets[iperfSecretName]; ok {
		t.Error("a request secret exists with the throughput tests off")
	}
	ae := iperfPodEnv(r.deployments["continuum-agent"].Spec.Template.Spec.Containers[0])
	if _, ok := ae["CONTINUUM_LINK_SERVE_IPERF"]; ok {
		t.Error("the agent serves issued credentials with the throughput tests off")
	}
}
