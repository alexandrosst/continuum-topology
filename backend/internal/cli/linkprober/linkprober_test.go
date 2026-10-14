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
	"continuum/internal/cli"
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
	go follow(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), srv.Client(), srv.URL, secret, peers, meter, &link.Load{})

	deadline := time.Now().Add(5 * time.Second)
	for peers.Empty() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if peers.Empty() || peers.Own() != "cl-me" {
		t.Fatalf("the prober never learned the peers: own %q, %v", peers.Own(), peers.List())
	}
}

func TestBadThroughputSettingsFailAtStartWithTheReason(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("a-secret-of-sixteen-plus-chars\n"), 0o600)
	owping := filepath.Join(dir, "owping")
	iperf := filepath.Join(dir, "iperf3")
	os.WriteFile(owping, []byte("#!/bin/sh\n"), 0o700)
	os.WriteFile(iperf, []byte("#!/bin/sh\n"), 0o700)
	base := []string{"--agent", "http://a:9104", "--secret-file", good, "--owping", owping, "--capacity", "--iperf3", iperf}
	for name, c := range map[string]struct {
		extra []string
		want  string
	}{
		"missing iperf3":    {[]string{"--iperf3", filepath.Join(dir, "nope")}, "--iperf3"},
		"too often":         {[]string{"--capacity-interval", "1m"}, "more often than every 10 minutes"},
		"one stream":        {[]string{"--capacity-parallel", "1"}, "parallel streams"},
		"bad rate":          {[]string{"--capacity-udp-max", "fast"}, "--capacity-udp-max"},
		"bad budget":        {[]string{"--capacity-budget", "lots"}, "--capacity-budget"},
		"bad window":        {[]string{"--capacity-window", "night"}, "--capacity-window"},
		"bad dscp":          {[]string{"--capacity-dscp", "af41"}, "--capacity-dscp"},
		"dscp out of range": {[]string{"--capacity-dscp", "99"}, "DSCP"},
		"half auth":         {[]string{"--capacity-auth-user", "u"}, "authenticated mode needs"},
		"missing password":  {[]string{"--capacity-auth-user", "u", "--capacity-auth-key-file", good, "--capacity-auth-password-file", filepath.Join(dir, "nope")}, "password file"},
	} {
		code, out := run(t, append(append([]string{}, base...), c.extra...)...)
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("%s: code %d, output:\n%s", name, code, out)
		}
	}
}

func TestTheDefaultsMakeAValidScheduler(t *testing.T) {
	dir := t.TempDir()
	iperf := filepath.Join(dir, "iperf3")
	os.WriteFile(iperf, []byte("#!/bin/sh\n"), 0o700)
	fs := cli.NewFlagSet("t", io.Discard)
	f := addCapacityFlags(fs)
	if err := fs.Parse([]string{"--iperf3", iperf, "--capacity-dscp", "0, 46", "--capacity-window", "02:00-06:00", "--capacity-skip-above", "500M"}); err != nil {
		t.Fatal(err)
	}
	c, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if c.Every != 12*time.Hour || c.Duration != 10*time.Second || c.Parallel != 4 || !c.UDP.Enabled || c.UDP.Max != 200e6 || c.UDP.Loss != 0.01 ||
		c.Budget != 10<<30 || len(c.DSCP) != 2 || c.DSCP[1] != 46 || !c.Window.Set || c.SkipAbove != 500e6 || c.BusyFraction != 0.3 || c.Port != 8862 {
		t.Errorf("%+v", c)
	}
	fs2 := cli.NewFlagSet("t", io.Discard)
	f2 := addCapacityFlags(fs2)
	_ = fs2.Parse([]string{"--iperf3", iperf, "--capacity-udp-max", "0"})
	if c2, err := f2.build(); err != nil || c2.UDP.Enabled {
		t.Errorf("a ramp of 0 must turn the ramp off: %+v %v", c2, err)
	}
}

func TestBadBaselineSettingsFailAtStartWithTheReason(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	os.WriteFile(good, []byte("a-secret-of-sixteen-plus-chars\n"), 0o600)
	owping := filepath.Join(dir, "owping")
	os.WriteFile(owping, []byte("#!/bin/sh\n"), 0o700)
	base := []string{"--agent", "http://a:9104", "--secret-file", good, "--owping", owping}
	for name, c := range map[string]struct {
		extra []string
		want  string
	}{
		"connect too often": {[]string{"--tcp-interval", "1s"}, "more often than every 5s"},
		"connect timeout":   {[]string{"--tcp-interval", "10s", "--tcp-timeout", "20s"}, "shorter than the interval"},
	} {
		code, out := run(t, append(append([]string{}, base...), c.extra...)...)
		if code == 0 || !strings.Contains(out, c.want) {
			t.Errorf("%s: code %d, output:\n%s", name, code, out)
		}
	}
}

func TestConnectTimingIsOnByDefaultAndOffAtZero(t *testing.T) {
	fs := cli.NewFlagSet("t", io.Discard)
	f := addBaselineFlags(fs)
	_ = fs.Parse(nil)
	m := link.NewMeter(link.NewPeers())
	if c, err := f.connect(m, 8861); err != nil || c == nil || c.Every != 15*time.Second {
		t.Fatalf("connect timing is on by default: %+v %v", c, err)
	}
	fs2 := cli.NewFlagSet("t", io.Discard)
	f2 := addBaselineFlags(fs2)
	_ = fs2.Parse([]string{"--tcp-interval", "0"})
	if c, err := f2.connect(m, 8861); err != nil || c != nil {
		t.Fatalf("an interval of 0 turns connect timing off: %+v %v", c, err)
	}
}
