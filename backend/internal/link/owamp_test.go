package link

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func fixture(t *testing.T, name string) OwampResult {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r, err := ParseOwping(f)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

// The fixtures are real `owping -M` output from a run with packets dropped on purpose: 100 sent each way, with a known
// number lost, so a parser that reads the wrong field or the wrong direction is caught.
func TestTheParserReadsRealOwpingOutput(t *testing.T) {
	to := fixture(t, "owping_to.txt")
	if to.Sent != 100 || to.Lost != 18 || to.Duplicates != 0 {
		t.Fatalf("to: %+v", to)
	}
	if !to.HasDelay || to.Min <= 0 || to.Median < to.Min || to.Max < to.Median {
		t.Fatalf("to delays: %+v", to)
	}
	if to.Synchronized {
		t.Error("the fixture was recorded on an unsynchronised clock")
	}
	if to.ClockError <= 0 || !to.HasClockError {
		t.Errorf("clock error = %v (%v)", to.ClockError, to.HasClockError)
	}
	if to.Hops != 0 {
		t.Errorf("hops on loopback = %d, want 0", to.Hops)
	}
	from := fixture(t, "owping_from.txt")
	if from.Sent != 100 || from.Lost != 8 {
		t.Fatalf("from: %+v", from)
	}
}

func TestTheParserRefusesWhatIsNotACompleteSummary(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"notice only": "Approximately 8.0 seconds until results available\n",
		"no sent":     "SUMMARY\t3.00\nLOST\t3\n",
		"more lost":   "SUMMARY\t3.00\nSENT\t10\nLOST\t11\n",
		"before":      "SENT\t10\nLOST\t1\n",
	} {
		if _, err := ParseOwping(strings.NewReader(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTheParserToleratesMissingAndOddFields(t *testing.T) {
	r, err := ParseOwping(strings.NewReader("noise\nSUMMARY\t3.00\nSENT\t50\nLOST\t50\nSYNC\t1\nMIN\t0.01\nMINTTL\t250\nMAXERR\tbad\n<BUCKETS>\n\t1\t5\n</BUCKETS>\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.HasDelay {
		t.Error("a run that lost everything claimed a delay")
	}
	if r.HasClockError {
		t.Error("an unreadable MAXERR was taken as a clock error")
	}
	if !r.Synchronized || r.Hops != 5 {
		t.Errorf("%+v", r)
	}
	// an oversized input is cut off rather than read whole
	big := "SUMMARY\t3\nSENT\t1\n" + strings.Repeat("x\ty\n", 1<<20)
	if _, err := ParseOwping(strings.NewReader(big)); err != nil {
		t.Fatalf("a long tail should not break a complete summary: %v", err)
	}
}

func TestTheParserOnlyTrustsACompleteAndOrderedSetOfDelays(t *testing.T) {
	const head = "SUMMARY\t3.00\nSENT\t50\nLOST\t1\n"
	for name, in := range map[string]string{
		"min only":    head + "MIN\t0.01\n",
		"no max":      head + "MIN\t0.01\nMEDIAN\t0.02\n",
		"median<min":  head + "MIN\t0.02\nMEDIAN\t0.01\nMAX\t0.03\n",
		"max<median":  head + "MIN\t0.01\nMEDIAN\t0.03\nMAX\t0.02\n",
		"unparseable": head + "MIN\t0.01\nMEDIAN\tx\nMAX\t0.02\n",
	} {
		r, err := ParseOwping(strings.NewReader(in))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if r.HasDelay || r.Min != 0 || r.Median != 0 || r.Max != 0 {
			t.Errorf("%s: delays kept: %+v", name, r)
		}
		if r.Sent != 50 || r.Lost != 1 {
			t.Errorf("%s: the loss counts were dropped with the delays: %+v", name, r)
		}
	}
	r, err := ParseOwping(strings.NewReader(head + "MIN\t0.01\nMEDIAN\t0.02\nMAX\t0.03\n"))
	if err != nil || !r.HasDelay || r.Max != 0.03 {
		t.Errorf("a complete set was refused: %+v %v", r, err)
	}
}

func TestTheParserRefusesCountsThatCouldWrapACounter(t *testing.T) {
	for _, in := range []string{
		"SUMMARY\t3\nSENT\t1e30\n",
		"SUMMARY\t3\nSENT\t10\nDUPS\t9.3e18\n",
		"SUMMARY\t3\nSENT\t10\nLOST\t-1\n",
	} {
		if _, err := ParseOwping(strings.NewReader(in)); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestClockErrorIsShownOnlyWhenKnownAndTheClocksAgree(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	r := OwampResult{Sent: 10, ClockError: 4.6e-10, HasClockError: true}
	m.ObserveProbe("cl-a", Forward, r)
	if hasKey(render(m), "clock_error") {
		t.Fatal("a clock error bound is shown for unsynchronised clocks")
	}
	r.Synchronized = true
	m.ObserveProbe("cl-a", Forward, r)
	if !hasKey(render(m), "clock_error") {
		t.Fatal("the clock error is hidden for synchronised clocks")
	}
	r.HasClockError = false
	m.ObserveProbe("cl-a", Forward, r)
	if hasKey(render(m), "clock_error") {
		t.Fatal("a missing clock error is shown as 0")
	}
}

func TestHopsAreExportedOnceFromTheTTLOfTheOneWayProbe(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 10, Hops: 9})
	m.ObserveProbe("cl-a", Reverse, OwampResult{Sent: 10, Hops: 11})
	m.ObservePath("cl-a", PathResult{MTU: 1500, Hops: 4, Reached: true, Route: "r"})
	got := render(m)
	L := func(d string) string {
		return `continuum_link_probe_path_hops{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp"}`
	}
	if got[L("forward")] != "9" || got[L("reverse")] != "11" {
		t.Errorf("hops per direction: %q / %q", got[L("forward")], got[L("reverse")])
	}
	if hasKey(got, "continuum_link_path_hops") {
		t.Error("the path probe still exports its own hop count")
	}
}

func TestAFailedRunWithdrawsTheReadingsOfTheLastGoodOne(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100, Lost: 0, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, Hops: 5})
	got := render(m)
	up := `continuum_link_probe_up{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`
	if got[up] != "1" || !hasKey(got, "probe_loss_ratio{") {
		t.Fatalf("after a good run: %v", got)
	}
	if _, ok := got[`continuum_link_probe_up{cluster="cl-me",peer="cl-a",direction="reverse",method="owamp"}`]; ok {
		t.Error("up shown for a direction never tried")
	}
	m.ProbeFailed("cl-a", Forward, "timeout")
	got = render(m)
	if got[up] != "0" {
		t.Fatalf("up = %q after a failure", got[up])
	}
	for k := range got {
		if strings.Contains(k, "loss_ratio{") || strings.Contains(k, "delay_variation") || strings.Contains(k, "one_way_delay") || strings.Contains(k, "clock_") || strings.Contains(k, "path_hops") {
			t.Errorf("a reading of the earlier run is still shown: %s", k)
		}
	}
	if got[`continuum_link_probe_packets_sent_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp"}`] != "100" || !hasKey(got, "last_success") {
		t.Errorf("the totals or the last success were lost: %v", got)
	}
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100})
	if got = render(m); got[up] != "1" || !hasKey(got, "probe_loss_ratio{") {
		t.Errorf("not back after a good run: %v", got)
	}
}

func probeMeter(t *testing.T) *Meter {
	t.Helper()
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m := NewMeter(p)
	m.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	return m
}

func TestProbeSeriesAreSilentUntilEnabledAndCountPerDirection(t *testing.T) {
	m := probeMeter(t)
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100, Lost: 5, HasDelay: true, Min: 0.01, Median: 0.012, Max: 0.03, ClockError: 0.0005, HasClockError: true, Hops: 9})
	if got := render(m); strings.Contains(strings.Join(keys(got), "\n"), "probe") {
		t.Fatal("probe series appeared before they were enabled")
	}
	m.EnableProbes()
	m.ObserveProbe("cl-a", Reverse, OwampResult{Sent: 100, Lost: 0, HasDelay: true, Min: 0.02, Median: 0.021, Max: 0.04})
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100, Lost: 10})
	got := render(m)
	L := func(d, extra string) string {
		return `{cluster="cl-me",peer="cl-a",direction="` + d + `",method="owamp"` + extra + `}`
	}
	want := map[string]string{
		"continuum_link_probe_packets_sent_total" + L("forward", ""):              "200",
		"continuum_link_probe_packets_lost_total" + L("forward", ""):              "15",
		"continuum_link_probe_packets_lost_total" + L("reverse", ""):              "0",
		"continuum_link_probe_loss_ratio" + L("forward", ""):                      "0.1",
		"continuum_link_probe_loss_ratio" + L("reverse", ""):                      "0",
		"continuum_link_probe_clock_synchronized" + L("reverse", ""):              "0",
		"continuum_link_probe_last_success_timestamp_seconds" + L("forward", ""):  "1700000000",
		"continuum_link_probe_failures_total" + L("forward", `,reason="timeout"`): "0",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if v, err := strconv.ParseFloat(got["continuum_link_probe_delay_variation_seconds"+L("reverse", `,stat="median"`)], 64); err != nil || math.Abs(v-0.001) > 1e-9 {
		t.Errorf("delay variation = %v (%v)", v, err)
	}
	// the later forward run had no delays, so its absence is not a stale reading of the earlier one
	for k := range got {
		if strings.Contains(k, "probe_path_hops") {
			t.Errorf("a field the latest run did not report is still shown: %s", k)
		}
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAbsoluteDelayIsShownOnlyWhenClocksAgree(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	r := OwampResult{Sent: 10, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02}
	m.ObserveProbe("cl-a", Forward, r)
	if hasKey(render(m), "one_way_delay") {
		t.Fatal("absolute one-way delay shown with unsynchronised clocks")
	}
	if !hasKey(render(m), "delay_variation") {
		t.Fatal("delay variation, which the clock offset cancels out of, is missing")
	}
	r.Synchronized = true
	m.ObserveProbe("cl-a", Forward, r)
	if !hasKey(render(m), "one_way_delay") {
		t.Fatal("absolute one-way delay hidden with synchronised clocks")
	}
}

func hasKey(m map[string]string, sub string) bool {
	for k := range m {
		if strings.Contains(k, sub) {
			return true
		}
	}
	return false
}

func TestProbeFailuresAreCountedByReasonAndUnknownReasonsFoldIntoOther(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	m.ProbeFailed("cl-a", Forward, "timeout")
	m.ProbeFailed("cl-a", Forward, "timeout")
	m.ProbeFailed("cl-a", Forward, "weird\"reason")
	got := render(m)
	base := `continuum_link_probe_failures_total{cluster="cl-me",peer="cl-a",direction="forward",method="owamp",reason="`
	if got[base+`timeout"}`] != "2" || got[base+`other"}`] != "1" {
		t.Fatalf("%v", got)
	}
}

func TestProbesForARemovedPeerGoAndStrangersAreIgnored(t *testing.T) {
	m := probeMeter(t)
	ck := &clock{t: time.Unix(1_700_000_000, 0)}
	m.now = ck.now
	m.EnableProbes()
	m.ObserveProbe("cl-zzz", Forward, OwampResult{Sent: 5}) // not a configured peer
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 5})
	if !hasKey(render(m), "last_success") {
		t.Fatal("no reading recorded")
	}
	m.Peers.Set("cl-me", nil)
	m.Forget()
	ck.add(peerGrace + time.Minute)
	m.Forget()
	m.mu.Lock()
	n := len(m.probes)
	m.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d probe records survived the peer", n)
	}
	var nilMeter *Meter
	nilMeter.ObserveProbe("x", Forward, OwampResult{})
	nilMeter.ProbeFailed("x", Forward, "timeout")
	nilMeter.EnableProbes()
}

