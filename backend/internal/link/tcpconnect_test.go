package link

import (
	"context"
	"errors"
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
