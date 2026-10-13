package linkprober

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/link"
)

func run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	code := Main(args)
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func TestStartingWithBadSettingsFailsWithTheReason(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short")
	os.WriteFile(short, []byte("abc"), 0o600)
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("a-secret-of-sixteen-plus-chars\n"), 0o600)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"nothing":         {nil, "--agent and --secret-file are required"},
		"not a url":       {[]string{"--agent", "agent:9104", "--secret-file", good}, "must be an http(s) URL"},
		"short secret":    {[]string{"--agent", "http://a:9104", "--secret-file", short}, "at least 16 characters"},
		"missing owping":  {[]string{"--agent", "http://a:9104", "--secret-file", good, "--owping", filepath.Join(dir, "nope")}, "--owping"},
		"too few packets": {[]string{"--agent", "http://a:9104", "--secret-file", good, "--packets", "2"}, "packets per test"},
		"half auth":       {[]string{"--agent", "http://a:9104", "--secret-file", good, "--auth-user", "x"}, "needs both a user and a key file"},
	} {
		code, out := run(t, c.args...)
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("%s: code %d, output:\n%s", name, code, out)
		}
	}
}

// The prober takes its peers from the agent.
func TestTheProberFollowsTheAgentsPeerList(t *testing.T) {
	secret := []byte("a-secret-of-sixteen-plus-chars")
	agentPeers := link.NewPeers()
	agentMeter := link.NewMeter(agentPeers)
	agentMeter.ServePeers(secret)
	agentPeers.Set("cl-me", []*continuumv1.LinkPeer{{ClusterId: "cl-a", Name: "A", Addresses: []string{"10.0.0.1"}}})
	srv := httptest.NewServer(agentMeter.Handler())
	defer srv.Close()

	peers := link.NewPeers()
	meter := link.NewMeter(peers)
	meter.OnlyProbes()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go follow(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), srv.Client(), srv.URL, secret, peers, meter)

	deadline := time.Now().Add(5 * time.Second)
	for peers.Empty() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if peers.Empty() || peers.Own() != "cl-me" {
		t.Fatalf("the prober never learned the peers: own %q, %v", peers.Own(), peers.List())
	}
}
