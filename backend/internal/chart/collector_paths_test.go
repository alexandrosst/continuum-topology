package chart

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The collector refuses to validate a config whose storage directories, or whose host root, are not on the machine that runs it, and
// the paths the chart renders are the pod's (/queue is a volume, /var/lib/otelcol/checkpoints is another, /hostfs is the host). A test
// machine has none of them, so a test that asks the real collector to validate a rendered config first moves them under a directory
// of its own. Without this the tests passed only on a machine that happened to have those paths, and failed on a clean one.
var collectorStorageDir = regexp.MustCompile(`(?m)^(\s*directory:\s*)(/queue|/var/lib/otelcol/checkpoints)\s*$`)

// collectorConfig is a rendered config made ready for the real collector to validate: its paths moved (collectorPaths), on a machine
// that has what the kubelet receiver reads from the pod it runs in. That one cannot be moved, because the Kubernetes client looks for
// it at a fixed place, so a machine without it skips the test, and CI (CONTINUUM_REQUIRE_TOOLS=1) fails instead of skipping.
func collectorConfig(t *testing.T, cfg string) string {
	t.Helper()
	for _, f := range []string{"token", "ca.crt"} {
		p := filepath.Join(podIdentityDir, f)
		if _, err := os.Stat(p); err != nil {
			if toolsRequired() {
				t.Fatalf("%s is missing and CONTINUUM_REQUIRE_TOOLS is set: %v", p, err)
			}
			t.Skipf("%s is missing: the collector's kubelet receiver reads the pod's service account there, and this machine has none", p)
		}
	}
	return collectorPaths(t, cfg)
}

func collectorPaths(t *testing.T, cfg string) string {
	t.Helper()
	root := t.TempDir()
	cfg = collectorStorageDir.ReplaceAllStringFunc(cfg, func(line string) string {
		m := collectorStorageDir.FindStringSubmatch(line)
		dir := filepath.Join(root, "storage", strings.Trim(strings.ReplaceAll(m[2], "/", "_"), "_"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return m[1] + dir
	})
	host := filepath.Join(root, "hostfs")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(cfg, "/hostfs", host)
}

func TestCollectorPathsMovesEveryPodPathUnderTheTestsOwnDirectory(t *testing.T) {
	in := "extensions:\n  file_storage/queue:\n    directory: /queue\n  file_storage/logs:\n    directory: /var/lib/otelcol/checkpoints\nreceivers:\n  hostmetrics:\n    root_path: /hostfs\n  journald:\n    directory: /var/log/journal\n"
	out := collectorPaths(t, in)
	for _, gone := range []string{"directory: /queue", "directory: /var/lib/otelcol/checkpoints", "root_path: /hostfs"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q is still in the config:\n%s", gone, out)
		}
	}
	// What is not a path of the pod's own volumes is left as it was, and the names of the extensions are not touched.
	for _, kept := range []string{"directory: /var/log/journal", "file_storage/queue:", "file_storage/logs:"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was changed:\n%s", kept, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "directory: "); ok && p != "/var/log/journal" {
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				t.Errorf("%s does not exist as a directory: %v", p, err)
			}
		}
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "root_path: "); ok {
			if st, err := os.Stat(p); err != nil || !st.IsDir() {
				t.Errorf("%s does not exist as a directory: %v", p, err)
			}
		}
	}
}
