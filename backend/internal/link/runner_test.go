package link

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

type fakeOwping struct {
	mu    sync.Mutex
	calls [][]string
	run   func(args []string) ([]byte, []byte, error)
}

func (f *fakeOwping) exec(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string{name}, args...))
	f.mu.Unlock()
	return f.run(args)
}

func summary(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func runnerFor(t *testing.T, f *fakeOwping, addrs ...string) *Runner {
	t.Helper()
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", addrs...)})
	m := NewMeter(p)
	m.EnableProbes()
	// The real route tracer must never run in unit tests; path tests inject their own.
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: -1}
	r.Defaults()
	return r
}

func TestARoundMeasuresBothDirectionsAndRecordsTheLossOfEach(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		for _, a := range args {
			if a == "-t" {
				return summary(t, "owping_to.txt"), nil, nil
			}
		}
		return summary(t, "owping_from.txt"), nil, nil
	}
	r := runnerFor(t, f, "10.0.0.1")
	r.Round(context.Background())
	got := render(r.Meter)
	base := `{cluster="cl-me",peer="cl-a",direction="`
	if got["continuum_link_probe_packets_lost_total"+base+`forward",method="owamp"}`] != "18" ||
		got["continuum_link_probe_packets_lost_total"+base+`reverse",method="owamp"}`] != "8" {
		t.Fatalf("directions mixed up or not recorded: %v", got)
	}
	// the arguments: one test to the peer, then one from it, machine-readable, against the responder port
	if len(f.calls) != 2 {
		t.Fatalf("%d runs", len(f.calls))
	}
	joined := strings.Join(f.calls[0], " ")
	for _, want := range []string{"-M", "-t", "-A O", "10.0.0.1:861", "-c 100"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	if !strings.Contains(strings.Join(f.calls[1], " "), "-f") {
		t.Errorf("second run is not the reverse direction: %v", f.calls[1])
	}
}

func TestAnUnansweringAddressMovesOnAndTheGoodOneIsTriedFirstNextTime(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "10.0.0.2:") {
			return summary(t, "owping_to.txt"), nil, nil
		}
		return nil, []byte("owping: Unable to open control connection to 10.0.0.1:861."), errors.New("exit 1")
	}
	r := runnerFor(t, f, "10.0.0.1", "10.0.0.2")
	r.Round(context.Background())
	if !hasKey(render(r.Meter), "last_success") {
		t.Fatal("the second address answered but nothing was recorded")
	}
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
	r.Round(context.Background())
	if !strings.Contains(strings.Join(f.calls[0], " "), "10.0.0.2:") {
		t.Errorf("the address that answered was not tried first: %v", f.calls[0])
	}
}

func TestAFailureIsCountedOnceByReasonNotPerAddress(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}}
	r := runnerFor(t, f, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	r.Round(context.Background())
	got := render(r.Meter)
	k := `continuum_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="timeout"}`
	if got[k] != "1" {
		t.Fatalf("%s = %q", k, got[k])
	}
	if hasKey(got, "last_success") {
		t.Error("a failed round recorded a success")
	}
}

func TestAnAuthenticationFailureStopsTryingOtherAddresses(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return nil, []byte("owping: authentication failed"), errors.New("exit 1")
	}}
	r := runnerFor(t, f, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	r.Round(context.Background())
	if len(f.calls) != 2 { // one per direction
		t.Fatalf("%d runs; the other addresses should not have been tried", len(f.calls))
	}
	if got := render(r.Meter)[`continuum_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="auth"}`]; got != "1" {
		t.Fatalf("auth failures = %q", got)
	}
}

func TestAddressesAreCheckedBeforeTheyReachTheCommandLine(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return nil, nil, nil }}
	r := runnerFor(t, f)
	for _, bad := range []string{"-A", "10.0.0.1; rm -rf /", "0.0.0.0", "224.0.0.1", "host.example", ""} {
		if _, why := r.once(context.Background(), bad, Forward); why == "" {
			t.Errorf("%q was accepted", bad)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("owping was run for a bad address: %v", f.calls)
	}
}

func TestAuthenticatedModeUsesTheKeyAndIPv6IsBracketed(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f)
	r.User, r.KeyFile, r.DSCP = "continuum", "/etc/link/key", 46
	r.once(context.Background(), "2001:db8::1", Forward)
	joined := strings.Join(f.calls[0], " ")
	for _, want := range []string{"-A A", "-u continuum", "-k /etc/link/key", "-D 46", "[2001:db8::1]:861"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
}

func TestSettingsAreValidated(t *testing.T) {
	ok := func() *Runner { r := &Runner{}; r.Defaults(); return r }
	if err := ok().Validate(); err != nil {
		t.Fatalf("the defaults are invalid: %v", err)
	}
	for name, mut := range map[string]func(*Runner){
		"few packets":  func(r *Runner) { r.Packets = 3 },
		"many packets": func(r *Runner) { r.Packets = 100000 },
		"fast spacing": func(r *Runner) { r.Spacing = time.Millisecond },
		"often":        func(r *Runner) { r.Every = time.Second },
		"port":         func(r *Runner) { r.Port = 70000 },
		"half auth":    func(r *Runner) { r.User = "x" },
		"dscp":         func(r *Runner) { r.DSCP = 64 },
		"does not fit": func(r *Runner) { r.Packets, r.Spacing, r.Every = 1000, time.Second, time.Minute },
	} {
		r := ok()
		mut(r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// With the real OWAMP binaries (CONTINUUM_TEST_OWAMP_DIR holds bin/owampd and bin/owping) the runner measures a real
// responder over loopback. It is skipped where they are not installed.
func TestAgainstARealResponder(t *testing.T) {
	dir := os.Getenv("CONTINUUM_TEST_OWAMP_DIR")
	if dir == "" {
		t.Skip("CONTINUUM_TEST_OWAMP_DIR not set")
	}
	// Peers never include loopback, so the responder listens on one of the host's own addresses.
	host := ""
	if as, err := net.InterfaceAddrs(); err == nil {
		for _, a := range as {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && ipn.IP.IsGlobalUnicast() {
				host = ipn.IP.String()
				break
			}
		}
	}
	if host == "" {
		t.Skip("no usable IPv4 address on this host")
	}
	work := t.TempDir()
	conf := filepath.Join(work, "conf")
	for _, d := range []string{conf, filepath.Join(work, "data"), filepath.Join(work, "var")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(conf, "owamp-server.conf"), []byte("testports 18900-18999\n"), 0o644)
	os.WriteFile(filepath.Join(conf, "owamp-server.limits"), []byte("limit root with disk=0, bandwidth=0, delete_on_fetch=on\nlimit regular with parent=root, disk=10G, bandwidth=20M\nassign default regular\n"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := exec.CommandContext(ctx, filepath.Join(dir, "bin/owampd"), "-Z", "-f", "-a", "O", "-c", conf, "-d", filepath.Join(work, "data"), "-R", filepath.Join(work, "var"), "-S", host+":18861")
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); _ = srv.Wait() }()
	time.Sleep(time.Second)

	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", host)})
	m := NewMeter(p)
	m.EnableProbes()
	r := &Runner{Meter: m, Bin: filepath.Join(dir, "bin/owping"), Port: 18861, Packets: 20, Spacing: 50 * time.Millisecond}
	r.Defaults()
	r.Round(ctx)
	got := render(m)
	for _, d := range []string{"forward", "reverse"} {
		k := `continuum_link_probe_packets_sent_total{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp"}`
		if got[k] != "20" {
			t.Errorf("%s = %q; all: %v", k, got[k], got)
		}
	}
}
