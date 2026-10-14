package link

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func connectFor(t *testing.T, dial DialFunc, peers ...*continuumv1.LinkPeer) *TCPConnect {
	t.Helper()
	p := NewPeers()
	p.Set("cl-me", peers)
	m := NewMeter(p)
	m.OnlyProbes()
	m.EnableConnects()
	c := &TCPConnect{Meter: m, Dial: dial, Port: 8861}
	c.Defaults()
	return c
}

// listener accepts and closes connections on a loopback port; dialing any address returns a connection to it.
func listener(t *testing.T) DialFunc {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback listener:", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}
}

func TestAConnectionThatOpensIsTimedAndCounted(t *testing.T) {
	c := connectFor(t, listener(t), peer("cl-a", "A", "10.0.0.1"))
	c.Round(context.Background())
	c.Round(context.Background())
	got := render(c.Meter)
	if got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "2" {
		t.Fatalf("two connections should be counted: %v", got)
	}
	if got[`continuum_link_tcp_connect_seconds_bucket{cluster="cl-me",peer="cl-a",le="+Inf"}`] != "2" {
		t.Fatalf("buckets: %v", got)
	}
	for _, r := range failReasons {
		if got[`continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="`+r+`"}`] != "0" {
			t.Fatalf("no failure expected for %s: %v", r, got)
		}
	}
	if _, ok := got[`continuum_link_tcp_connect_last_success_timestamp_seconds{cluster="cl-me",peer="cl-a"}`]; !ok {
		t.Fatal("last success missing")
	}
}

func TestFailuresAreCountedByTheirReason(t *testing.T) {
	cases := map[string]error{
		"refused":     &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED},
		"unreachable": &net.OpError{Op: "dial", Err: syscall.EHOSTUNREACH},
		"reset":       &net.OpError{Op: "dial", Err: syscall.ECONNRESET},
		"timeout":     context.DeadlineExceeded,
		"other":       errors.New("boom"),
	}
	for reason, err := range cases {
		c := connectFor(t, func(context.Context, string, string) (net.Conn, error) { return nil, err }, peer("cl-a", "A", "10.0.0.1"))
		c.Round(context.Background())
		got := render(c.Meter)
		key := `continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="` + reason + `"}`
		if got[key] != "1" {
			t.Errorf("%s: want one failure, got %v", reason, got)
		}
		if got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "0" {
			t.Errorf("%s: a failure must not be timed", reason)
		}
		if _, ok := got[`continuum_link_tcp_connect_last_success_timestamp_seconds{cluster="cl-me",peer="cl-a"}`]; ok {
			t.Errorf("%s: no success to report", reason)
		}
	}
}

