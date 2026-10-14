package link

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

func TestASilentPeerCostsAFewAttemptsOnceAndIsThenLeftAloneForGrowingSpells(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "10.0.1.1:") {
			return summary(t, "owping_to.txt"), nil, nil
		}
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"), peer("cl-b", "B", "10.0.1.1")})
	m := NewMeter(p)
	m.EnableProbes()
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: -1}
	r.Defaults()
	count := func(prefix string) (n int) {
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.calls {
			if strings.Contains(strings.Join(c, " "), prefix) {
				n++
			}
		}
		return
	}
	failed := func() string {
		return render(m)[`continuum_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="reverse",method="owamp",reason="timeout"}`]
	}
	r.Round(context.Background())
	// 5 addresses are not all tried, and the reverse direction is not tried at all, yet it is counted
	if got := count("10.0.0."); got != maxSlowFailures {
		t.Fatalf("%d attempts towards the silent peer in the first round, want %d", got, maxSlowFailures)
	}
	if failed() != "1" {
		t.Errorf("the reverse direction was not counted as failed: %q", failed())
	}
	// the healthy peer after it is measured every round, in both directions
	if got := count("10.0.1.1:"); got != 2 {
		t.Fatalf("%d runs towards the healthy peer", got)
	}
	r.Round(context.Background()) // second failure: one round of rest follows
	before := count("10.0.0.")
	r.Round(context.Background())
	if got := count("10.0.0."); got != before {
		t.Errorf("the silent peer was tried again during its rest (%d -> %d)", before, got)
	}
	if got := count("10.0.1.1:"); got != 6 {
		t.Errorf("the healthy peer was not measured in each round: %d runs", got)
	}
	if failed() != "3" {
		t.Errorf("a rested round must still count as failed: %q", failed())
	}
	// a peer that answers again is forgiven
	f.run = func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }
	for i := 0; i < 20; i++ {
		r.Round(context.Background())
	}
	r.mu.Lock()
	_, stillDown := r.down["cl-a"]
	r.mu.Unlock()
	if stillDown {
		t.Error("a peer that answers again is still marked down")
	}
}

func TestARefusingPeerSkipsTheOtherDirectionButAnAuthenticationFailureDoesNot(t *testing.T) {
	for msg, wantRuns := range map[string]int{"owping: connection refused": 1, "owping: authentication failed": 2} {
		f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return nil, []byte(msg), errors.New("exit 1") }}
		r := runnerFor(t, f, "10.0.0.1")
		r.Round(context.Background())
		if len(f.calls) != wantRuns {
			t.Errorf("%q: %d runs, want %d", msg, len(f.calls), wantRuns)
		}
	}
}

func TestARunThatCannotBeWhatWasAskedForIsNotRecorded(t *testing.T) {
	for name, in := range map[string]string{
		"more sent than asked": "SUMMARY\t3\nSENT\t101\nLOST\t0\n",
		"nothing sent":         "SUMMARY\t3\nSENT\t0\nLOST\t0\n",
		"more duplicates":      "SUMMARY\t3\nSENT\t100\nDUPS\t101\n",
	} {
		f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return []byte(in), nil, nil }}
		r := runnerFor(t, f, "10.0.0.1")
		if _, why := r.once(context.Background(), "10.0.0.1", Forward); why == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	if _, why := runnerFor(t, f, "10.0.0.1").once(context.Background(), "10.0.0.1", Forward); why != "" {
		t.Errorf("a real run was refused: %s", why)
	}
}

func TestTheKillDeadlineAllowsForTheRandomSpacingOfPackets(t *testing.T) {
	r := &Runner{Packets: 10, Spacing: 5 * time.Second}
	r.Defaults()
	if r.maxTime() < r.testTime()+10*time.Second+5*time.Second*10 {
		t.Errorf("a test of 10 packets 5s apart may be killed after %s", r.maxTime())
	}
	d := &Runner{}
	d.Defaults()
	if d.maxTime() < d.testTime()+10*time.Second || d.maxTime() > d.Every {
		t.Errorf("default deadline %s", d.maxTime())
	}
}

