package link

import (
	"bufio"
	"bytes"
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
	if to.ClockError <= 0 {
		t.Errorf("clock error = %v", to.ClockError)
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
	r, err := ParseOwping(strings.NewReader("noise\nSUMMARY\t3.00\nSENT\t50\nLOST\t50\nSYNC\t1\nMIN\t0.01\nPDV\tnan\nMINTTL\t250\nMAXERR\tbad\n<BUCKETS>\n\t1\t5\n</BUCKETS>\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.HasDelay {
		t.Error("a run that lost everything claimed a delay")
	}
	if r.HasJitter {
		t.Error("nan accepted as jitter")
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
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 100, Lost: 5, HasDelay: true, Min: 0.01, Median: 0.012, Max: 0.03, Jitter: 0.002, HasJitter: true, ClockError: 0.0005, Hops: 9})
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
		if strings.Contains(k, "probe_path_hops") || strings.Contains(k, "probe_jitter") {
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
	m.EnableProbes()
	m.ObserveProbe("cl-zzz", Forward, OwampResult{Sent: 5}) // not a configured peer
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 5})
	if !hasKey(render(m), "last_success") {
		t.Fatal("no reading recorded")
	}
	m.Peers.Set("cl-me", nil)
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
	m.ObserveProbe("cl-a", Forward, OwampResult{Sent: 10, Lost: 1, HasDelay: true, Min: 0.01, Median: 0.011, Max: 0.02, Synchronized: true, Hops: 7, HasJitter: true, Jitter: 0.001})
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
