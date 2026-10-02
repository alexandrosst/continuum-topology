package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// Regression guard: Ready previously carried `json:"ready,omitempty"`, which makes encoding/json drop the
// field whenever it is false - indistinguishable on the wire from a pod whose readiness was never
// collected at all. A genuinely not-ready pod must still say so explicitly.
func TestPodReadyFalseIsNotOmittedFromJSON(t *testing.T) {
	notReady := Pod{Name: "p", Phase: "Running", Ready: false}
	b, err := json.Marshal(notReady)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ready":false`) {
		t.Errorf("marshaled not-ready pod = %s, want an explicit \"ready\":false, not an omitted field", b)
	}

	ready := Pod{Name: "p", Phase: "Running", Ready: true}
	b, err = json.Marshal(ready)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ready":true`) {
		t.Errorf("marshaled ready pod = %s, want \"ready\":true", b)
	}
}

func TestTunnelEncryptionPostureClassifiesEveryDocumentedKind(t *testing.T) {
	cases := map[string]string{
		"wireguard": "encrypted",
		"vti":       "encrypted",
		"vti6":      "encrypted",
		"xfrm":      "encrypted",
		"vxlan":     "plaintext",
		"geneve":    "plaintext",
		"gre":       "plaintext",
		"gretap":    "plaintext",
		"ip6gre":    "plaintext",
		"ip6gretap": "plaintext",
		"ipip":      "plaintext",
		"sit":       "plaintext",
		"":          "unknown",
		"ppp":       "unknown", // not one of TunnelInterface.Kind's documented values - never guessed either way
	}
	for kind, want := range cases {
		if got := TunnelEncryptionPosture(kind); got != want {
			t.Errorf("TunnelEncryptionPosture(%q) = %q, want %q", kind, got, want)
		}
	}
}