func TestProbeOutputIsWellFormedAndEachFamilyHasOneHeader(t *testing.T) {
	m := probeMeter(t)
	m.EnableProbes()
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 10, Lost: 1, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, Hops: 7})
	var b bytes.Buffer
	m.Write(&b)
	typed := map[string]int{}
	sc := bufio.NewScanner(&b)
	for sc.Scan() {
		if l := sc.Text(); strings.HasPrefix(l, "# TYPE ") {
			typed[strings.Fields(l)[2]]++
		}
	}
	for name, n := range typed {
		if n != 1 {
			t.Errorf("%s has %d TYPE headers", name, n)
		}
	}
	if typed["continuum_link_probe_one_way_delay_seconds"] != 1 {
		t.Error("the probe families are missing")
	}
}

// A summary that says the session did not finish describes a half-run test: not a measurement. One that does not say, or says
// 1, is taken as it is, because a version of the tool that leaves the key out must not turn every run into a failure.
func TestASessionThatDidNotFinishIsAFailureAndAMissingKeyIsNot(t *testing.T) {
	body := "SUMMARY\t3.00\nSTART_TIME\t17183736333280792826\nEND_TIME\t17183736352655782137\nLOSS_TIMEOUT\t8591072758\n"
	counts := "SENT\t100\nSYNC\t0\nMAXERR\t4.6e-10\nDUPS\t0\nLOST\t100\n"
	for name, tc := range map[string]struct {
		finished string
		fails    bool
	}{
		"explicit zero": {"SESSION_FINISHED\t0\n", true},
		"explicit one":  {"SESSION_FINISHED\t1\n", false},
		"absent":        {"", false},
	} {
		r, err := ParseOwping(strings.NewReader(body + tc.finished + counts))
		if tc.fails != (err != nil) {
			t.Errorf("%s: err = %v, want failure %v", name, err, tc.fails)
		}
		if tc.fails && !errors.Is(err, errSessionUnfinished) {
			t.Errorf("%s: %v is not errSessionUnfinished", name, err)
		}
		if !tc.fails && (r.Sent != 100 || r.Lost != 100) {
			// every packet lost is a result, not an error: the control connection worked and nothing arrived
			t.Errorf("%s: sent %d lost %d", name, r.Sent, r.Lost)
		}
	}
	// what the log needs comes back with the figures
	r, _ := ParseOwping(strings.NewReader(body + "SESSION_FINISHED\t1\n" + counts))
	want := OwampSession{Finished: "1", Start: "17183736333280792826", End: "17183736352655782137", Sync: "0", MaxErr: "4.6e-10", LossTimeout: "8591072758"}
	if r.Session != want {
		t.Errorf("session = %+v, want %+v", r.Session, want)
	}
	// and nothing in it can carry a control character or run on
	r, _ = ParseOwping(strings.NewReader("SUMMARY\t3\nSENT\t10\nLOST\t10\nSTART_TIME\t12\x1b[31m" + strings.Repeat("9", 100) + "\n"))
	if strings.ContainsAny(r.Session.Start, "\x1b") || len(r.Session.Start) > 24 {
		t.Errorf("an unsafe value was kept: %q", r.Session.Start)
	}
}
