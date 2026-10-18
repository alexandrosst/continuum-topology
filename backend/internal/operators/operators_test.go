package operators

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"continuum/internal/chart"
	"continuum/internal/store"
)

func TestGuardCentralStillProtectsEverythingElse(t *testing.T) {
	if err := GuardCentral(CentralOperatorID); err == nil {
		t.Fatal("the central operator can be edited by hand")
	}
}

// The Service the chart creates, and the name a sender in the same cluster dials, are one and the same: the chart's
// own naming rule (release name, plus "-regional-operator" unless it already says so), and the receiver certificate
// carries both that name and the stable one.
func TestOperatorServiceNameFollowsTheChartAndIsOnTheCertificate(t *testing.T) {
	for _, c := range []struct{ id, want string }{
		{"op-abc", "op-abc-regional-operator"},
		{"my-regional-operator", "my-regional-operator"},
		{"op-" + strings.Repeat("x", 70), ("op-" + strings.Repeat("x", 70) + "-regional-operator")[:63]},
	} {
		if got := OperatorServiceName(c.id); got != c.want {
			t.Fatalf("operatorServiceName(%q) = %q, want %q", c.id, got, c.want)
		}
	}
	op := store.Operator{ID: "op-abc"}
	if got := OperatorInClusterEndpoint(op); got != "op-abc-regional-operator.continuum-system.svc:4317" {
		t.Fatalf("in-cluster endpoint: %s", got)
	}
	if !OperatorNeedsServerName(op) || OperatorNeedsServerName(store.Operator{ID: CentralOperatorID}) || !OperatorNeedsServerName(store.Operator{ID: CentralOperatorID, Address: "a:1"}) {
		t.Fatal("which operators need a server name override")
	}
}

// What the server tells a sender to dial is the Service the chart really creates for that release: rendered here
// for the exact release name the install command uses (the operator's id), for plain and "regional-operator" names.
func TestTheDialledServiceNameIsTheOneTheChartRenders(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	pkg, err := chart.RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	tgz := filepath.Join(t.TempDir(), chart.RegionalOperator.Filename())
	if err := os.WriteFile(tgz, pkg, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"op-52c91de1fa99", "eu-regional-operator"} {
		out, err := exec.Command(h, "template", id, tgz, "--set", "export.otlp.endpoint=c:4317").CombinedOutput()
		if err != nil {
			t.Fatalf("helm template %s: %v\n%s", id, err, out)
		}
		var svc string
		for _, doc := range strings.Split(string(out), "\n---") {
			if !strings.Contains(doc, "\nkind: Service\n") {
				continue
			}
			for _, line := range strings.Split(doc, "\n") {
				if strings.HasPrefix(line, "  name: ") {
					svc = strings.TrimSpace(strings.TrimPrefix(line, "  name: "))
					break
				}
			}
		}
		if svc == "" || svc != OperatorServiceName(id) {
			t.Fatalf("release %s renders Service %q, the server dials %q", id, svc, OperatorServiceName(id))
		}
		if want := svc + ".continuum-system.svc:4317"; OperatorInClusterEndpoint(store.Operator{ID: id}) != want {
			t.Fatalf("endpoint %s, want %s", OperatorInClusterEndpoint(store.Operator{ID: id}), want)
		}
	}
}

func TestValidOperatorAddress(t *testing.T) {
	for in, want := range map[string]string{
		"":                             "",
		"  ":                           "",
		"otlp.example.com:4317":        "otlp.example.com:4317",
		"  OTLP.Example.COM.:4317":     "otlp.example.com:4317",
		"203.0.113.7:4317":             "203.0.113.7:4317",
		"[2001:db8::1]:4317":           "[2001:db8::1]:4317",
		"otlp.example.com":             "otlp.example.com:4317", // a bare host gets the receiver's own port
		"203.0.113.7":                  "203.0.113.7:4317",
		"Otlp.Example.com:+4317":       "otlp.example.com:4317", // the port is stored as the number it is
		"otlp.example.com:04317":       "otlp.example.com:4317",
		"op-1.svc.clusterset.local:80": "op-1.svc.clusterset.local:80",
	} {
		got, err := ValidOperatorAddress(in)
		if err != nil || got != want {
			t.Errorf("%q -> %q, %v (want %q)", in, got, err, want)
		}
	}
	for _, in := range []string{
		"https://otlp.example.com:4317", // a scheme
		"otlp.example.com:4317/x",       // a path
		"u:p@otlp.example.com:4317",     // credentials
		"otlp.example.com:0",
		"otlp.example.com:70000",
		"otlp.example.com:abc",
		"2001:db8::1", // an IPv6 address needs its brackets
		"otlp.example.com:",
		"127.0.0.1:4317",
		"[::1]:4317",
		"0.0.0.0:4317",
		"169.254.1.1:4317",
		"localhost:4317",
		"a b.example.com:4317",
		"-bad.example.com:4317",
		"bad_name.example.com:4317",
		strings.Repeat("a", 300) + ":4317",
	} {
		if got, err := ValidOperatorAddress(in); err == nil {
			t.Errorf("%q was accepted as %q", in, got)
		}
	}
}

// What `kubectl get svc` prints cannot be reached from another cluster, however well it is spelled.
func TestAddressRefusesNamesThatOnlyResolveInsideOneCluster(t *testing.T) {
	for _, in := range []string{"op-a.continuum-system.svc", "op-a.continuum-system.svc:4317", "op-a.continuum-system.svc.cluster.local:4317", "x.cluster.local", "OP-A.NS.SVC.", "svc"} {
		if _, err := ValidOperatorAddress(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
	for in, want := range map[string]string{"op-a.continuum-system.svc.clusterset.local": "op-a.continuum-system.svc.clusterset.local:4317", "svc.example.com:4317": "svc.example.com:4317", "mysvc.example.com": "mysvc.example.com:4317"} {
		if got, err := ValidOperatorAddress(in); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
}

func TestAuditDetailListsAreBounded(t *testing.T) {
	ids := []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"}
	if got := auditIDList(ids); got != "c1, c2, c3, c4, c5 and 2 more" {
		t.Fatalf("auditIDList = %q", got)
	}
	if got := auditIDList(ids[:2]); got != "c1, c2" {
		t.Fatalf("auditIDList = %q", got)
	}
	added, removed := idChanges([]string{"a", "b", "c"}, []string{"b", "d", "e"})
	if strings.Join(added, ",") != "d,e" || strings.Join(removed, ",") != "a,c" {
		t.Fatalf("idChanges = %v %v", added, removed)
	}
	// A hostile endpoint (or one that is merely long) is clipped, and the whole row stays within what a row may hold.
	long := store.Destination{Kind: store.DestinationExternal, Endpoint: strings.Repeat("a", 400)}
	if d := operatorAuditSummary(long, nil); len(d) > 200 || !strings.Contains(d, "0 source clusters") {
		t.Fatalf("summary = %q", d)
	}
}
