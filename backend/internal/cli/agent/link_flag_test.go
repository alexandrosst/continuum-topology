package agent

import (
	"io"
	"os"
	"strings"
	"testing"
)

// The link metrics are labelled from observed traffic, so asking for them without the flow collectors' secret is a
// mistake to be told about at start-up, before anything is connected, not discovered later as an empty endpoint.
func TestLinkMetricsNeedTheFlowSecret(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := Main([]string{"--server", "s.example:8443", "--ca-pin", "ab", "--link-metrics-listen", ":0"})
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	if code == 0 {
		t.Fatal("started without a flow secret")
	}
	if !strings.Contains(string(out), "--link-metrics-listen needs --flow-secret-file") {
		t.Fatalf("the reason was not given:\n%s", out)
	}
}

func TestServingPeersNeedsTheMetricsAddress(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := Main([]string{"--server", "s.example:8443", "--ca-pin", "ab", "--link-serve-peers"})
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	if code == 0 || !strings.Contains(string(out), "--link-serve-peers needs --link-metrics-listen") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}
