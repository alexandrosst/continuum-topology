package server

import (
	"strings"
	"testing"

	"continuum/internal/kernel"
	"continuum/internal/operators"
	"continuum/internal/store"
)

// The regional operator runs the upstream collector image its chart names. A configured registry holds the
// `continuum` image, not an operator one, so the command must not point the operator's image at it.
func TestOperatorInstallCommandNeverOverridesTheImage(t *testing.T) {
	a := newAdminRig(t)
	op := store.Operator{ID: "op-abc123", Status: store.OperatorActive, ReceiverAuth: store.ReceiverAuthMTLS,
		Destination: store.Destination{Kind: store.DestinationExternal, Endpoint: "c:4317"}}
	img := kernel.ImageConfig{Registry: "ghcr.io/me", Tag: "0.2.0-dev", Digest: "sha256:" + strings.Repeat("a", 64)}
	got, _ := operatorInstall(a.a, img, "", op, operators.OperatorTLSBundle{ReceiverCertPEM: []byte("x")}, "")
	// The collector keeps the upstream image the chart names; only the cert-renew sidecar runs the continuum image.
	if strings.Contains(got, "--set image.") || strings.Contains(got, "--set global.") {
		t.Fatalf("operator install command sets the collector's image: %s", got)
	}
	for _, want := range []string{"--set renew.image.repository=ghcr.io/me/continuum", "--set renew.image.tag=0.2.0-dev", "--set renew.image.digest=sha256:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("operator install command lacks %q: %s", want, got)
		}
	}
}
