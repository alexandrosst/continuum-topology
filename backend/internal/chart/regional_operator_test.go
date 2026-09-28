package chart

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegionalOperatorPackageIsAChartHelmAccepts(t *testing.T) {
	b, err := RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := RegionalOperator.Package()
	if !bytes.Equal(b, b2) {
		t.Fatal("packaging must give the same bytes every time")
	}
	dir := t.TempDir()
	names := unpack(t, b, dir)
	want := map[string]bool{
		"continuum-regional-operator/Chart.yaml": false, "continuum-regional-operator/values.yaml": false,
		"continuum-regional-operator/templates/_helpers.tpl": false, "continuum-regional-operator/templates/deployment.yaml": false,
		"continuum-regional-operator/templates/config.yaml": false, "continuum-regional-operator/templates/serviceaccount.yaml": false,
		"continuum-regional-operator/values.schema.json": false, "continuum-regional-operator/templates/NOTES.txt": false,
	}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
		if !strings.HasPrefix(n, "continuum-regional-operator/") {
			t.Fatalf("entry outside the chart directory: %s", n)
		}
	}
	for n, ok := range want {
		if !ok {
			t.Fatalf("%s is missing from the package", n)
		}
	}
	if RegionalOperator.Filename() != "continuum-regional-operator-"+RegionalOperator.Version()+".tgz" || RegionalOperator.Version() == "0.0.0" {
		t.Fatalf("filename/version wrong: %s %s", RegionalOperator.Filename(), RegionalOperator.Version())
	}
	// The agent chart must be entirely unaffected by the regional-operator chart existing alongside it.
	if Filename() == RegionalOperator.Filename() {
		t.Fatal("the two charts must not collide on filename")
	}
	if ab, err := Package(); err != nil || len(ab) == 0 {
		t.Fatalf("Agent.Package() regressed: %v", err)
	}

	if h, err := exec.LookPath("helm"); err == nil {
		tgz := filepath.Join(dir, RegionalOperator.Filename())
		os.WriteFile(tgz, b, 0o644)
		if out, err := exec.Command(h, "lint", tgz).CombinedOutput(); err != nil {
			t.Fatalf("helm lint: %v\n%s", err, out)
		}
		out, err := exec.Command(h, "template", "x", tgz, "--set", "export.otlp.endpoint=collector.example:4317").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "kind: Deployment") {
			t.Fatalf("helm template: %v\n%s", err, out)
		}
	}
}

func TestRegionalOperatorRendersNoRBAC(t *testing.T) {
	h, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is not installed")
	}
	b, err := RegionalOperator.Package()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	tgz := filepath.Join(dir, RegionalOperator.Filename())
	os.WriteFile(tgz, b, 0o644)
	out, err := exec.Command(h, "template", "x", tgz, "--set", "export.otlp.endpoint=collector.example:4317").CombinedOutput()
	if err != nil {
		t.Fatalf("helm template: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "kind: ClusterRole") || strings.Contains(string(out), "kind: ClusterRoleBinding") || strings.Contains(string(out), "kind: Role") {
		t.Fatalf("the regional operator should need zero Kubernetes API permissions, but RBAC was rendered:\n%s", out)
	}
	if !strings.Contains(string(out), "automountServiceAccountToken: false") {
		t.Fatalf("expected automountServiceAccountToken: false, got:\n%s", out)
	}
}
