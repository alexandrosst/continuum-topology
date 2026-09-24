//go:build linux && (amd64 || arm64)

package ebpf

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

// These tests load the real program into the running kernel and count real connections on the loopback
// interface. They are skipped, with the reason, where the kernel or the privileges do not allow it.
func open(t *testing.T) *Observer { return openWith(t, Options{}) }

func openWith(t *testing.T, opts Options) *Observer {
	t.Helper()
	o, err := Open(opts)
	if err != nil {
		// Only a machine that cannot run the program at all may skip. Where it should work (root, kernel
		// BTF present) a load failure is a real failure, not something to hide behind a skip.
		if _, statErr := os.Stat("/sys/kernel/btf/vmlinux"); os.Geteuid() != 0 || statErr != nil {
			t.Skipf("eBPF observer unavailable here: %v", err)
		}
		t.Fatalf("the program should load here (root, BTF present): %v", err)
	}
	t.Cleanup(func() { o.Close() })
	return o
}

// exchange opens n connections; each sends out bytes to the server and gets back in bytes.
func exchange(t *testing.T, ln net.Listener, n, out, in int) {
	t.Helper()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, out)
				if _, err := io.ReadFull(c, buf); err != nil {
					return
				}
				c.Write(make([]byte, in))
			}()
		}
	}()
	for i := 0; i < n; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.Write(make([]byte, out))
		if _, err := io.ReadFull(c, make([]byte, in)); err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
}

func collectFor(t *testing.T, o *Observer, port uint32, want int) (client, server *continuumv1.RawFlow) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var cn, sn uint64
	for time.Now().Before(deadline) && (cn < uint64(want) || sn < uint64(want)) {
		flows, _, err := o.Collect()
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range flows {
			if f.Port != port {
				continue
			}
			tgt := &server
			if f.Client {
				tgt = &client
			}
			if *tgt == nil {
				*tgt = f
			} else {
				(*tgt).Connections += f.Connections
				(*tgt).BytesOut += f.BytesOut
				(*tgt).BytesIn += f.BytesIn
			}
		}
		cn, sn = 0, 0
		if client != nil {
			cn = client.Connections
		}
		if server != nil {
			sn = server.Connections
		}
		time.Sleep(200 * time.Millisecond)
	}
	return
}

func TestCountsRealConnectionsAndBytes(t *testing.T) {
	o := open(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)

	const n, out, in = 5, 1234, 4321
	exchange(t, ln, n, out, in)
	client, server := collectFor(t, o, port, n)

	if client == nil || server == nil {
		t.Fatalf("expected a client and a server observation, got client=%v server=%v", client, server)
	}
	for name, f := range map[string]*continuumv1.RawFlow{"client": client, "server": server} {
		if f.Connections != n {
			t.Errorf("%s: connections = %d, want %d", name, f.Connections, n)
		}
		// The kernel counts in TCP sequence space, so a connection can carry one byte more than its
		// payload in each direction: the FIN takes a sequence number (the SYN is left out). More is an error.
		slack := uint64(n)
		if f.BytesOut < n*out || f.BytesOut > n*out+slack || f.BytesIn < n*in || f.BytesIn > n*in+slack {
			t.Errorf("%s: bytes out/in = %d/%d, want %d/%d plus at most %d (from the caller's point of view)", name, f.BytesOut, f.BytesIn, n*out, n*in, slack)
		}
		if f.LocalIp != "127.0.0.1" || f.PeerIp != "127.0.0.1" || f.Protocol != "tcp" {
			t.Errorf("%s: addresses = %s -> %s %s", name, f.LocalIp, f.PeerIp, f.Protocol)
		}
	}
}

// The loopback route always resolves to the "lo" device, so this is a real, kernel-verified check that
// put_iface's pointer chase (sk -> sk_dst_cache -> dst_entry.dev -> net_device.ifindex/name) actually reads
// what it is supposed to, not just that it compiles and the verifier accepts it.
func TestReportsTheInterface(t *testing.T) {
	o := open(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	exchange(t, ln, 1, 10, 10)
	client, server := collectFor(t, o, port, 1)
	for name, f := range map[string]*continuumv1.RawFlow{"client": client, "server": server} {
		if f == nil {
			t.Fatalf("%s: no observation", name)
		}
		if f.Iface != "lo" {
			t.Errorf("%s: iface = %q, want \"lo\"", name, f.Iface)
		}
	}
}

func TestCollectEmptiesTheCounters(t *testing.T) {
	o := open(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	exchange(t, ln, 2, 10, 10)
	if c, _ := collectFor(t, o, port, 2); c == nil {
		t.Fatal("nothing counted")
	}
	time.Sleep(300 * time.Millisecond)
	flows, _, _ := o.Collect()
	for _, f := range flows {
		if f.Port == port {
			t.Errorf("counters were not reset: %+v", f)
		}
	}
}

func TestConnectionsOpenBeforeTheObserverAreReportedAsLost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); accepted <- c }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	srv := <-accepted

	o := open(t) // loaded after the connection was established
	c.Close()
	srv.Close()
	deadline := time.Now().Add(5 * time.Second)
	var lost uint64
	for time.Now().Before(deadline) && lost == 0 {
		_, l, _ := o.Collect()
		lost += l
		time.Sleep(200 * time.Millisecond)
	}
	if lost == 0 {
		t.Error("closing a connection the observer never saw should be reported as lost, not silently ignored")
	}
}

