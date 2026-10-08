package collector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
	"continuum/internal/agent/collect"
	"continuum/internal/flow"
	"continuum/internal/probe"

	"google.golang.org/protobuf/reflect/protoreflect"
)

type fake struct {
	method string
	bytes  bool
	next   [][]*continuumv1.RawFlow
	closed bool
	// snat, when non-zero, makes fake implement SnatExhaustionSource (see its own method below) -
	// left unset, a *fake behaves exactly like a source with no opinion on SNAT exhaustion at all, the
	// same as conntrack.Reader; TestChooseFallsBackAndSaysWhy and friends never go near this.
	snat uint64
	// cpuFreqChangeCount/thermalTripCount are ThermalThrottle's own analogue of snat above.
	cpuFreqChangeCount, thermalTripCount uint64
	// calls counts every real Collect() call - the stand-in, in tests, for the eBPF map read/fold a
	// real Source does. Run calling this less often than once per tick is exactly what a pause backing
	// off real work is supposed to look like; atomic because Run's goroutine writes it while a test
	// reads it concurrently.
	calls atomic.Int64
}

func (f *fake) Method() string   { return f.method }
func (f *fake) BytesKnown() bool { return f.bytes }
func (f *fake) Close() error     { f.closed = true; return nil }
func (f *fake) Collect() ([]*continuumv1.RawFlow, uint64, error) {
	f.calls.Add(1)
	if len(f.next) == 0 {
		return nil, 0, nil
	}
	r := f.next[0]
	f.next = f.next[1:]
	return r, 0, nil
}

// SnatExhaustion makes *fake satisfy SnatExhaustionSource unconditionally (a real eBPF Observer always
// does too, whether or not either of its own attach paths actually worked - see its own doc comment);
// tests that want "no opinion" behavior use a value that is simply always 0, same as an Observer whose
// SnatExhaustionErr is set.
func (f *fake) SnatExhaustion() uint64 { return f.snat }

// ThermalThrottle makes *fake satisfy ThermalThrottleSource unconditionally, the same always-on
// treatment SnatExhaustion above gets.
func (f *fake) ThermalThrottle() (uint64, uint64) { return f.cpuFreqChangeCount, f.thermalTripCount }

func TestChooseFallsBackAndSaysWhy(t *testing.T) {
	bad := func() (Source, error) { return nil, errors.New("no BTF") }
	ct := &fake{method: "conntrack"}
	good := func() (Source, error) { return ct, nil }

	s, why, err := Choose("auto", bad, good)
	if err != nil || s.Method() != "conntrack" || len(why) != 1 || why[0] != "ebpf: no BTF" {
		t.Fatalf("auto = %v %v %v", s, why, err)
	}
	if s, _, err := Choose("auto", func() (Source, error) { return &fake{method: "ebpf"}, nil }, good); err != nil || s.Method() != "ebpf" {
		t.Errorf("eBPF must win when it works: %v %v", s, err)
	}
	// Forcing a method never falls back to another one silently.
	if _, _, err := Choose("ebpf", bad, good); err == nil {
		t.Error("ebpf was forced and cannot run; that has to be an error")
	}
	if _, _, err := Choose("auto", bad, bad); err == nil {
		t.Error("nothing works: expected an error naming the reasons")
	}
	if _, _, err := Choose("dtrace", bad, good); err == nil {
		t.Error("unknown method accepted")
	}
}

func rf(local, peer string, port uint32, n uint64) *continuumv1.RawFlow {
	return &continuumv1.RawFlow{Client: true, LocalIp: local, PeerIp: peer, Port: port, Protocol: "tcp", Connections: n, BytesOut: n * 10, BytesIn: n * 20}
}

