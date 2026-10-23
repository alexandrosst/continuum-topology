package chart

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Some tests need a program or a service that a developer's machine may not have: helm renders every chart and install command the
// tests pin, the real collector validates what the chart renders, and a real Neo4j answers the graph's tests. Each skips itself when
// its tool is absent, which is right on a laptop and wrong in CI, where a skip is a green check for something that was not run.
// IKHNOS_REQUIRE_TOOLS=1 (set by CI) turns the absence of any of them into a failure of this test, which names what is missing.

// podIdentityDir is where the Kubernetes client, and the collector's kubelet receiver with it, looks for the pod's own credentials.
const podIdentityDir = "/var/run/secrets/kubernetes.io/serviceaccount"

func toolsRequired() bool { return os.Getenv("IKHNOS_REQUIRE_TOOLS") == "1" }

// missingTools names what the skipped tests need and the machine does not have. Everything it looks at is passed in, so a test can
// give it a machine that has everything and one that has nothing.
func missingTools(getenv func(string) string, lookPath func(string) (string, error), stat func(string) error, reach func(url string) error) []string {
	var out []string
	if _, err := lookPath("helm"); err != nil {
		out = append(out, "helm is not on the PATH")
	}
	if bin := getenv("IKHNOS_OTELCOL"); bin == "" {
		out = append(out, "IKHNOS_OTELCOL does not name an otelcol-contrib binary")
	} else if _, err := lookPath(bin); err != nil {
		out = append(out, "IKHNOS_OTELCOL ("+bin+") is not an executable file")
	}
	for _, f := range []string{"token", "ca.crt"} {
		if err := stat(filepath.Join(podIdentityDir, f)); err != nil {
			out = append(out, filepath.Join(podIdentityDir, f)+" does not exist")
		}
	}
	if u := getenv("IKHNOS_TEST_NEO4J"); u == "" {
		out = append(out, "IKHNOS_TEST_NEO4J does not name a Neo4j")
	} else if err := reach(u); err != nil {
		out = append(out, "Neo4j at "+u+" does not answer: "+err.Error())
	}
	return out
}

func TestMissingToolsNamesEachOneThatIsAbsent(t *testing.T) {
	has := func(string) (string, error) { return "/bin/x", nil }
	none := func(string) (string, error) { return "", fmt.Errorf("not found") }
	statOK := func(string) error { return nil }
	statNo := func(string) error { return os.ErrNotExist }
	reachOK := func(string) error { return nil }
	reachNo := func(string) error { return fmt.Errorf("refused") }
	all := func(k string) string {
		return map[string]string{"IKHNOS_OTELCOL": "/opt/otelcol", "IKHNOS_TEST_NEO4J": "http://127.0.0.1:7474"}[k]
	}
	nothing := func(string) string { return "" }

	if got := missingTools(all, has, statOK, reachOK); len(got) != 0 {
		t.Errorf("a machine with everything was told it lacks %v", got)
	}
	// Five things are needed and each is named: helm, the collector, the two files of the pod identity (two lines), Neo4j.
	if got := missingTools(nothing, none, statNo, reachNo); len(got) != 5 {
		t.Errorf("a machine with nothing was told it lacks %d things, want 5: %v", len(got), got)
	}
	// Each tool taken away on its own is named, and nothing else is.
	lacks := func(tool string) func(string) (string, error) {
		return func(n string) (string, error) {
			if n == tool {
				return "", fmt.Errorf("not found")
			}
			return "/bin/x", nil
		}
	}
	for name, c := range map[string]struct {
		look  func(string) (string, error)
		stat  func(string) error
		reach func(string) error
		want  string
	}{
		"helm":      {lacks("helm"), statOK, reachOK, "helm is not on the PATH"},
		"collector": {lacks("/opt/otelcol"), statOK, reachOK, "IKHNOS_OTELCOL (/opt/otelcol)"},
		"identity":  {has, statNo, reachOK, "serviceaccount/token does not exist"},
		"neo4j":     {has, statOK, reachNo, "Neo4j at http://127.0.0.1:7474 does not answer: refused"},
	} {
		got := missingTools(all, c.look, c.stat, c.reach)
		if len(got) == 0 || !strings.Contains(got[0], c.want) {
			t.Errorf("%s: missing is %q, want it to start with %q", name, got, c.want)
		}
		if name != "identity" && len(got) != 1 {
			t.Errorf("%s: %d things reported missing, want 1: %v", name, len(got), got)
		}
	}
}

func TestTheToolsTheSkippedTestsNeedAreHereWhenCIRequiresThem(t *testing.T) {
	if !toolsRequired() {
		t.Skip("IKHNOS_REQUIRE_TOOLS is not set: a test that lacks its tool skips itself")
	}
	reach := func(u string) error {
		var err error
		for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
			var resp *http.Response
			if resp, err = http.Get(u); err == nil {
				resp.Body.Close()
				return nil
			}
		}
		return err
	}
	stat := func(p string) error { _, err := os.Stat(p); return err }
	if missing := missingTools(os.Getenv, exec.LookPath, stat, reach); len(missing) > 0 {
		t.Fatalf("IKHNOS_REQUIRE_TOOLS is set and the machine lacks:\n  %s", strings.Join(missing, "\n  "))
	}
}