func TestCountsIPv6(t *testing.T) {
	o := open(t)
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	exchange(t, ln, 3, 100, 200)
	client, server := collectFor(t, o, port, 3)
	if client == nil || server == nil {
		t.Fatalf("expected both roles, got client=%v server=%v", client, server)
	}
	if client.LocalIp != "::1" || client.PeerIp != "::1" || client.Connections != 3 {
		t.Errorf("client = %+v", client)
	}
}

// A connection that stays open must show its traffic while it is open, and the total must still be exact
// once it closes: nothing lost between snapshots, nothing counted twice.
func TestLiveCountingOfAConnectionThatStaysOpen(t *testing.T) {
	o := openWith(t, Options{Live: true})
	if !o.Live() {
		t.Fatalf("live counting was asked for and is not running: %v", o.LiveErr)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	srvc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); srvc <- c }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	srv := <-srvc

	send := func(from, to net.Conn, n int) {
		if _, err := from.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(to, make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	send(c, srv, 5000)
	send(srv, c, 7000)
	time.Sleep(200 * time.Millisecond) // let the sender's ACKs come back: bytes_acked counts acknowledged bytes

	sum := func(fl []*continuumv1.RawFlow) (client *continuumv1.RawFlow) {
		for _, f := range fl {
			if f.Port == port && f.Client {
				if client == nil {
					client = &continuumv1.RawFlow{Client: true, Port: f.Port, Connections: f.Connections, BytesOut: f.BytesOut, BytesIn: f.BytesIn}
				} else {
					client.Connections += f.Connections
					client.BytesOut += f.BytesOut
					client.BytesIn += f.BytesIn
				}
			}
		}
		return
	}

	fl, _, err := o.Collect()
	if err != nil {
		t.Fatal(err)
	}
	first := sum(fl)
	if first == nil || first.Connections != 1 {
		t.Fatalf("an open connection must already be counted: %+v", first)
	}
	if first.BytesOut != 5000 || first.BytesIn != 7000 {
		t.Errorf("open connection bytes out/in = %d/%d, want 5000/7000", first.BytesOut, first.BytesIn)
	}

	// More traffic, seen by the next window; the connection is not counted again.
	send(c, srv, 300)
	time.Sleep(200 * time.Millisecond)
	fl, _, _ = o.Collect()
	second := sum(fl)
	if second == nil || second.Connections != 0 || second.BytesOut != 300 || second.BytesIn != 0 {
		t.Errorf("second window = %+v, want no new connection and 300 bytes out", second)
	}

	c.Close()
	srv.Close()
	time.Sleep(500 * time.Millisecond)
	fl, _, _ = o.Collect()
	last := sum(fl)
	// Only the FIN's sequence number can remain, at most one byte per direction.
	if last != nil && (last.Connections != 0 || last.BytesOut > 1 || last.BytesIn > 1) {
		t.Errorf("closing added %+v; everything but a FIN byte was already counted", last)
	}
}

func TestWithoutLiveCountingBytesArriveAtClose(t *testing.T) {
	o := open(t)
	if o.Live() {
		t.Fatal("live counting must be opt-in")
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	srvc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); srvc <- c }()
	c, _ := net.Dial("tcp", ln.Addr().String())
	srv := <-srvc
	c.Write(make([]byte, 1000))
	io.ReadFull(srv, make([]byte, 1000))
	time.Sleep(200 * time.Millisecond)
	fl, _, _ := o.Collect()
	for _, f := range fl {
		if f.Port == port && f.Client && (f.BytesOut != 0 || f.Connections != 1) {
			t.Errorf("before close: %+v (the connection is counted, its bytes are not yet)", f)
		}
	}
	c.Close()
	srv.Close()
}