func TestRunDeliversSignedReportsAndKeepsWhatCouldNotBeDelivered(t *testing.T) {
	secret := []byte("0123456789abcdef-flow-secret")
	ix := &collect.Index{Pods: map[string]string{"10.42.0.5": "shop/web"}, Services: map[string][]string{"10.43.1.1": {"shop/db"}}}
	p := flow.NewPipeline(secret, func() *collect.Index { return ix }, nil)

	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !up.Load() {
			http.Error(w, "restarting", http.StatusServiceUnavailable)
			return
		}
		p.Handler().ServeHTTP(w, r)
	}))
	defer srv.Close()

	src := &fake{method: "ebpf", bytes: true, next: [][]*continuumv1.RawFlow{
		{rf("10.42.0.5", "10.43.1.1", 5432, 3)}, // window 1: the agent is down
		{rf("10.42.0.5", "10.43.1.1", 5432, 4)}, // window 2: the agent is back
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, src, srv.URL, secret, "node-a", 100*time.Millisecond, func(string, ...any) {})
		close(done)
	}()
	time.Sleep(150 * time.Millisecond) // first window fails
	up.Store(true)

	deadline := time.Now().Add(3 * time.Second)
	var batch *continuumv1.FlowBatch
	for time.Now().Before(deadline) {
		if b := p.Aggregator.Flush(); b != nil && len(b.Flows) > 0 {
			batch = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if batch == nil {
		t.Fatal("nothing arrived at the agent")
	}
	f := batch.Flows[0]
	if f.Src.Ref != "shop/web" || f.Dst.Ref != "shop/db" || f.Port != 5432 || f.Method != "ebpf" || !f.BytesKnown {
		t.Errorf("flow = %+v", f)
	}
	if f.Connections != 7 || f.BytesOut != 70 || f.BytesIn != 140 {
		t.Errorf("the window that could not be delivered was not merged into the next: connections=%d out=%d in=%d, want 7/70/140", f.Connections, f.BytesOut, f.BytesIn)
	}
}

func TestRunWithTheWrongSecretDeliversNothing(t *testing.T) {
	p := flow.NewPipeline([]byte("0123456789abcdef-right-secret"), func() *collect.Index { return &collect.Index{Pods: map[string]string{"10.42.0.5": "shop/web"}} }, nil)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	src := &fake{method: "conntrack", next: [][]*continuumv1.RawFlow{{rf("10.42.0.5", "8.8.8.8", 443, 1)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	Run(ctx, src, srv.URL, []byte("0123456789abcdef-wrong-secret!"), "node-a", 100*time.Millisecond, func(string, ...any) {})
	if b := p.Aggregator.Flush(); b != nil && len(b.Flows) > 0 {
		t.Fatalf("a report signed with the wrong secret was accepted: %+v", b.Flows)
	}
}

// TestRunSkipsRealCollectionWhilePausedAndResumesPromptly proves Run actually does less real work
// while the agent is paused, not just that the pause toggle exists: with the agent's Pipeline paused
// from the start, src.Collect() - the stand-in for the eBPF map read/fold a real Source does - is
// called far less often than once per tick, because most ticks are skipped outright (no Collect(), no
// sign, no POST). Once unpaused, the next real Collect() happens within the documented bound of
// probe.PauseBackoffTicks ticks.
func TestRunSkipsRealCollectionWhilePausedAndResumesPromptly(t *testing.T) {
	secret := []byte("0123456789abcdef-flow-secret")
	p := flow.NewPipeline(secret, func() *collect.Index { return &collect.Index{} }, nil)
	p.SetPaused(true)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	src := &fake{method: "ebpf", bytes: true}
	// 300ms, not the production 30s, so the test runs quickly - but still well over a second between
	// real attempts (PauseBackoffTicks*every = 1.2s) so two real attempts never land on the same
	// whole-second timestamp and collide in the replay cache (see Sign/ReplayCache), which would
	// otherwise make every real attempt fail and defeat the very backoff this test is checking.
	every := 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, src, srv.URL, secret, "node-a", every, func(string, ...any) {})
		close(done)
	}()

	// Let roughly 12 ticks pass while paused. probe.PauseBackoffTicks=4 means about 3 of those should
	// be real Collect() calls (tick 1, then every 4th after); nowhere near all 12.
	time.Sleep(12*every + 8*every) // + margin for scheduling jitter
	gotPaused := src.calls.Load()
	if gotPaused == 0 {
		t.Fatal("Collect() was never called at all while paused - Run never even checks")
	}
	if gotPaused > 6 {
		t.Fatalf("Collect() calls while paused = %d over ~12 ticks; want far fewer than one per tick, "+
			"i.e. the real collect/sign/POST cycle must be skipped on most ticks while paused", gotPaused)
	}

	// Resume, and check the bound this design promises: at most probe.PauseBackoffTicks ticks after
	// the agent stops reporting paused, the next real Collect() happens.
	before := src.calls.Load()
	p.SetPaused(false)
	bound := time.Duration(probe.PauseBackoffTicks) * every
	deadline := time.Now().Add(bound + 10*every) // margin for scheduling jitter on top of the bound itself
	for time.Now().Before(deadline) && src.calls.Load() == before {
		time.Sleep(every / 2)
	}
	cancel()
	<-done
	if src.calls.Load() == before {
		t.Fatalf("no real Collect() within the stated resume bound of %v (PauseBackoffTicks * interval)", bound)
	}
}

func TestReportBoundsAndSaysWhatItDropped(t *testing.T) {
	var many []*continuumv1.RawFlow
	for i := 0; i < flow.MaxRawFlows+10; i++ {
		many = append(many, rf("10.42.0.5", "10.43.1.1", uint32(1+i%60000), uint64(1+i%3)))
	}
	rep := Report(&fake{method: "ebpf", bytes: true}, "n", 30*time.Second, many, 2, 9, nil)
	if len(rep.Flows) != flow.MaxRawFlows || rep.Lost != 12 || rep.WindowSeconds != 30 || rep.SnatExhaustion != 9 {
		t.Errorf("flows=%d lost=%d window=%d snatExhaustion=%d", len(rep.Flows), rep.Lost, rep.WindowSeconds, rep.SnatExhaustion)
	}
}

// TestRunReportsThermalThrottle is ThermalThrottle's own analogue of TestRunReportsSnatExhaustion right
// above: a source that implements ThermalThrottleSource gets both counters read fresh every window and
// carried onto the delivered FlowReport/CollectorInfo for this node.
func TestRunReportsThermalThrottle(t *testing.T) {
	secret := []byte("0123456789abcdef-flow-secret")
	p := flow.NewPipeline(secret, func() *collect.Index { return &collect.Index{} }, nil)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	src := &fake{method: "ebpf", bytes: true, cpuFreqChangeCount: 500, thermalTripCount: 3}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, src, srv.URL, secret, "node-a", 100*time.Millisecond, func(string, ...any) {})
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	var batch *continuumv1.FlowBatch
	for time.Now().Before(deadline) {
		if b := p.Aggregator.Flush(); b != nil && len(b.Collectors) > 0 {
			batch = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if batch == nil || len(batch.Collectors) != 1 || batch.Collectors[0].ThermalThrottle == nil ||
		batch.Collectors[0].ThermalThrottle.CpuFreqChangeCount != 500 || batch.Collectors[0].ThermalThrottle.ThermalTripCount != 3 {
		t.Fatalf("batch = %+v", batch)
	}
}

// TestRunReportsSnatExhaustion covers Run's own type-assertion wiring (SnatExhaustionSource): a source
// that implements it gets its reading read fresh every window and carried onto the delivered
// FlowReport, ending up on the agent's own CollectorInfo for this node.
func TestRunReportsSnatExhaustion(t *testing.T) {
	secret := []byte("0123456789abcdef-flow-secret")
	p := flow.NewPipeline(secret, func() *collect.Index { return &collect.Index{} }, nil)
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	src := &fake{method: "ebpf", bytes: true, snat: 5}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		Run(ctx, src, srv.URL, secret, "node-a", 100*time.Millisecond, func(string, ...any) {})
		close(done)
	}()

	deadline := time.Now().Add(3 * time.Second)
	var batch *continuumv1.FlowBatch
	for time.Now().Before(deadline) {
		if b := p.Aggregator.Flush(); b != nil && len(b.Collectors) > 0 {
			batch = b
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	if batch == nil || len(batch.Collectors) != 1 || batch.Collectors[0].SnatExhaustion != 5 {
		t.Fatalf("batch = %+v", batch)
	}
}

func TestRollupSaturationSumsByIfaceAndDividesByWindow(t *testing.T) {
	flows := []*continuumv1.RawFlow{
		{Iface: "eth0", BytesOut: 1_000_000, BytesIn: 500_000},
		{Iface: "eth0", BytesOut: 500_000, BytesIn: 0},
		{Iface: "wlan0", BytesOut: 100, BytesIn: 100},
		{BytesOut: 999}, // no iface resolved: must not be attributed anywhere
	}
	got := rollupSaturation(flows, 2*time.Second)
	want := map[string]uint64{"eth0": (1_000_000 + 500_000 + 500_000) * 8 / 2, "wlan0": (100 + 100) * 8 / 2}
	if len(got) != len(want) {
		t.Fatalf("len(got)=%d, want %d (%+v)", len(got), len(want), got)
	}
	for _, ls := range got {
		if ls.ThroughputBps != want[ls.Iface] {
			t.Errorf("%s: throughput_bps=%d, want %d", ls.Iface, ls.ThroughputBps, want[ls.Iface])
		}
	}
}

func TestRollupSaturationNilOnNothingToReport(t *testing.T) {
	if got := rollupSaturation(nil, 30*time.Second); got != nil {
		t.Errorf("no flows: got %+v, want nil", got)
	}
	if got := rollupSaturation([]*continuumv1.RawFlow{{Iface: "eth0", BytesOut: 1}}, 0); got != nil {
		t.Errorf("zero window: got %+v, want nil", got)
	}
	if got := rollupSaturation([]*continuumv1.RawFlow{{BytesOut: 1}}, time.Second); got != nil {
		t.Errorf("no iface resolved anywhere: got %+v, want nil", got)
	}
}

func TestRollupSaturationComputesPctWhenSpeedIsReadable(t *testing.T) {
	dir := t.TempDir()
	old := sysClassNet
	sysClassNet = dir
	defer func() { sysClassNet = old }()

	mk := func(name, speed string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if speed != "" {
			if err := os.WriteFile(filepath.Join(dir, name, "speed"), []byte(speed), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	mk("eth0", "1000") // 1000 Mbps
	mk("veth1", "-1")  // "no link": must stay unknown, not treated as 0 capacity
	mk("wlan0", "")    // no speed file at all (virtual-like): same treatment

	// eth0: 100,000,000 bytes/sec = 800 Mbps out of a 1000 Mbps link = 80%.
	flows := []*continuumv1.RawFlow{
		{Iface: "eth0", BytesOut: 100_000_000},
		{Iface: "veth1", BytesOut: 123},
		{Iface: "wlan0", BytesOut: 456},
	}
	got := rollupSaturation(flows, time.Second)
	byIface := map[string]*continuumv1.LinkSaturation{}
	for _, ls := range got {
		byIface[ls.Iface] = ls
	}
	if byIface["eth0"].SaturationPct == nil || round1(*byIface["eth0"].SaturationPct) != 80 {
		t.Errorf("eth0 saturation_pct = %v, want 80", byIface["eth0"].SaturationPct)
	}
	if byIface["veth1"].SaturationPct != nil {
		t.Errorf("veth1 (no link / speed=-1) got a saturation_pct: %v, want nil (unknown, not 0%%)", *byIface["veth1"].SaturationPct)
	}
	if byIface["wlan0"].SaturationPct != nil {
		t.Errorf("wlan0 (no speed file) got a saturation_pct: %v, want nil (unknown, not 0%%)", *byIface["wlan0"].SaturationPct)
	}
}

func TestRollupSaturationClampsAt100(t *testing.T) {
	dir := t.TempDir()
	old := sysClassNet
	sysClassNet = dir
	defer func() { sysClassNet = old }()
	if err := os.MkdirAll(filepath.Join(dir, "eth0"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "eth0", "speed"), []byte("10"), 0o644); err != nil { // 10 Mbps
		t.Fatal(err)
	}
	// Vastly more than 10 Mbps worth of bytes in one second - a counter race across the window boundary.
	got := rollupSaturation([]*continuumv1.RawFlow{{Iface: "eth0", BytesOut: 100_000_000}}, time.Second)
	if len(got) != 1 || got[0].SaturationPct == nil || *got[0].SaturationPct != 100 {
		t.Fatalf("got %+v, want clamped to 100", got)
	}
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }

// A flow that could not be sent is merged with the next window's reading of it. Every field of that reading must
// survive: counts add, samples take the newer value. The test walks the message, so a field added to RawFlow and not
// handled by merge fails here.
func TestMergeKeepsEveryFieldOfTheNewerReading(t *testing.T) {
	isKey := func(fd protoreflect.FieldDescriptor) bool {
		switch fd.Name() {
		case "client", "local_ip", "peer_ip", "port", "protocol":
			return true
		}
		return false
	}
	fill := func(n uint64) *continuumv1.RawFlow {
		f := &continuumv1.RawFlow{Client: true, LocalIp: "10.0.0.1", PeerIp: "10.0.0.2", Port: 80, Protocol: "tcp"}
		m := f.ProtoReflect()
		fields := m.Descriptor().Fields()
		for i := 0; i < fields.Len(); i++ {
			fd := fields.Get(i)
			if isKey(fd) {
				continue
			}
			switch fd.Kind() {
			case protoreflect.Uint32Kind:
				m.Set(fd, protoreflect.ValueOfUint32(uint32(n)))
			case protoreflect.Uint64Kind:
				m.Set(fd, protoreflect.ValueOfUint64(n))
			case protoreflect.StringKind:
				m.Set(fd, protoreflect.ValueOfString(fmt.Sprint("s", n)))
			case protoreflect.EnumKind:
				m.Set(fd, protoreflect.ValueOfEnum(fd.Enum().Values().Get(int(n)).Number()))
			}
		}
		return f
	}
	got := merge([]*continuumv1.RawFlow{fill(1)}, []*continuumv1.RawFlow{fill(2)})
	if len(got) != 1 {
		t.Fatalf("%d flows after the merge, want 1", len(got))
	}
	old := fill(1).ProtoReflect()
	m := got[0].ProtoReflect()
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if isKey(fd) {
			continue
		}
		if m.Get(fd).Equal(old.Get(fd)) {
			t.Errorf("%s still has the older reading's value %v: the newer reading was dropped by merge", fd.Name(), m.Get(fd))
		}
	}
	if got[0].Connections != 3 || got[0].Retransmits != 3 || got[0].FailedReset != 3 {
		t.Errorf("counts must add: connections %d, retransmits %d, failed_reset %d", got[0].Connections, got[0].Retransmits, got[0].FailedReset)
	}
	if got[0].RttUs != 2 {
		t.Errorf("a sample must take the newer value: rtt %d", got[0].RttUs)
	}
}
