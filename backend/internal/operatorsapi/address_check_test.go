package operatorsapi

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ikhnos/internal/kernel"
	"ikhnos/internal/testkit"
)

func TestAddressWarningsSayWhatLooksWrongAndNothingElse(t *testing.T) {
	has := func(ws []string, sub string) bool {
		for _, w := range ws {
			if strings.Contains(w, sub) {
				return true
			}
		}
		return false
	}
	for _, c := range []struct {
		name string
		addr string
		svc  *kernel.KubeService
		want []string // a warning containing each of these
		none bool     // no warning at all
	}{
		{name: "nothing recorded", addr: "", none: true},
		{name: "a public name on the gRPC port", addr: "otlp.example.com:4317", none: true},
		{name: "the HTTP port, which every printed command's gRPC export cannot use", addr: "otlp.example.com:4318", want: []string{"OTLP/HTTP"}},
		{name: "a private address", addr: "10.1.2.3:4317", want: []string{"private address"}},
		{name: "an IPv6 unique-local address", addr: "[fd00::5]:4317", want: []string{"private address"}},
		{name: "a ClusterIP gateway with an address nobody forwards", addr: "203.0.113.7:4317", svc: &kernel.KubeService{Type: "ClusterIP", Port: 4317}, want: []string{"ClusterIP", "listens only inside this cluster"}},
		{name: "a NodePort gateway at 4317, where no node listens", addr: "198.51.100.4:4317", svc: &kernel.KubeService{Type: "NodePort", Port: 4317, NodePort: 31317}, want: []string{"31317"}},
		{name: "a NodePort gateway at its node port", addr: "198.51.100.4:31317", svc: &kernel.KubeService{Type: "NodePort", Port: 4317, NodePort: 31317}, none: true},
		{name: "a load balancer with no address yet", addr: "203.0.113.7:4317", svc: &kernel.KubeService{Type: "LoadBalancer", Port: 4317}, want: []string{"no address yet"}},
		{name: "a load balancer that reports another address", addr: "203.0.113.7:4317", svc: &kernel.KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.99:4317"}, want: []string{"203.0.113.99:4317"}},
		{name: "a load balancer at its address", addr: "203.0.113.99:4317", svc: &kernel.KubeService{Type: "LoadBalancer", Port: 4317, LoadBalancer: "203.0.113.99:4317"}, none: true},
	} {
		got := AddressWarnings(c.addr, c.svc)
		if c.none && len(got) != 0 {
			t.Errorf("%s: warned %v", c.name, got)
		}
		for _, w := range c.want {
			if !has(got, w) {
				t.Errorf("%s: no warning mentions %q: %v", c.name, w, got)
			}
		}
	}
}

// The probe dials whatever address the administrator recorded, so a name that merely resolves to this server's own
// network must not turn "check this address" into a port scan of it.
func TestAddressCheckRefusesNamesThatResolveToTheServersOwnNetwork(t *testing.T) {
	var target atomic.Value
	target.Store(net.ParseIP("127.0.0.1"))
	dns := testkit.FakeDNS(t, func() net.IP { return target.Load().(net.IP) })
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp", dns)
	}}

	var hits atomic.Int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits.Add(1)
			c.Close()
		}
	}()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	look := func(dp *kernel.DeciderPolicy, addr string) AddressProbe {
		return ProbeAddress(context.Background(), addr, nil, "", time.Now(), ProbeDialer(dp))
	}

	name := "receiver.rebind.test.:" + port
	got := look(&kernel.DeciderPolicy{Resolver: res}, name)
	if got.State != "blocked" {
		t.Fatalf("a name that resolves to loopback: %+v", got)
	}
	if strings.Contains(got.Message, "127.0.0.1") {
		t.Fatalf("the message names the address it resolved to: %q", got.Message)
	}
	if hits.Load() != 0 {
		t.Fatal("the loopback listener received a connection")
	}
	// An IP literal goes through the same check.
	if got := look(nil, "127.0.0.1:"+port); got.State != "blocked" || hits.Load() != 0 {
		t.Fatalf("a loopback literal: %+v", got)
	}

	// Cloud metadata is refused even when the operator opened every range.
	target.Store(net.ParseIP("169.254.169.254"))
	all, err := kernel.NewDeciderPolicy("0.0.0.0/0")
	if err != nil {
		t.Fatal(err)
	}
	all.Resolver = res
	if got := look(all, "receiver.rebind.test.:80"); got.State != "blocked" {
		t.Fatalf("a name that resolves to the metadata address: %+v", got)
	}

	// What the decider policy opens stays open here: the same name is looked at once loopback is listed.
	target.Store(net.ParseIP("127.0.0.1"))
	allowed, err := kernel.NewDeciderPolicy("127.0.0.0/8")
	if err != nil {
		t.Fatal(err)
	}
	allowed.Resolver = res
	if got := look(allowed, name); got.State != "reachable" {
		t.Fatalf("loopback listed in the decider policy: %+v", got)
	}

	// Receivers on private addresses are still checked by default.
	p := probePolicy(nil)
	for _, s := range []string{"10.1.2.3", "172.16.0.9", "192.168.1.1", "100.64.0.1", "fd00::5"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err != nil {
			t.Errorf("%s should stay probeable: %v", s, err)
		}
	}
	for _, s := range []string{"127.0.0.1", "::1", "169.254.169.254", "fe80::1", "224.0.0.1", "0.0.0.0"} {
		if err := p.CheckAddr(netip.MustParseAddr(s)); err == nil {
			t.Errorf("%s should be refused", s)
		}
	}
}