func TestADownNodeIsSkippedNotCountedAsALinkFailure(t *testing.T) {
	good := listener(t)
	var mu sync.Mutex
	tried := map[string]int{}
	dial := func(ctx context.Context, n, addr string) (net.Conn, error) {
		mu.Lock()
		tried[addr]++
		mu.Unlock()
		if strings.HasPrefix(addr, "10.0.0.1:") {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return good(ctx, n, addr)
	}
	c := connectFor(t, dial, peer("cl-a", "A", "10.0.0.1", "10.0.0.2"))
	for i := 0; i < 4; i++ {
		c.Round(context.Background())
	}
	got := render(c.Meter)
	if got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "4" {
		t.Fatalf("the second address answers every time: %v", got)
	}
	if got[`continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="refused"}`] != "0" {
		t.Fatalf("a down node is not a link failure: %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if tried["10.0.0.2:8861"] != 4 || tried["10.0.0.1:8861"] > 1 {
		t.Fatalf("the address that answered should be tried first from then on: %v", tried)
	}
}

func TestAPeerThatIsNotProbedIsNotTimed(t *testing.T) {
	called := false
	lp := peer("cl-a", "A", "10.0.0.1")
	lp.NoProbe = true
	c := connectFor(t, func(context.Context, string, string) (net.Conn, error) { called = true; return nil, errors.New("x") }, lp)
	c.Round(context.Background())
	if called {
		t.Fatal("no connection should be opened for a pair with probing off")
	}
	for k := range render(c.Meter) {
		if strings.Contains(k, "tcp_connect") {
			t.Fatalf("a pair with probing off has no connect series: %s", k)
		}
	}
}

func TestConnectSeriesAreAbsentUntilTurnedOn(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m := NewMeter(p)
	m.EnableProbes()
	for k := range render(m) {
		if strings.Contains(k, "tcp_connect") {
			t.Fatalf("a cluster that does not time connections must not report them: %s", k)
		}
	}
}

func TestConnectSettingsAreChecked(t *testing.T) {
	for name, c := range map[string]*TCPConnect{
		"port":    {Port: 70000, Every: 15 * time.Second, Timeout: time.Second},
		"every":   {Port: 861, Every: time.Second, Timeout: 500 * time.Millisecond},
		"timeout": {Port: 861, Every: 15 * time.Second, Timeout: time.Millisecond},
		"order":   {Port: 861, Every: 6 * time.Second, Timeout: 6 * time.Second},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	c := &TCPConnect{}
	c.Defaults()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestAPeerTheOtherClusterTimesIsNotTimedHere(t *testing.T) {
	var mu sync.Mutex
	dialed := map[string]bool{}
	served := peer("cl-a", "A", "10.0.0.1")
	served.SkipProbe = true
	c := connectFor(t, func(_ context.Context, _, addr string) (net.Conn, error) {
		mu.Lock()
		dialed[addr] = true
		mu.Unlock()
		return nil, errors.New("x")
	}, served, peer("cl-b", "B", "10.0.1.1"))
	c.Round(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if dialed["10.0.0.1:8861"] || !dialed["10.0.1.1:8861"] {
		t.Fatalf("dialed %v: the pair the other cluster times must be left alone and the other one not", dialed)
	}
	for k := range render(c.Meter) {
		if strings.Contains(k, `peer="cl-a"`) && strings.Contains(k, "tcp_connect") {
			t.Fatalf("a connect series for a pair timed by the other cluster: %s", k)
		}
	}
}

// A handshake that queues behind a saturated link is the load, not the baseline: while a throughput test runs nothing is
// recorded, neither a sample nor a failure, and the series are otherwise unchanged.
func TestNoSampleIsTakenWhileAThroughputTestRuns(t *testing.T) {
	dialed := 0
	fail := false
	c := connectFor(t, func(ctx context.Context, n, a string) (net.Conn, error) {
		dialed++
		if fail {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}
		return listener(t)(ctx, n, a)
	}, peer("cl-a", "A", "10.0.0.1"))
	c.Testing = &Flag{}

	exit := c.Testing.Enter()
	c.Round(context.Background())
	fail = true
	c.Round(context.Background())
	exit()
	if dialed != 0 {
		t.Errorf("%d connections were opened during a test", dialed)
	}
	got := render(c.Meter)
	if got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "0" {
		t.Errorf("a sample was taken during a test: %v", got)
	}
	for _, r := range connectFailReasons {
		if got[`continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="`+r+`"}`] != "0" {
			t.Errorf("a failure (%s) was counted during a test", r)
		}
	}

	// Once the test is over, rounds count again, successes and failures alike.
	fail = false
	c.Round(context.Background())
	fail = true
	c.Round(context.Background())
	got = render(c.Meter)
	if got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "1" ||
		got[`continuum_link_tcp_connect_failures_total{cluster="cl-me",peer="cl-a",reason="refused"}`] != "1" {
		t.Errorf("after the test: %v", got)
	}
}

// A test that starts or ends while a handshake is in flight taints that sample too.
func TestASampleThatOverlapsAThroughputTestStartIsDropped(t *testing.T) {
	var c *TCPConnect
	c = connectFor(t, func(ctx context.Context, n, a string) (net.Conn, error) {
		exit := c.Testing.Enter() // the test starts, and is over again, before this handshake returns
		exit()
		return listener(t)(ctx, n, a)
	}, peer("cl-a", "A", "10.0.0.1"))
	c.Testing = &Flag{}
	c.Round(context.Background())
	if got := render(c.Meter); got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "0" {
		t.Errorf("a sample that overlapped a test was kept: %v", got)
	}
	c.Dial = listener(t)
	c.Round(context.Background())
	if got := render(c.Meter); got[`continuum_link_tcp_connect_seconds_count{cluster="cl-me",peer="cl-a"}`] != "1" {
		t.Errorf("the next, clean sample was not taken: %v", got)
	}
}

func TestWhatIsRememberedForAGonePeerGoesAfterTheGrace(t *testing.T) {
	c := connectFor(t, listener(t), peer("cl-a", "A", "10.0.0.1"))
	c.Round(context.Background())
	if c.good["cl-a"] == "" {
		t.Fatal("the answering address was not remembered")
	}
	t0 := time.Unix(1_700_000_000, 0)
	c.forget(nil, t0)
	c.forget(nil, t0.Add(peerGrace-time.Minute))
	if c.good["cl-a"] == "" {
		t.Error("forgotten inside the grace")
	}
	c.forget([]Peer{{ID: "cl-a"}}, t0.Add(peerGrace-time.Second)) // back in time
	c.forget(nil, t0.Add(peerGrace))
	c.forget(nil, t0.Add(peerGrace+time.Minute))
	if c.good["cl-a"] == "" {
		t.Error("a return inside the grace did not restart it")
	}
	c.forget(nil, t0.Add(2*peerGrace+2*time.Minute))
	if len(c.good) != 0 || len(c.failed) != 0 || len(c.absent) != 0 {
		t.Errorf("left: %v %v %v", c.good, c.failed, c.absent)
	}
}

func TestFailuresAreLoggedAsWarningsOnlyWhenTheyChangeAndRoundsThatOverrunAreSaid(t *testing.T) {
	var buf bytes.Buffer
	var err error
	c := connectFor(t, func(context.Context, string, string) (net.Conn, error) { return nil, err }, peer("cl-a", "A", "10.0.0.1"))
	c.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	count := func(s string) int { return strings.Count(buf.String(), s) }

	err = &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	c.Round(context.Background())
	c.Round(context.Background())
	if count("level=WARN") != 1 || count("level=DEBUG") != 1 {
		t.Fatalf("a steady failure should warn once and then be quiet:\n%s", buf.String())
	}
	err = context.DeadlineExceeded
	c.Round(context.Background())
	if count("level=WARN") != 2 {
		t.Errorf("a new reason should warn:\n%s", buf.String())
	}
	err = nil
	c.Dial = listener(t)
	c.Round(context.Background())
	if count("works again") != 1 {
		t.Errorf("recovery was not said:\n%s", buf.String())
	}

	buf.Reset()
	c.noteRound(context.Background(), 2*c.Every)
	c.noteRound(context.Background(), 3*c.Every)
	if count("longer than the interval") != 1 || count("level=WARN") != 1 {
		t.Errorf("an overrun should be warned once:\n%s", buf.String())
	}
	c.noteRound(context.Background(), c.Every/2)
	if count("fit the interval again") != 1 {
		t.Errorf("the end of the overrun was not said:\n%s", buf.String())
	}
}
