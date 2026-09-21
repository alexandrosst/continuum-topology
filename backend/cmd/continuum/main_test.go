package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestDispatch(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "usage: continuum <role>") {
		t.Fatalf("no role: code %d, stderr %q", code, errb.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "continuum "+version) {
		t.Fatalf("version: code %d, stdout %q", code, out.String())
	}
	out.Reset()
	errb.Reset()
	if code := run([]string{"help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "agent") || !strings.Contains(out.String(), "flow") || !strings.Contains(out.String(), "probe") {
		t.Fatalf("help: code %d, stdout %q", code, out.String())
	}
	errb.Reset()
	if code := run([]string{"server"}, &out, &errb); code != 2 || !strings.Contains(errb.String(), `unknown role "server"`) {
		t.Fatalf("unknown role: code %d, stderr %q", code, errb.String())
	}
}

// Each role must accept its own flags and refuse to start without what it needs, with the exit codes the separate
// binaries had: 2 for a bad flag, 1 for a missing requirement, 0 for --help.
func TestRolesKeepTheirFlagContracts(t *testing.T) {
	var out, errb bytes.Buffer
	for _, role := range []string{"agent", "probe", "flow"} {
		if code := run([]string{role, "--help"}, &out, &errb); code != 0 {
			t.Errorf("%s --help: code %d", role, code)
		}
		if code := run([]string{role, "--no-such-flag"}, &out, &errb); code != 2 {
			t.Errorf("%s --no-such-flag: code %d, want 2", role, code)
		}
	}
	t.Setenv("CONTINUUM_SERVER", "")
	t.Setenv("CONTINUUM_CA_PIN", "")
	if code := run([]string{"agent"}, &out, &errb); code != 1 {
		t.Errorf("agent without --server: code %d, want 1", code)
	}
	if code := run([]string{"probe"}, &out, &errb); code != 1 {
		t.Errorf("probe without --agent: code %d, want 1", code)
	}
}
