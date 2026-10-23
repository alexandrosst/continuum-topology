package chart

import (
	"strings"
	"testing"
)

// The server's chart runs a node probe on every node by default, so the server learns its own cluster's node addresses with
// no agent enrolled there. It reports over the cluster network to a receiver in the server, signed with a secret the chart
// generates; the DaemonSet is the one place in the server's install that asks for hostNetwork, and nothing else.

func TestTheServerChartRunsNodeProbesThatReportToTheServer(t *testing.T) {
	r := serverRenderWith(t, "ik")
	ds, ok := r.daemonsets["ik-ikhnos-server-probe"]
	if !ok {
		t.Fatalf("no node probe DaemonSet: %v", r.daemonsets)
	}
	spec := ds.Spec.Template.Spec
	if !spec.HostNetwork {
		t.Fatal("without hostNetwork the probe sees only its own pod's interfaces")
	}
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("the probe never talks to the Kubernetes API, so it gets no token")
	}
	for _, v := range spec.Volumes {
		if v.HostPath != nil {
			t.Fatalf("addresses need no host files, but the probe mounts %v", v.HostPath.Path)
		}
	}
	c := spec.Containers[0]
	if c.SecurityContext == nil || c.SecurityContext.Capabilities == nil || len(c.SecurityContext.Capabilities.Drop) == 0 || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("the probe has no capabilities and cannot escalate: %+v", c.SecurityContext)
	}
	url := ""
	for _, e := range c.Env {
		if e.Name == "IKHNOS_PROBE_URL" {
			url = e.Value
		}
	}
	if url != "http://ik-ikhnos-server-probe.ikhnos.svc:8083" {
		t.Fatalf("probe URL: %q", url)
	}
	if svc, ok := r.services["ik-ikhnos-server-probe"]; !ok || svc.Spec.Ports[0].Port != 8083 {
		t.Fatalf("the Service the probes report to: %+v", r.services)
	}

	srv := r.deployments["ik-ikhnos-server"].Spec.Template
	args := strings.Join(srv.Spec.Containers[0].Args, " ")
	for _, want := range []string{"--probe-listen=:8083", "--probe-secret-file=/etc/ikhnos/probe/secret", "--probe-interval=3m"} {
		if !strings.Contains(args, want) {
			t.Fatalf("the server is not told %q: %s", want, args)
		}
	}
	if srv.Annotations["checksum/node-probe-secret"] == "" {
		t.Fatal("a new secret must restart the server, which reads it at start")
	}
}

func TestTheServerChartRunsNoNodeProbesWhenTheyAreSwitchedOff(t *testing.T) {
	r := serverRenderWith(t, "ik", "--set", "nodeProbe.enabled=false")
	if len(r.daemonsets) != 0 {
		t.Fatalf("DaemonSets: %v", r.daemonsets)
	}
	if _, ok := r.services["ik-ikhnos-server-probe"]; ok {
		t.Fatal("the probe Service is left behind")
	}
	args := strings.Join(r.deployments["ik-ikhnos-server"].Spec.Template.Spec.Containers[0].Args, " ")
	if strings.Contains(args, "--probe-") {
		t.Fatalf("the server listens for probes that do not exist: %s", args)
	}
}
