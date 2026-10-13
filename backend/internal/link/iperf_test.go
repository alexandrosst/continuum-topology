package link

import (
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func iperfFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/iperf3_" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func parseFixture(t *testing.T, name string, dir Direction) (IperfResult, error) {
	t.Helper()
	return ParseIperf(strings.NewReader(iperfFixture(t, name)), dir)
}

func TestATCPTestIsReadOnTheReceivingEnd(t *testing.T) {
	r, err := parseFixture(t, "tcp1", Forward)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := r.Flow(Forward)
	if r.UDP || r.Streams != 1 || len(r.Flows) != 1 || !ok {
		t.Fatalf("%+v", r)
	}
	if math.Abs(f.Bits-13847713674.5054) > 1 {
		t.Errorf("rate = %v: the receiver's figure is the one to report, not the sender's", f.Bits)
	}
	if f.Bytes != 5194383360 || f.Retransmits != 36346 {
		t.Errorf("bytes %d retransmits %d", f.Bytes, f.Retransmits)
	}
	if !f.HasRTT || f.RTT <= 0 || f.RTT > 1 {
		t.Errorf("rtt = %v (%v)", f.RTT, f.HasRTT)
	}
}

func TestAReverseTestIsTheReverseDirection(t *testing.T) {
	r, err := parseFixture(t, "tcp1r", Reverse)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := r.Flow(Reverse)
	if !ok || len(r.Flows) != 1 {
		t.Fatalf("%+v", r)
	}
	if math.Abs(f.Bits-14543531405.560715) > 1 || f.Retransmits != 26052 {
		t.Errorf("%+v", f)
	}
	if f.HasRTT {
		t.Error("a reverse test cannot know the peer's round-trip time: iperf3 sends it only for the client's own streams")
	}
}

func TestParallelStreamsAreOneAggregate(t *testing.T) {
	r, err := parseFixture(t, "tcp4", Forward)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := r.Flow(Forward)
	if r.Streams != 4 || f.Bits < 1e9 || !f.HasRTT {
		t.Fatalf("%+v", r)
	}
}

func TestABidirectionalTestGivesBothDirections(t *testing.T) {
	r, err := parseFixture(t, "bidir", Forward)
	if err != nil {
		t.Fatal(err)
	}
	fw, ok1 := r.Flow(Forward)
	rv, ok2 := r.Flow(Reverse)
	if !ok1 || !ok2 || len(r.Flows) != 2 {
		t.Fatalf("%+v", r)
	}
	if math.Abs(fw.Bits-13711110805.825726) > 1 || math.Abs(rv.Bits-13179824037.026884) > 1 {
		t.Errorf("rates %v / %v", fw.Bits, rv.Bits)
	}
	if fw.Retransmits != 0 || rv.Retransmits != 96 {
		t.Errorf("retransmits belong to each direction's sender: %d / %d", fw.Retransmits, rv.Retransmits)
	}
	if !fw.HasRTT || !rv.HasRTT || fw.RTT == rv.RTT {
		t.Errorf("each direction has its own round-trip time: %v / %v", fw.RTT, rv.RTT)
	}
}

func TestUDPLossAndJitterAreTheReceivers(t *testing.T) {
	r, err := parseFixture(t, "udphi", Forward)
	if err != nil {
		t.Fatal(err)
	}
	f, _ := r.Flow(Forward)
	if !r.UDP || f.Packets != 556319 || f.Lost != 218403 {
		t.Fatalf("%+v", f)
	}
	if got := f.LossRatio(); math.Abs(got-0.39258590844461544) > 1e-9 {
		t.Errorf("loss ratio = %v", got)
	}
	if math.Abs(f.Jitter-0.00231217913431339/1e3) > 1e-12 {
		t.Errorf("jitter = %v seconds", f.Jitter)
	}
	if f.Bytes != 749918012 {
		t.Errorf("the cost is what the sender put on the wire: %d", f.Bytes)
	}
	clean, _ := parseFixture(t, "udp", Forward)
	if c, _ := clean.Flow(Forward); c.Lost != 0 || c.LossRatio() != 0 {
		t.Errorf("%+v", c)
	}
}

func TestAFailureIsReportedAsOneAndAHalfFinishedTestIsNot(t *testing.T) {
	for name, want := range map[string]string{"busy": "busy running a test", "refused": "Connection refused"} {
		_, err := parseFixture(t, name, Forward)
		var ie *IperfError
		if !errors.As(err, &ie) || !strings.Contains(ie.Msg, want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, doc := range map[string]string{
		"empty":       ``,
		"not json":    `iperf3: error`,
		"no summary":  `{"start":{"test_start":{"protocol":"TCP","num_streams":1}},"end":{}}`,
		"no protocol": `{"start":{"test_start":{}},"end":{"sum_sent":{},"sum_received":{}}}`,
		"no packets":  `{"start":{"test_start":{"protocol":"UDP","num_streams":1}},"end":{"sum_sent":{"bytes":1},"sum_received":{"bits_per_second":5}}}`,
		"bidir cut":   `{"start":{"test_start":{"protocol":"TCP","num_streams":1,"bidir":1}},"end":{"sum_sent":{"bytes":1},"sum_received":{"bits_per_second":5}}}`,
		"negative":    `{"start":{"test_start":{"protocol":"TCP","num_streams":1}},"end":{"sum_sent":{"bytes":1},"sum_received":{"bits_per_second":-5}}}`,
	} {
		if _, err := ParseIperf(strings.NewReader(doc), Forward); err == nil {
			t.Errorf("%s accepted", name)
		} else if errors.As(err, new(*IperfError)) {
			t.Errorf("%s: a malformed result is not iperf3's own error", name)
		}
	}
}

func TestRatesAndSizesAreParsed(t *testing.T) {
	for in, want := range map[string]uint64{"": 0, "0": 0, "1500000": 1500000, "512k": 512e3, "200M": 200e6, "1G": 1e9, "2.5G": 2.5e9, " 5m ": 5e6} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("rate %q = %d, %v; want %d", in, got, err, want)
		}
	}
	for in, want := range map[string]uint64{"": 0, "0": 0, "4096": 4096, "10Gi": 10 << 30, "512Mi": 512 << 20, "500MB": 500e6, "2G": 2e9, "1TiB": 1 << 40} {
		if got, err := ParseBytes(in); err != nil || got != want {
			t.Errorf("bytes %q = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"fast", "-5M", "1e99G", "M", "NaN"} {
		if _, err := ParseRate(in); err == nil {
			t.Errorf("rate %q accepted", in)
		}
		if _, err := ParseBytes(in); err == nil {
			t.Errorf("bytes %q accepted", in)
		}
	}
}
