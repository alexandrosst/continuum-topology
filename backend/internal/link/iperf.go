package link

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

// IperfFlow is what one direction of an iperf3 test achieved, read on the receiving end (what arrived, which is what a
// link can be said to carry). Bytes is what the sender put on the wire, which is what the test cost.
type IperfFlow struct {
	Direction Direction
	// Bits is the receive rate in bits per second.
	Bits  float64
	Bytes uint64
	// TCP: segments retransmitted by the sender during the test, and the mean round-trip time its kernel measured under the
	// load (seconds; HasRTT is false when the system did not report one).
	Retransmits uint64
	RTT         float64
	HasRTT      bool
	// UDP: datagrams the receiver expected and did not get, and the receiver's jitter (seconds).
	Packets, Lost uint64
	Jitter        float64
}

// LossRatio is the fraction of datagrams that did not arrive (UDP).
func (f IperfFlow) LossRatio() float64 {
	if f.Packets == 0 {
		return 0
	}
	return float64(min(f.Lost, f.Packets)) / float64(f.Packets)
}

// IperfResult is one finished test: one flow, or two for a bidirectional test.
type IperfResult struct {
	UDP     bool
	Streams int
	Flows   []IperfFlow
}

// Flow returns the result for a direction.
func (r IperfResult) Flow(d Direction) (IperfFlow, bool) {
	for _, f := range r.Flows {
		if f.Direction == d {
			return f, true
		}
	}
	return IperfFlow{}, false
}

// IperfError is an iperf3 run that reported a failure (it still prints JSON, with an "error" member).
type IperfError struct{ Msg string }

func (e *IperfError) Error() string { return "iperf3: " + e.Msg }

type ipSum struct {
	Bytes       float64  `json:"bytes"`
	Bits        float64  `json:"bits_per_second"`
	Retransmits *float64 `json:"retransmits"`
	Jitter      float64  `json:"jitter_ms"`
	Lost        float64  `json:"lost_packets"`
	Packets     float64  `json:"packets"`
}

type ipStreamSide struct {
	Sender  bool    `json:"sender"`
	MeanRTT float64 `json:"mean_rtt"` // microseconds
}

type ipDoc struct {
	Error string `json:"error"`
	Start struct {
		TestStart struct {
			Protocol string `json:"protocol"`
			Streams  int    `json:"num_streams"`
			Reverse  int    `json:"reverse"`
			Bidir    int    `json:"bidir"`
		} `json:"test_start"`
	} `json:"start"`
	End struct {
		Streams []struct {
			Sender ipStreamSide `json:"sender"`
		} `json:"streams"`
		SumSent     *ipSum `json:"sum_sent"`
		SumReceived *ipSum `json:"sum_received"`
		BidirSent   *ipSum `json:"sum_sent_bidir_reverse"`
		BidirRecv   *ipSum `json:"sum_received_bidir_reverse"`
	} `json:"end"`
}

// ParseIperf reads the JSON `iperf3 -c ... -J` prints. It returns an *IperfError when iperf3 reported a failure, and an
// ordinary error when the output is not a complete result, so a test that stopped half way is never reported as a slow link.
//
// The numbers of a direction are those of the receiving end: in a normal test that is the server's, in a reverse test the
// client's. A reverse test's flow is Reverse, whichever end sent. dir is the direction the test was asked for.
func ParseIperf(r io.Reader, dir Direction) (IperfResult, error) {
	var d ipDoc
	if err := json.NewDecoder(io.LimitReader(r, 4<<20)).Decode(&d); err != nil {
		return IperfResult{}, fmt.Errorf("not an iperf3 result: %w", err)
	}
	if d.Error != "" {
		return IperfResult{}, &IperfError{Msg: d.Error}
	}
	ts := d.Start.TestStart
	res := IperfResult{UDP: strings.EqualFold(ts.Protocol, "UDP"), Streams: ts.Streams}
	if ts.Protocol == "" || res.Streams < 1 {
		return IperfResult{}, errors.New("iperf3 result does not describe the test")
	}
	if d.End.SumSent == nil || d.End.SumReceived == nil {
		return IperfResult{}, errors.New("iperf3 result has no summary: the test did not finish")
	}
	// rtt is the mean over the streams that carried data in the wanted direction.
	rtt := func(forward bool) (float64, bool) {
		var sum float64
		var n int
		for _, s := range d.End.Streams {
			if s.Sender.Sender == forward && s.Sender.MeanRTT > 0 {
				sum += s.Sender.MeanRTT
				n++
			}
		}
		if n == 0 {
			return 0, false
		}
		return sum / float64(n) / 1e6, true
	}
	flow := func(dir Direction, sent, recv *ipSum, forward bool) (IperfFlow, error) {
		f := IperfFlow{Direction: dir, Bits: recv.Bits, Bytes: num(sent.Bytes)}
		if !finite(recv.Bits, sent.Bytes) || recv.Bits < 0 || sent.Bytes < 0 {
			return f, errors.New("iperf3 result holds an impossible number")
		}
		if res.UDP {
			f.Packets, f.Lost, f.Jitter = num(recv.Packets), num(recv.Lost), recv.Jitter/1e3
			if f.Packets == 0 {
				return f, errors.New("iperf3 result has no datagram count")
			}
			if !finite(recv.Jitter) || f.Jitter < 0 {
				f.Jitter = 0
			}
		} else {
			if sent.Retransmits != nil {
				f.Retransmits = num(*sent.Retransmits)
			}
			f.RTT, f.HasRTT = rtt(forward)
		}
		return f, nil
	}
	// In a reverse test the "sender" is the server; the summaries are still named for the role, so the first pair is the
	// wanted direction either way.
	f, err := flow(dir, d.End.SumSent, d.End.SumReceived, dir == Forward)
	if err != nil {
		return IperfResult{}, err
	}
	res.Flows = append(res.Flows, f)
	if ts.Bidir != 0 {
		if d.End.BidirSent == nil || d.End.BidirRecv == nil {
			return IperfResult{}, errors.New("iperf3 result is missing the second direction of a bidirectional test")
		}
		other := Reverse
		if dir == Reverse {
			other = Forward
		}
		g, err := flow(other, d.End.BidirSent, d.End.BidirRecv, dir != Forward)
		if err != nil {
			return IperfResult{}, err
		}
		res.Flows = append(res.Flows, g)
	}
	return res, nil
}

func finite(vs ...float64) bool {
	for _, v := range vs {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return true
}

// num turns a JSON number into a count; negatives (iperf3 reports a negative loss when datagrams arrive twice) are zero.
func num(v float64) uint64 {
	if v <= 0 || math.IsNaN(v) {
		return 0
	}
	return uint64(v)
}
