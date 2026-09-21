package measure

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestCheckTargetRefusesWhatMustNeverBeProbed(t *testing.T) {
	for _, h := range []string{"127.0.0.1", "127.5.5.5", "::1", "169.254.169.254", "fe80::1", "0.0.0.0", "224.0.0.1", "ff02::1"} {
		if err := CheckTarget(h, 443); err == nil {
			t.Errorf("%s must be refused", h)
		}
	}
	for _, c := range []struct {
		h string
		p int
	}{{"", 80}, {"a b", 80}, {"http://x", 80}, {"x/y", 80}, {"host@x", 80}, {"ok.example.com", 0}, {"ok.example.com", 70000}, {"bad$host", 80}} {
		if CheckTarget(c.h, c.p) == nil {
			t.Errorf("%q:%d must be refused", c.h, c.p)
		}
	}
	for _, h := range []string{"10.1.2.3", "192.168.0.10", "203.0.113.7", "edge-patras.example.com", "2001:db8::1", "my_host.internal"} {
		if err := CheckTarget(h, 443); err != nil {
			t.Errorf("%s: %v", h, err)
		}
	}
}

func TestProbeTimesConnectionsToARealListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// The address is loopback, which Probe refuses; the dialer stands in for the network but is
	// handed the address Probe chose, so we can still check what it would have connected to.
	var dialed string
	real := func(ctx context.Context, network, addr string) (net.Conn, error) {
		dialed = addr
		var d net.Dialer
		return d.DialContext(ctx, network, ln.Addr().String())
	}
	res, err := Probe(context.Background(), Target{ID: "t", Host: "203.0.113.9", Port: 4433}, 5, time.Second, real, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dialed != "203.0.113.9:4433" {
		t.Fatalf("dialed %s", dialed)
	}
	if res.Samples != 5 || res.Failed != 0 || res.P50 <= 0 || res.Min > res.P50 || res.P50 > res.P95 {
		t.Fatalf("%+v", res)
	}
}

func TestProbeCountsFailures(t *testing.T) {
	down := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, errors.New("refused") }
	res, err := Probe(context.Background(), Target{ID: "t", Host: "203.0.113.9", Port: 1}, 4, time.Second, down, nil)
	if err != nil || res.Failed != 4 || res.P50 != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestANameThatResolvesToAForbiddenAddressIsRefused(t *testing.T) {
	resolve := func(ctx context.Context, h string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("203.0.113.1"), netip.MustParseAddr("169.254.169.254")}, nil
	}
	called := false
	dial := func(ctx context.Context, n, a string) (net.Conn, error) { called = true; return nil, errors.New("no") }
	if _, err := Probe(context.Background(), Target{Host: "rebind.example.com", Port: 80}, 2, time.Second, dial, resolve); err == nil {
		t.Fatal("DNS rebinding to the metadata address must be refused")
	}
	if called {
		t.Fatal("nothing may be dialed")
	}
	// a name that does not resolve is simply unreachable
	none := func(ctx context.Context, h string) ([]netip.Addr, error) { return nil, errors.New("nxdomain") }
	res, err := Probe(context.Background(), Target{ID: "x", Host: "nope.example.com", Port: 80}, 3, time.Second, dial, none)
	if err != nil || res.Failed != 3 {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRoundLimitsTargetsAndCountsRefusals(t *testing.T) {
	ok := func(ctx context.Context, n, a string) (net.Conn, error) {
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	var ts []Target
	for i := 0; i < MaxTargets+5; i++ {
		ts = append(ts, Target{ID: "t", Host: "203.0.113.1", Port: 80 + i})
	}
	ts[0] = Target{ID: "bad", Host: "127.0.0.1", Port: 80}
	res, refused := Round(context.Background(), ts, ok, nil)
	if refused != 5+1 || len(res) != MaxTargets-1 {
		t.Fatalf("results %d refused %d", len(res), refused)
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if percentile(s, 0.5) != 5 || percentile(s, 0.95) != 10 || percentile(nil, 0.5) != 0 || percentile([]float64{7}, 0.95) != 7 {
		t.Fatal(percentile(s, 0.5), percentile(s, 0.95))
	}
}

func TestControlPlanePortsAreRefusedByDefault(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	for _, port := range []int{6443, 10250, 10255, 2379, 2380, 10257, 10259} {
		for _, h := range []string{"10.1.2.3", "203.0.113.7", "db.example.com"} {
			if err := CheckTarget(h, port); err == nil {
				t.Errorf("%s:%d must be refused", h, port)
			}
		}
	}
	// Neighbouring and ordinary ports are fine.
	for _, port := range []int{443, 6444, 10251, 2378, 8080} {
		if err := CheckTarget("10.1.2.3", port); err != nil {
			t.Errorf("port %d: %v", port, err)
		}
	}
}

func TestTheAPIServerAddressIsRefusedByDefault(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	for _, h := range []string{"10.96.0.1"} {
		if err := CheckTarget(h, 443); err == nil {
			t.Errorf("%s (the API server's ClusterIP) must be refused", h)
		}
	}
	if err := CheckTarget("10.96.0.2", 443); err != nil {
		t.Errorf("another address: %v", err)
	}
	// Also when a name resolves to it, and the mapped form is the same address.
	res := func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("192.0.2.9"), netip.MustParseAddr("::ffff:10.96.0.1")}, nil
	}
	if _, err := Probe(context.Background(), Target{ID: "x", Host: "apiserver.example.com", Port: 443}, 1, time.Second, nil, res); err == nil {
		t.Error("a name that resolves to the API server must be refused")
	}
	// A named API server host.
	t.Setenv("KUBERNETES_SERVICE_HOST", "api.internal.example.com")
	if err := CheckTarget("API.internal.example.com.", 443); err == nil {
		t.Error("the API server's name must be refused")
	}
}

func TestPolicyOverridesLiftOnlyWhatTheyName(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.96.0.1")
	p := Policy{AllowPorts: []int{10250}, AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("10.96.0.0/16")}}
	if err := p.CheckTarget("10.1.2.3", 10250); err != nil {
		t.Errorf("allowed port: %v", err)
	}
	if err := p.CheckTarget("10.1.2.3", 6443); err == nil {
		t.Error("only 10250 was allowed")
	}
	if err := p.CheckTarget("10.96.0.1", 443); err != nil {
		t.Errorf("the API server's range was allowed: %v", err)
	}
	if err := (Policy{AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/12")}}).CheckTarget("10.96.0.1", 443); err == nil {
		t.Error("an unrelated range must not lift it")
	}
	// The hard block cannot be overridden.
	hard := Policy{AllowPorts: []int{80}, AllowCIDRs: []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}}
	for _, h := range []string{"127.0.0.1", "169.254.169.254"} {
		if err := hard.CheckTarget(h, 80); err == nil {
			t.Errorf("%s must stay refused", h)
		}
	}
	// SetPolicy changes what the package functions do.
	SetPolicy(Policy{AllowPorts: []int{2379}})
	defer SetPolicy(Policy{})
	if err := CheckTarget("10.1.2.3", 2379); err != nil {
		t.Errorf("SetPolicy: %v", err)
	}
}

func TestRoundCountsControlPlaneTargetsAsRefused(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) { return nil, errors.New("down") }
	_, refused := Round(context.Background(), []Target{{ID: "a", Host: "10.1.1.1", Port: 6443}, {ID: "b", Host: "10.1.1.1", Port: 10250}}, dial, nil)
	if refused != 2 {
		t.Fatalf("refused = %d", refused)
	}
}
