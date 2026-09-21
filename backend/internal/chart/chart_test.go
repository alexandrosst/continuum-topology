package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func unpack(t *testing.T, b []byte, to string) []string {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		p := filepath.Join(to, h.Name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		c, _ := io.ReadAll(tr)
		if err := os.WriteFile(p, c, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPackageIsAChartHelmAccepts(t *testing.T) {
	b, err := Package()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := Package()
	if !bytes.Equal(b, b2) {
		t.Fatal("packaging must give the same bytes every time")
	}
	dir := t.TempDir()
	names := unpack(t, b, dir)
	want := map[string]bool{"continuum-agent/Chart.yaml": false, "continuum-agent/values.yaml": false, "continuum-agent/templates/_helpers.tpl": false, "continuum-agent/templates/rbac.yaml": false, "continuum-agent/values.schema.json": false, "continuum-agent/templates/NOTES.txt": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
		if !strings.HasPrefix(n, "continuum-agent/") {
			t.Fatalf("entry outside the chart directory: %s", n)
		}
	}
	for n, ok := range want {
		if !ok {
			t.Fatalf("%s is missing from the package", n)
		}
	}
	if Filename() != "continuum-agent-"+Version()+".tgz" || Version() == "0.0.0" {
		t.Fatalf("filename/version wrong: %s %s", Filename(), Version())
	}
	// When helm is on the machine, let it judge the archive itself.
	if h, err := exec.LookPath("helm"); err == nil {
		tgz := filepath.Join(dir, Filename())
		os.WriteFile(tgz, b, 0o644)
		if out, err := exec.Command(h, "lint", tgz).CombinedOutput(); err != nil {
			t.Fatalf("helm lint: %v\n%s", err, out)
		}
		out, err := exec.Command(h, "template", "x", tgz, "--set", "server.address=a:1", "--set", "server.caPin=ab", "--set", "enrollment.token=t").CombinedOutput()
		if err != nil || !strings.Contains(string(out), "kind: Deployment") {
			t.Fatalf("helm template: %v\n%s", err, out)
		}
	}
}
