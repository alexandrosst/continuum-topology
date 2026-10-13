package agent

import (
	"io"
	"os"
	"path/filepath"
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

func TestProberPortsMustBePorts(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	secret := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(secret, []byte("0123456789abcdef0123"), 0o600)
	code := Main([]string{"--server", "s.example:8443", "--ca-pin", "ab", "--flow-secret-file", secret, "--link-metrics-listen", "127.0.0.1:0", "--link-prober-ports", "8861,eight"})
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	if code == 0 || !strings.Contains(string(out), "--link-prober-ports") {
		t.Fatalf("code %d:\n%s", code, out)
	}
}
