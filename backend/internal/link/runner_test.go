package link

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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
	"unicode/utf8"

	ikhnosv1 "ikhnos/gen/ikhnosv1"
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", addrs...)})
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
	if got["ikhnos_link_probe_packets_lost_total"+base+`forward",method="owamp"}`] != "18" ||
		got["ikhnos_link_probe_packets_lost_total"+base+`reverse",method="owamp"}`] != "8" {
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
	k := `ikhnos_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="timeout"}`
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
	if got := render(r.Meter)[`ikhnos_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="auth"}`]; got != "1" {
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5"), peer("cl-b", "B", "10.0.1.1")})
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
		return render(m)[`ikhnos_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="reverse",method="owamp",reason="timeout"}`]
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
		if _, why, _ := r.once(context.Background(), "10.0.0.1", Forward); why == "" {
			t.Errorf("%s: accepted", name)
		}
	}
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	if _, why, _ := runnerFor(t, f, "10.0.0.1").once(context.Background(), "10.0.0.1", Forward); why != "" {
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
		if _, why, _ := r.once(context.Background(), bad, Forward); why == "" {
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

// With the real OWAMP binaries (IKHNOS_TEST_OWAMP_DIR holds bin/owampd and bin/owping) the runner measures a real
// responder over loopback. It is skipped where they are not installed.
func TestAgainstARealResponder(t *testing.T) {
	dir := os.Getenv("IKHNOS_TEST_OWAMP_DIR")
	if dir == "" {
		t.Skip("IKHNOS_TEST_OWAMP_DIR not set")
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", host)})
	m := NewMeter(p)
	m.EnableProbes()
	r := &Runner{Meter: m, Bin: filepath.Join(dir, "bin/owping"), Port: 18861, Packets: 20, Spacing: 50 * time.Millisecond}
	r.Defaults()
	r.Round(ctx)
	got := render(m)
	for _, d := range []string{"forward", "reverse"} {
		k := `ikhnos_link_probe_packets_sent_total{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp"}`
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{off, peer("cl-b", "B", "10.0.1.1")})
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
	p.Set("cl-me", []*ikhnosv1.LinkPeer{served, peer("cl-b", "B", "10.0.1.1")})
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
	if got[`ikhnos_link_path_reached{cluster="cl-me",peer="cl-a"}`] != "1" {
		t.Errorf("no route series for the pair the other side probes: %v", got)
	}
	for k := range got {
		if strings.Contains(k, `peer="cl-a"`) && (strings.Contains(k, "ikhnos_link_probe_") || strings.Contains(k, "tcp_connect")) {
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

// logged returns a logger that keeps everything it is asked, Debug included, and the buffer it writes to.
func logged() (*slog.Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return slog.New(slog.NewTextHandler(&b, &slog.HandlerOptions{Level: slog.LevelDebug})), &b
}

func countLines(b *bytes.Buffer, sub string) int { return strings.Count(b.String(), sub) }

// A run in which owping completed and every packet was lost is a measurement: the control connection worked, nothing arrived.
// The probe is up, the loss is 1, and no delay is exported (there is none), exactly what was seen in the field.
const allLost = "SUMMARY\t3.00\nSID\t7f000001ee78e525de2749f06040d6c3\nSTART_TIME\t17183736333280792826\nEND_TIME\t17183736352655782137\nLOSS_TIMEOUT\t2000000000\nSESSION_FINISHED\t1\nSENT\t100\nSYNC\t0\nMAXERR\t4.65661e-10\nDUPS\t0\nLOST\t100\nMIN\tnan\nMEDIAN\tnan\nMAX\tnan\nMINTTL\t255\nMAXTTL\t0\n"

func TestAFullyLostRunIsUpWithLossOneAndNoDelaySeries(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return []byte(allLost), []byte("owping: NTP: STA_NANO should be set\x1b[31m\n"), nil
	}}
	r := runnerFor(t, f, "10.0.0.1")
	lg, buf := logged()
	r.Log = lg
	r.Round(context.Background())
	got := render(r.Meter)
	for _, d := range []string{"forward", "reverse"} {
		base := `{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp"}`
		if got["ikhnos_link_probe_up"+base] != "1" || got["ikhnos_link_probe_loss_ratio"+base] != "1" {
			t.Errorf("%s: up %q loss %q", d, got["ikhnos_link_probe_up"+base], got["ikhnos_link_probe_loss_ratio"+base])
		}
		if got["ikhnos_link_probe_packets_sent_total"+base] != "100" || got["ikhnos_link_probe_packets_lost_total"+base] != "100" {
			t.Errorf("%s: sent %q lost %q", d, got["ikhnos_link_probe_packets_sent_total"+base], got["ikhnos_link_probe_packets_lost_total"+base])
		}
	}
	for k := range got {
		if strings.Contains(k, "delay_variation") || strings.Contains(k, "one_way_delay") {
			t.Errorf("a delay series for a run in which nothing arrived: %s", k)
		}
	}
	// It is also said once per direction, with what the tool reported, and not again while it stays so.
	if n := countLines(buf, "level=WARN"); n != 2 {
		t.Fatalf("%d warnings after the first round, want one per direction:\n%s", n, buf)
	}
	for _, want := range []string{"lost every packet", "session_finished=1", "start_time=17183736333280792826", "end_time=17183736352655782137", "loss_timeout=2000000000", "sync=0", "max_err=4.65661e-10", `stderr="owping: NTP: STA_NANO should be set[31m"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the warning lacks %q:\n%s", want, buf)
		}
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Error("a control character reached the log")
	}
	r.Round(context.Background())
	if n := countLines(buf, "level=WARN"); n != 2 {
		t.Errorf("%d warnings after the second round: a state that did not change was logged again", n)
	}
	// Never the address that answered: nothing arrived from it.
	r.mu.Lock()
	good := r.good["cl-a"]
	r.mu.Unlock()
	if good != "" {
		t.Errorf("an address that received nothing was remembered as the one that answers: %q", good)
	}
	// Packets arriving again is said at Info, once.
	f.run = func(args []string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }
	r.Round(context.Background())
	if n := countLines(buf, "receives packets again"); n != 2 {
		t.Errorf("%d recoveries:\n%s", n, buf)
	}
}

func TestOneDirectionFailingLeavesTheOtherRecorded(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		if slices.Contains(args, "-t") {
			return summary(t, "owping_to.txt"), nil, nil
		}
		return nil, []byte("owping: Server denied test: 100.64.0.139\nowping: Session Failed!\n"), errors.New("exit status 1")
	}
	r := runnerFor(t, f, "10.0.0.1")
	lg, buf := logged()
	r.Log = lg
	r.Round(context.Background())
	got := render(r.Meter)
	base := `{cluster="cl-me",peer="cl-a",direction="`
	if got["ikhnos_link_probe_packets_lost_total"+base+`forward",method="owamp"}`] != "18" || got["ikhnos_link_probe_up"+base+`forward",method="owamp"}`] != "1" {
		t.Errorf("the forward run was not recorded: %v", got)
	}
	if got["ikhnos_link_probe_up"+base+`reverse",method="owamp"}`] != "0" ||
		got["ikhnos_link_probe_failures_total"+base+`reverse",method="owamp",reason="denied"}`] != "1" {
		t.Errorf("the reverse failure is not a denied one: %v", got)
	}
	if hasKey(got, `direction="reverse",method="owamp"} 18`) || got["ikhnos_link_probe_loss_ratio"+base+`reverse",method="owamp"}`] != "" {
		t.Error("a figure for the failed direction")
	}
	// A refusal by a responder that answered is not silence: the peer is not backed off.
	r.mu.Lock()
	_, down := r.down["cl-a"]
	r.mu.Unlock()
	if down {
		t.Error("the peer was backed off for a denied test")
	}
	if n := countLines(buf, "level=WARN"); n != 1 || !strings.Contains(buf.String(), `reason=denied`) || !strings.Contains(buf.String(), `stderr="owping: Server denied test: 100.64.0.139 | owping: Session Failed!"`) {
		t.Errorf("the failure was not logged once with its reason and stderr:\n%s", buf)
	}
	// The stderr text never becomes part of the message.
	for _, l := range strings.Split(buf.String(), "\n") {
		if i := strings.Index(l, `msg="`); i >= 0 && strings.Contains(l[i:strings.Index(l[i+5:], `"`)+i+5], "denied test") {
			t.Errorf("remote text in a message: %s", l)
		}
	}
	// A repetition is not news, a different reason is, and answering again is said at Info.
	r.Round(context.Background())
	if n := countLines(buf, "level=WARN"); n != 1 {
		t.Errorf("the same failure was warned about again (%d)", n)
	}
	f.run = func(args []string) ([]byte, []byte, error) {
		if slices.Contains(args, "-t") {
			return summary(t, "owping_to.txt"), nil, nil
		}
		return nil, []byte("owping: something else"), errors.New("exit status 1")
	}
	r.Round(context.Background())
	if n := countLines(buf, "level=WARN"); n != 2 || !strings.Contains(buf.String(), "reason=other") {
		t.Errorf("a change of reason was not logged:\n%s", buf)
	}
	f.run = func([]string) ([]byte, []byte, error) { return summary(t, "owping_from.txt"), nil, nil }
	r.Round(context.Background())
	if n := countLines(buf, "link probe recovered"); n != 1 || !strings.Contains(buf.String(), "was=other") {
		t.Errorf("the recovery was not logged once:\n%s", buf)
	}
}

func TestWhatTheProgramSaidIsMadeSafeToLog(t *testing.T) {
	long := strings.Repeat("é", 300)
	got := stderrNote([]byte("owping: a\x00b\x1b[0m\r\n\n  \towping: second line \n"+long+"\n"), nil)
	if strings.ContainsAny(got, "\x00\x1b\r\n\t") || !strings.HasPrefix(got, "owping: ab[0m | owping: second line") {
		t.Errorf("note = %q", got)
	}
	if len(got) > maxNote || !utf8.ValidString(got) {
		t.Errorf("%d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	if got := stderrNote(nil, errors.New("signal: killed")); got != "signal: killed" {
		t.Errorf("without stderr the error should speak: %q", got)
	}
	if got := stderrNote(nil, nil); got != "" {
		t.Errorf("note = %q", got)
	}
}

// The strings are the ones the OWAMP programs print (see owamp/capi.c and owping.c); what an administrator sees as a reason
// must name what happened.
func TestFailuresAreClassifiedFromWhatOwpingReallyPrints(t *testing.T) {
	for _, tc := range []struct {
		msg      string
		timedOut bool
		want     string
	}{
		// the responder answered and refused: NAT between the clusters, its limits
		{"owping: Server denied test: 100.64.0.139\nowping: Session Failed!", false, "denied"},
		{"owping: Session Failed!", false, "denied"},
		{"owping: Test not allowed", false, "denied"},
		{"OWPControlOpen: Test denied: resources unavailable", false, "denied"},
		{"owping: Server denied access: node-1\nUnable to open control connection to 10.0.0.1:861.", false, "denied"},
		// the responder's policy leaves the client no mode; the closing complaint must not read as silence
		{"OWPControlOpen: No Common Modes\nowping: Unable to open control connection to 10.0.0.1:861.", false, "auth"},
		{"owping: authentication failed", false, "auth"},
		{"owping: Unknown user", false, "auth"},
		// owping prints nothing about why it could not connect, so all of these are a timeout from here
		{"owping: Unable to open control connection to 10.0.0.1:861.", false, "timeout"},
		{"", true, "timeout"},
		{"owping: timed out waiting", false, "timeout"},
		{"owping: connection refused", false, "refused"},
		{"connect: network is unreachable", false, "unreachable"},
		{"connect: no route to host", false, "unreachable"},
		{"owping: something unexpected", false, "other"},
		{"", false, "other"},
	} {
		if got := classify(tc.msg, tc.timedOut); got != tc.want {
			t.Errorf("classify(%q, %v) = %q, want %q", tc.msg, tc.timedOut, got, tc.want)
		}
	}
	if unreachable("denied") || unreachable("auth") {
		t.Error("a responder that answered must not make the peer count as down")
	}
}

func TestAnUnfinishedSessionIsAFailureWithItsReasonInTheLog(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return []byte("SUMMARY\t3\nSESSION_FINISHED\t0\nSENT\t100\nLOST\t100\n"), nil, nil
	}}
	r := runnerFor(t, f, "10.0.0.1")
	lg, buf := logged()
	r.Log = lg
	r.Round(context.Background())
	got := render(r.Meter)
	if got[`ikhnos_link_probe_up{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "0" ||
		got[`ikhnos_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="other"}`] != "1" {
		t.Errorf("a half-run session was recorded as a result: %v", got)
	}
	if !strings.Contains(buf.String(), "SESSION_FINISHED 0") {
		t.Errorf("the log does not say why:\n%s", buf)
	}
}

// Three preferred addresses that stay silent used up the budget of slow attempts before the address that had answered last
// was reached, so a healthy peer was reported down and backed off.
func TestDeadPreferredAddressesDoNotStopTheProberReachingTheOneThatAnswered(t *testing.T) {
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "10.0.0.9:") {
			return summary(t, "owping_to.txt"), nil, nil
		}
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}
	p := NewPeers()
	p.set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.9")}, map[string][]string{"cl-a": {"10.0.0.1", "10.0.0.2", "10.0.0.3"}})
	m := NewMeter(p)
	m.EnableProbes()
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: -1}
	r.Defaults()
	r.good = map[string]string{"cl-a": "10.0.0.9"}
	r.Round(context.Background())
	var tried []string
	for _, c := range f.calls[:4] {
		tried = append(tried, c[len(c)-1])
	}
	if want := []string{"10.0.0.1:861", "10.0.0.2:861", "10.0.0.3:861", "10.0.0.9:861"}; !slices.Equal(tried, want) {
		t.Fatalf("tried %v, want %v", tried, want)
	}
	got := render(m)
	for _, d := range []string{"forward", "reverse"} {
		if got[`ikhnos_link_probe_up{cluster="cl-me",peer="cl-a",direction="`+d+`",method="owamp"}`] != "1" {
			t.Errorf("%s: the peer is reported down although the address that answered still does: %v", d, got)
		}
	}
	r.mu.Lock()
	_, down := r.down["cl-a"]
	r.mu.Unlock()
	if down {
		t.Error("a healthy peer was backed off")
	}
	// It is tried once, not twice, when it was already among the attempts.
	f2 := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}}
	r2 := &Runner{Meter: m, Exec: f2.exec, PathEvery: -1}
	r2.Defaults()
	r2.good = map[string]string{"cl-a": "10.0.0.1"}
	r2.measure(context.Background(), p.List()[0], Forward)
	if len(f2.calls) != maxSlowFailures {
		t.Errorf("%d attempts, want %d: the address that answered was tried again", len(f2.calls), maxSlowFailures)
	}
}

// An address where the control connection works but no packet arrives is not the answer while another may work: the next
// address is tried, and only if it receives nothing either is the first all-lost run recorded.
func TestAnAddressThatReceivesNothingDoesNotPinTheOthersOut(t *testing.T) {
	lostOn := map[string]bool{"10.0.0.1": true}
	f := &fakeOwping{}
	f.run = func(args []string) ([]byte, []byte, error) {
		for a := range lostOn {
			if strings.Contains(strings.Join(args, " "), a+":") {
				return []byte(allLost), nil, nil
			}
		}
		return summary(t, "owping_to.txt"), nil, nil
	}
	r := runnerFor(t, f, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	r.Round(context.Background())
	got := render(r.Meter)
	if got[`ikhnos_link_probe_packets_lost_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "18" {
		t.Errorf("the run of the address that works was not the one recorded: %v", got)
	}
	r.mu.Lock()
	good := r.good["cl-a"]
	r.mu.Unlock()
	if good == "" || good == "10.0.0.1" {
		t.Errorf("good = %q", good)
	}

	// Nothing arrives anywhere: at most 1+maxAfterAllLost attempts per direction, the first run is what is recorded, and
	// nothing is remembered as the address that answers.
	f3 := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return []byte(allLost), nil, nil }}
	r3 := runnerFor(t, f3, "10.0.0.1", "10.0.0.2", "10.0.0.3", "10.0.0.4", "10.0.0.5")
	r3.Round(context.Background())
	if want := 2 * (1 + maxAfterAllLost); len(f3.calls) != want {
		t.Errorf("%d runs for two directions, want %d", len(f3.calls), want)
	}
	got = render(r3.Meter)
	if got[`ikhnos_link_probe_loss_ratio{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "1" ||
		got[`ikhnos_link_probe_up{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "1" ||
		got[`ikhnos_link_probe_packets_sent_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "100" {
		t.Errorf("the all-lost run was not recorded once: %v", got)
	}
	r3.mu.Lock()
	n := len(r3.good)
	r3.mu.Unlock()
	if n != 0 {
		t.Errorf("remembered %v as answering", r3.good)
	}

	// The extra address is silent: the first all-lost run still stands (the link works at the control level).
	f4 := &fakeOwping{run: func(args []string) ([]byte, []byte, error) {
		if strings.Contains(strings.Join(args, " "), "10.0.0.1:") {
			return []byte(allLost), nil, nil
		}
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}}
	r4 := runnerFor(t, f4, "10.0.0.1", "10.0.0.2", "10.0.0.3")
	if why := r4.measure(context.Background(), r4.Meter.Peers.List()[0], Forward); why != "" {
		t.Errorf("reason %q", why)
	}
	if render(r4.Meter)[`ikhnos_link_probe_loss_ratio{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "1" {
		t.Error("the all-lost run was dropped because the next address was silent")
	}
}

func TestTheTraceTargetIsAnAddressTheTraceCanUseThatDoesNotJumpAround(t *testing.T) {
	if pathIPv6Tested {
		t.Skip("IPv6 is traced on this build; the cases below are about it not being")
	}
	mk := func(addrs []string, pref ...string) Peer {
		p := NewPeers()
		p.set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", addrs...)}, map[string][]string{"cl-a": pref})
		return p.List()[0]
	}
	for _, tc := range []struct {
		name             string
		p                Peer
		reached, answers string
		want             string
		untraceable      bool
	}{
		{"IPv6 preferred first falls through to IPv4", mk([]string{"10.0.0.1", "2001:db8::1"}, "2001:db8::1"), "", "", "10.0.0.1", false},
		{"only IPv6", mk([]string{"2001:db8::1", "2001:db8::2"}), "", "", "", true},
		{"no address", Peer{ID: "cl-a"}, "", "", "", false},
		{"preferred wins over class", mk([]string{"100.64.0.45", "10.42.2.0", "203.0.113.5"}, "203.0.113.5"), "", "", "203.0.113.5", false},
		{"the mesh address beats a LAN address that sorts before it", mk([]string{"10.42.2.0", "100.64.0.45"}), "", "", "100.64.0.45", false},
		{"public before private", mk([]string{"192.168.1.5", "203.0.113.5"}), "", "", "203.0.113.5", false},
		{"class order mesh, public, private", mk([]string{"192.168.1.5", "203.0.113.5", "100.64.0.45"}), "", "", "100.64.0.45", false},
		{"same class by text", mk([]string{"10.0.0.9", "10.0.0.2"}), "", "", "10.0.0.2", false},
		{"the address that answered the probe comes after the preferred", mk([]string{"100.64.0.45", "10.0.0.2"}), "", "10.0.0.2", "10.0.0.2", false},
		{"the address the last trace reached is kept", mk([]string{"100.64.0.45", "10.0.0.2"}, "100.64.0.45"), "10.0.0.2", "", "10.0.0.2", false},
		{"a reached address that is no longer the peer's is dropped", mk([]string{"100.64.0.45"}), "10.9.9.9", "", "100.64.0.45", false},
		{"a reached IPv6 address is not used", mk([]string{"100.64.0.45", "2001:db8::1"}), "2001:db8::1", "", "100.64.0.45", false},
	} {
		got, untraceable := pathTarget(tc.p, tc.reached, tc.answers)
		if got != tc.want || untraceable != tc.untraceable {
			t.Errorf("%s: pathTarget = %q, %v; want %q, %v", tc.name, got, untraceable, tc.want, tc.untraceable)
		}
	}
}

func TestAPeerWithOnlyIPv6AddressesIsCountedOnceAndNeverTakesTheGate(t *testing.T) {
	if pathIPv6Tested {
		t.Skip("IPv6 is traced on this build")
	}
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	r := runnerFor(t, f, "2001:db8::1")
	lg, buf := logged()
	r.Log = lg
	r.Gate, r.PathEvery = NewGate(), time.Minute
	r.Path = func(context.Context, netip.Addr) (PathResult, error) {
		t.Error("a trace towards an IPv6 address")
		return PathResult{}, nil
	}
	hold, _ := r.Gate.Acquire(context.Background()) // a trace that waited for the gate would not get through
	defer hold()
	p := r.Meter.Peers.List()[0]
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		r.maybePath(ctx, p)
		cancel()
	}
	if got := render(r.Meter)[`ikhnos_link_path_probe_failures_total{cluster="cl-me",peer="cl-a"}`]; got != "1" {
		t.Errorf("path failures = %q, want 1", got)
	}
	if countLines(buf, "level=INFO") != 1 {
		t.Errorf("not said once at Info:\n%s", buf)
	}
}

func TestADualStackPeerIsTracedOverIPv4AndKeepsTheAddressItReached(t *testing.T) {
	if pathIPv6Tested {
		t.Skip("IPv6 is traced on this build")
	}
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	p := NewPeers()
	p.set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", "2001:db8::1", "10.0.0.1", "100.64.0.45")}, map[string][]string{"cl-a": {"2001:db8::1"}})
	m := NewMeter(p)
	m.EnableProbes()
	var traced []string
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: time.Minute, Path: func(_ context.Context, dst netip.Addr) (PathResult, error) {
		traced = append(traced, dst.String())
		return PathResult{MTU: 1500, Reached: true, Route: "r"}, nil
	}}
	r.Defaults()
	now := time.Unix(1_700_000_000, 0)
	r.Now = func() time.Time { return now }
	r.maybePath(context.Background(), p.List()[0])
	now = now.Add(2 * time.Minute)
	r.maybePath(context.Background(), p.List()[0])
	if !slices.Equal(traced, []string{"100.64.0.45", "100.64.0.45"}) {
		t.Errorf("traced %v", traced)
	}
}

func TestWhatTheRunnerRemembersOfAPeerSurvivesAShortBlipAndIsDroppedAfterTheGrace(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}}
	r := runnerFor(t, f, "10.0.0.1")
	now := time.Unix(1_700_000_000, 0)
	r.Now = func() time.Time { return now }
	r.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	r.Round(context.Background())
	r.good = map[string]string{"cl-a": "10.0.0.1"}
	r.lastPath = map[string]time.Time{"cl-a": now}
	remembered := func() (down, good, path, state bool) {
		r.mu.Lock()
		defer r.mu.Unlock()
		_, down = r.down["cl-a"]
		_, good = r.good["cl-a"]
		_, path = r.lastPath["cl-a"]
		_, state = r.state[probeKey("cl-a", Forward)]
		return
	}
	if d, g, p, s := remembered(); !d || !g || !p || !s {
		t.Fatalf("setup: down %v good %v path %v state %v", d, g, p, s)
	}
	// The list is empty for a while (the agent restarting): the back-off must not restart from nothing.
	r.Meter.Peers.Set("cl-me", nil)
	for _, after := range []time.Duration{0, 5 * time.Minute, 10 * time.Minute, 14 * time.Minute} { // 29 minutes in all
		now = now.Add(after)
		r.Round(context.Background())
		if d, g, p, s := remembered(); !d || !g || !p || !s {
			t.Fatalf("forgotten after only %s: down %v good %v path %v state %v", after, d, g, p, s)
		}
	}
	// It comes back: still remembered, and the clock of its absence starts again the next time.
	r.Meter.Peers.Set("cl-me", []*ikhnosv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	r.Round(context.Background())
	if d, _, _, _ := remembered(); !d {
		t.Fatal("forgotten although the peer came back")
	}
	r.mu.Lock()
	_, stillAbsent := r.absent["cl-a"]
	r.mu.Unlock()
	if stillAbsent {
		t.Error("the peer is still timed as absent")
	}
	r.Meter.Peers.Set("cl-me", nil)
	r.Round(context.Background()) // absent since now
	now = now.Add(runnerStateGrace - time.Minute)
	r.Round(context.Background())
	if d, _, _, _ := remembered(); !d {
		t.Fatal("forgotten before the grace was over, counting from the new absence")
	}
	now = now.Add(2 * time.Minute)
	r.Round(context.Background())
	if d, g, p, s := remembered(); d || g || p || s {
		t.Errorf("still remembered after the grace: down %v good %v path %v state %v", d, g, p, s)
	}
	r.mu.Lock()
	left := len(r.absent) + len(r.reached) + len(r.noTrace)
	r.mu.Unlock()
	if left != 0 {
		t.Errorf("%d bookkeeping entries left", left)
	}
}

func TestTheOverrunOfARoundIsSaidOnceWhenThePeersChangeAndNotEveryRound(t *testing.T) {
	f := &fakeOwping{run: func(args []string) ([]byte, []byte, error) { return summary(t, "owping_to.txt"), nil, nil }}
	p := NewPeers()
	var lps []*ikhnosv1.LinkPeer
	for _, id := range []string{"cl-a", "cl-b", "cl-c"} { // 3 peers x 2 x 15 s = 90 s against a minute
		lps = append(lps, peer(id, id, "10.0.0."+strconv.Itoa(len(lps)+1)))
	}
	p.Set("cl-me", lps)
	m := NewMeter(p)
	m.EnableProbes()
	lg, buf := logged()
	r := &Runner{Meter: m, Exec: f.exec, PathEvery: -1, Log: lg}
	r.Defaults()
	for i := 0; i < 3; i++ {
		r.Round(context.Background())
	}
	if n := countLines(buf, "level=WARN"); n != 1 || !strings.Contains(buf.String(), "peers=3") || !strings.Contains(buf.String(), "effective_interval=1m36s") || !strings.Contains(buf.String(), "advice=") {
		t.Fatalf("%d warnings after three rounds:\n%s", n, buf)
	}
	p.Set("cl-me", lps[:2]) // 60 s: it fits, nothing to say
	r.Round(context.Background())
	p.Set("cl-me", lps) // changed again
	r.Round(context.Background())
	if n := countLines(buf, "level=WARN"); n != 2 {
		t.Errorf("%d warnings after the peers changed twice", n)
	}
}

func TestEachAddressIsBoundToItsOwnRouteSource(t *testing.T) {
	f := &fakeOwping{run: func([]string) ([]byte, []byte, error) {
		return nil, []byte("owping: Unable to open control connection"), errors.New("exit 1")
	}}
	r := runnerFor(t, f, "10.0.0.1", "192.168.1.5")
	r.Source = func(ip netip.Addr) (netip.Addr, bool) {
		if ip.String() == "10.0.0.1" {
			return netip.MustParseAddr("10.0.0.50"), true
		}
		return netip.MustParseAddr("192.168.1.50"), true
	}
	r.measure(context.Background(), r.Meter.Peers.List()[0], Forward)
	for _, c := range f.calls {
		j := strings.Join(c, " ")
		ok := strings.Contains(j, "-S 10.0.0.50 ") && strings.HasSuffix(j, "10.0.0.1:861") || strings.Contains(j, "-S 192.168.1.50 ") && strings.HasSuffix(j, "192.168.1.5:861")
		if !ok {
			t.Errorf("wrong source for the address: %s", j)
		}
	}
	if len(f.calls) != 2 {
		t.Errorf("%d calls", len(f.calls))
	}
}

func TestRouteSourceIsTheLocalAddressOfTheRoute(t *testing.T) {
	for in, want := range map[string]string{
		"127.0.0.1":         "127.0.0.1",
		"::ffff:127.0.0.1":  "127.0.0.1", // an IPv4 address in its mapped form is an IPv4 address
		"127.0.0.1%ignored": "",
	} {
		a, err := netip.ParseAddr(in)
		if err != nil {
			continue // not a form this Go version parses
		}
		got, ok := routeSource(a)
		if want == "" {
			continue
		}
		if !ok || got.String() != want {
			t.Errorf("routeSource(%s) = %v, %v; want %s", in, got, ok, want)
		}
	}
	// What cannot be sent to has no source, and the probe then runs unbound, as before.
	for _, bad := range []netip.Addr{{}, netip.MustParseAddr("0.0.0.0"), netip.MustParseAddr("::"), netip.MustParseAddr("224.0.0.1")} {
		if got, ok := routeSource(bad); ok {
			t.Errorf("routeSource(%v) = %v, want none", bad, got)
		}
	}
	// A destination without a route: skipped where this host happens to have one (a default route to anywhere).
	if got, ok := routeSource(netip.MustParseAddr("2001:db8::1")); ok && got.Is4() {
		t.Errorf("an IPv6 destination got an IPv4 source %v", got)
	}
}