func TestThePathProbeWaitsForItsTurnAtTheGate(t *testing.T) {
	gate := NewGate()
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "10.0.0.1")
	r.Gate, r.PathEvery = gate, time.Minute
	var traced atomic.Int32
	r.Path = func(context.Context, netip.Addr) (PathResult, error) {
		traced.Add(1)
		return PathResult{}, nil
	}
	hold, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := r.Meter.Peers.List()[0]
	done := make(chan struct{})
	go func() { r.maybePath(context.Background(), p); close(done) }()
	select {
	case <-done:
		t.Fatal("the path probe ran while another measurement held the link")
	case <-time.After(50 * time.Millisecond):
	}
	if traced.Load() != 0 {
		t.Fatal("the path probe ran beside another measurement")
	}
	hold()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the path probe did not run once the link was free")
	}
	if traced.Load() != 1 {
		t.Fatalf("path probes run: %d", traced.Load())
	}
	// a wait that is cancelled is not a trace: it is tried again next time
	r.lastPath = nil
	hold, _ = gate.Acquire(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	r.maybePath(ctx, p)
	hold()
	r.mu.Lock()
	_, stamped := r.lastPath[p.ID]
	r.mu.Unlock()
	if stamped || traced.Load() != 1 {
		t.Error("a trace that never ran was recorded as done")
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

func TestTheDSCPIsSetAndIPv6IsBracketed(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f)
	r.DSCP = 46
	r.once(context.Background(), "2001:db8::1", Forward)
	joined := strings.Join(f.calls[0], " ")
	for _, want := range []string{"-A O", "-D 46", "[2001:db8::1]:861"} {
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

func TestAPairWithTheProbeSwitchedOffIsNotProbedAndHasNoProbeSeries(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	p := NewPeers()
	off := peer("cl-a", "A", "10.0.0.1")
	off.NoProbe = true
	p.Set("cl-me", []*continuumv1.LinkPeer{off, peer("cl-b", "B", "10.0.1.1")})
	m := NewMeter(p)
	m.EnableProbes()
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: -1}
	r.Defaults()
	r.Round(context.Background())
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), "10.0.0.1") {
			t.Errorf("probed a pair that has the probe off: %v", c)
		}
	}
	if len(f.calls) == 0 {
		t.Fatal("the other pair was not probed")
	}
	for k := range render(m) {
		if strings.Contains(k, "probe") && strings.Contains(k, `peer="cl-a"`) {
			t.Errorf("series for a pair that is not probed: %s", k)
		}
	}
}

func TestAPeerTheOtherClusterProbesIsOnlyTracedAndExportsNoProbeSeries(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	p := NewPeers()
	served := peer("cl-a", "A", "10.0.0.1")
	served.SkipProbe = true
	p.Set("cl-me", []*continuumv1.LinkPeer{served, peer("cl-b", "B", "10.0.1.1")})
	m := NewMeter(p)
	m.EnableProbes()
	m.EnableConnects()
	var traced []string
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: time.Hour, Path: func(_ context.Context, dst netip.Addr) (PathResult, error) {
		traced = append(traced, dst.String())
		return PathResult{MTU: 1500, Reached: true, Route: "r"}, nil
	}}
	r.Defaults()
	r.Round(context.Background())
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c, " "), "10.0.0.1") {
			t.Errorf("probed a pair the other cluster measures: %v", c)
		}
	}
	if len(f.calls) == 0 {
		t.Fatal("the pair this cluster measures was not probed")
	}
	if len(traced) != 2 {
		t.Fatalf("the route to both peers is traced from both sides (the two directions can differ), got %v", traced)
	}
	got := render(m)
	if got[`continuum_link_path_reached{cluster="cl-me",peer="cl-a"}`] != "1" {
		t.Errorf("no route series for the pair the other side probes: %v", got)
	}
	for k := range got {
		if strings.Contains(k, `peer="cl-a"`) && (strings.Contains(k, "continuum_link_probe_") || strings.Contains(k, "tcp_connect")) {
			t.Errorf("series for a measurement the other cluster makes: %s", k)
		}
	}
}

// The client is bound to the address the system reaches the responder from, in both directions: left to itself owping announces
// another address of the node as the receiver of a reverse test, and the responder refuses the test (open mode requires the
// receiver to be the control client).
func TestTheClientIsBoundToTheAddressTheResponderIsReachedFrom(t *testing.T) {
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
	r.Source = func(ip netip.Addr) (netip.Addr, bool) {
		return netip.MustParseAddr("100.64.0.45"), ip.String() == "10.0.0.1"
	}
	r.Round(context.Background())
	if len(f.calls) != 2 {
		t.Fatalf("calls: %v", f.calls)
	}
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		if !strings.Contains(j, "-S 100.64.0.45 ") || !strings.HasSuffix(j, "10.0.0.1:"+strconv.Itoa(r.Port)) {
			t.Errorf("the client is not bound to the route's source before the responder: %v", c)
		}
	}
	// No route to the address: no binding, as before.
	f2 := &fakeOwping{run: f.run}
	r2 := runnerFor(t, f2, "10.0.0.1")
	r2.Source = func(netip.Addr) (netip.Addr, bool) { return netip.Addr{}, false }
	r2.Round(context.Background())
	for _, c := range f2.calls {
		if slices.Contains(c, "-S") {
			t.Errorf("bound without a source: %v", c)
		}
	}
}
