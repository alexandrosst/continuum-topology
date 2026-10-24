package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"ikhnos/internal/pki"
)

// The policy is on unless it is switched off, and the log says which it is.
func TestTheIdentityPolicyIsOnByDefaultAndSaysWhichItIs(t *testing.T) {
	t.Cleanup(func() { pki.RequireIdentityURI = false })
	if !*requireIdentityURI {
		t.Fatal("certificates are not required to state their holder by default")
	}
	for _, require := range []bool{true, false} {
		var out bytes.Buffer
		pki.RequireIdentityURI = !require
		applyIdentityPolicy(slog.New(slog.NewTextHandler(&out, nil)), require)
		if pki.RequireIdentityURI != require {
			t.Errorf("require=%v: the policy is %v", require, pki.RequireIdentityURI)
		}
		line := out.String()
		if require && (!strings.Contains(line, "level=INFO") || !strings.Contains(line, "refused")) || !require && !strings.Contains(line, "level=WARN") {
			t.Errorf("require=%v: %s", require, line)
		}
	}
}
