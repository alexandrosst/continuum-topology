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

// The prober gets a secret of its own: the agent will not answer it with the flow secret, and says so at start-up.
func TestServingPeersNeedsTheirOwnSecretWhichIsNotTheFlowSecret(t *testing.T) {
	dir := t.TempDir()
	flow := filepath.Join(dir, "flow")
	os.WriteFile(flow, []byte("0123456789abcdef0123\n"), 0o600)
	same := filepath.Join(dir, "same")
	os.WriteFile(same, []byte("0123456789abcdef0123"), 0o600)
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc"), 0o600)
	base := []string{"--server", "s.example:8443", "--ca-pin", "ab", "--probe-listen", "127.0.0.1:0", "--flow-secret-file", flow, "--link-metrics-listen", "127.0.0.1:0"}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no peers secret":      {[]string{"--link-serve-peers"}, "--link-serve-peers needs --link-peers-secret-file"},
		"secret without serve": {[]string{"--link-peers-secret-file", same}, "--link-peers-secret-file needs --link-serve-peers"},
		"short":                {[]string{"--link-serve-peers", "--link-peers-secret-file", short}, "at least 16 characters"},
		"the flow secret":      {[]string{"--link-serve-peers", "--link-peers-secret-file", same}, "must not hold the flow secret"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		code := Main(append(append([]string{}, base...), c.args...))
		w.Close()
		os.Stderr = old
		out, _ := io.ReadAll(r)
		if code == 0 || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: code %d, output:\n%s", name, code, out)
		}
	}
}

// The issued iperf3 credentials are handed out only with a request secret of their own, which is neither the prober's nor the flow
// secret: the prober, which holds the second, must not be able to ask for the private key.
func TestServingIperfCredentialsNeedsASecretOfItsOwn(t *testing.T) {
	dir := t.TempDir()
	flow := filepath.Join(dir, "flow")
	os.WriteFile(flow, []byte("0123456789abcdef0123\n"), 0o600)
	peers := filepath.Join(dir, "peers")
	os.WriteFile(peers, []byte("peers-secret-abcdef0123"), 0o600)
	samePeers := filepath.Join(dir, "same-peers")
	os.WriteFile(samePeers, []byte("peers-secret-abcdef0123\n"), 0o600)
	sameFlow := filepath.Join(dir, "same-flow")
	os.WriteFile(sameFlow, []byte("0123456789abcdef0123"), 0o600)
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc"), 0o600)
	base := []string{"--server", "s.example:8443", "--ca-pin", "ab", "--probe-listen", "127.0.0.1:0", "--flow-secret-file", flow, "--link-metrics-listen", "127.0.0.1:0", "--link-serve-peers", "--link-peers-secret-file", peers}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no secret":            {[]string{"--link-serve-iperf"}, "--link-serve-iperf needs --link-iperf-secret-file"},
		"secret without serve": {[]string{"--link-iperf-secret-file", samePeers}, "--link-iperf-secret-file needs --link-serve-iperf"},
		"short":                {[]string{"--link-serve-iperf", "--link-iperf-secret-file", short}, "at least 16 characters"},
		"the peers secret":     {[]string{"--link-serve-iperf", "--link-iperf-secret-file", samePeers}, "must differ from --link-peers-secret-file"},
		"the flow secret":      {[]string{"--link-serve-iperf", "--link-iperf-secret-file", sameFlow}, "must not hold the flow secret"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = w
		code := Main(append(append([]string{}, base...), c.args...))
		w.Close()
		os.Stderr = old
		out, _ := io.ReadAll(r)
		if code == 0 || !strings.Contains(string(out), c.want) {
			t.Errorf("%s: code %d, output:\n%s", name, code, out)
		}
	}
	// Without the peers being served there is no peer list to carry the prober's credentials.
	noPeers := []string{"--server", "s.example:8443", "--ca-pin", "ab", "--probe-listen", "127.0.0.1:0", "--flow-secret-file", flow, "--link-metrics-listen", "127.0.0.1:0", "--link-serve-iperf", "--link-iperf-secret-file", peers}
	r, w, _ := os.Pipe()
	old := os.Stderr
	os.Stderr = w
	code := Main(noPeers)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	if code == 0 || !strings.Contains(string(out), "--link-serve-iperf needs --link-serve-peers") {
		t.Errorf("iperf without peers: code %d, output:\n%s", code, out)
	}
}
