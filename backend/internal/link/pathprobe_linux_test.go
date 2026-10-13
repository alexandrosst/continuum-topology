//go:build linux

package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// With PATHPROBE_DST set (and, for the checks, PATHPROBE_WANT_*) the prober is run against a real path, for instance one
// built from network namespaces with a link of a smaller MTU in the middle. It is skipped otherwise.
func TestAgainstARealPath(t *testing.T) {
	dst := os.Getenv("PATHPROBE_DST")
	if dst == "" {
		t.Skip("PATHPROBE_DST not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := PathProber{SrcPort: 41234}.Probe(ctx, netip.MustParseAddr(dst))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%+v", res)
	if v := os.Getenv("PATHPROBE_WANT_MTU"); v != "" {
		if want, _ := strconv.Atoi(v); res.MTU != want {
			t.Errorf("MTU = %d, want %d", res.MTU, want)
		}
	}
	if v := os.Getenv("PATHPROBE_WANT_HOPS"); v != "" {
		if want, _ := strconv.Atoi(v); res.Hops != want || !res.Reached {
			t.Errorf("hops = %d reached = %v, want %d", res.Hops, res.Reached, want)
		}
	}
	if v := os.Getenv("PATHPROBE_WANT_BLACKHOLE"); v == "1" && !res.Blackhole {
		t.Error("a black hole was not detected")
	}
}

// ---- parseRecvErr: the byte layout of a sock_extended_err followed by the sockaddr of the router ----

// recvErr builds the control message the kernel hands out for IP_RECVERR / IPV6_RECVERR: struct sock_extended_err (errno u32,
// origin u8, type u8, code u8, pad u8, info u32, data u32) followed by the offender's sockaddr.
func recvErr(level, typ int, origin, icmpType, icmpCode uint8, info uint32, sockaddr []byte) unix.SocketControlMessage {
	d := make([]byte, 16, 16+len(sockaddr))
	binary.NativeEndian.PutUint32(d[0:4], uint32(unix.EHOSTUNREACH))
	d[4], d[5], d[6] = origin, icmpType, icmpCode
	binary.NativeEndian.PutUint32(d[8:12], info)
	d = append(d, sockaddr...)
	return unix.SocketControlMessage{Header: unix.Cmsghdr{Level: int32(level), Type: int32(typ)}, Data: d}
}

// sockaddrIn is a struct sockaddr_in: family, port, address, 8 bytes of padding.
func sockaddrIn(ip [4]byte) []byte {
	b := make([]byte, 16)
	binary.NativeEndian.PutUint16(b[0:2], unix.AF_INET)
	copy(b[4:8], ip[:])
	return b
}

// sockaddrIn6 is a struct sockaddr_in6: family, port, flowinfo, address, scope id.
func sockaddrIn6(ip [16]byte) []byte {
	b := make([]byte, 28)
	binary.NativeEndian.PutUint16(b[0:2], unix.AF_INET6)
	copy(b[8:24], ip[:])
	return b
}

func TestParseRecvErr(t *testing.T) {
	r1 := [4]byte{192, 0, 2, 7}
	r6 := netip.MustParseAddr("2001:db8::7").As16()
	v4 := func(origin, typ, code uint8, info uint32, sa []byte) unix.SocketControlMessage {
		return recvErr(unix.IPPROTO_IP, unix.IP_RECVERR, origin, typ, code, info, sa)
	}
	v6 := func(origin, typ, code uint8, info uint32, sa []byte) unix.SocketControlMessage {
		return recvErr(unix.IPPROTO_IPV6, unix.IPV6_RECVERR, origin, typ, code, info, sa)
	}
	for _, c := range []struct {
		name string
		cm   unix.SocketControlMessage
		v6   bool
		ev   pathEvent
		from string
		info int
		ok   bool
	}{
		{"v4 time exceeded", v4(2, 11, 0, 0, sockaddrIn(r1)), false, evTimeExceeded, "192.0.2.7", 0, true},
		{"v4 port unreachable", v4(2, 3, 3, 0, sockaddrIn(r1)), false, evReached, "192.0.2.7", 0, true},
		{"v4 fragmentation needed carries the MTU", v4(2, 3, 4, 1400, sockaddrIn(r1)), false, evFragNeeded, "192.0.2.7", 1400, true},
		{"v4 host unreachable is some other answer", v4(2, 3, 1, 0, sockaddrIn(r1)), false, evOther, "192.0.2.7", 0, true},
		{"v6 time exceeded", v6(3, 3, 0, 0, sockaddrIn6(r6)), true, evTimeExceeded, "2001:db8::7", 0, true},
		{"v6 port unreachable", v6(3, 1, 4, 0, sockaddrIn6(r6)), true, evReached, "2001:db8::7", 0, true},
		{"v6 packet too big carries the MTU", v6(3, 2, 0, 1280, sockaddrIn6(r6)), true, evFragNeeded, "2001:db8::7", 1280, true},
		{"not from ICMP (a local error)", v4(1, 11, 0, 0, sockaddrIn(r1)), false, evOther, "", 0, false},
		{"v6 origin on a v4 socket", v4(3, 11, 0, 0, sockaddrIn(r1)), false, evOther, "", 0, false},
		{"wrong level", recvErr(unix.IPPROTO_IPV6, unix.IP_RECVERR, 2, 11, 0, 0, sockaddrIn(r1)), false, evOther, "", 0, false},
		{"wrong type", recvErr(unix.IPPROTO_IP, unix.IP_TTL, 2, 11, 0, 0, sockaddrIn(r1)), false, evOther, "", 0, false},
		{"shorter than a sock_extended_err", unix.SocketControlMessage{Header: unix.Cmsghdr{Level: unix.IPPROTO_IP, Type: unix.IP_RECVERR}, Data: make([]byte, 15)}, false, evOther, "", 0, false},
		{"truncated sockaddr leaves the router unknown", v4(2, 11, 0, 0, sockaddrIn(r1)[:7]), false, evTimeExceeded, "", 0, true},
		{"no sockaddr at all", v4(2, 11, 0, 0, nil), false, evTimeExceeded, "", 0, true},
		{"truncated v6 sockaddr", v6(3, 3, 0, 0, sockaddrIn6(r6)[:23]), true, evTimeExceeded, "", 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ev, from, info, ok := parseRecvErr(c.cm, c.v6)
			if ok != c.ok || ev != c.ev || info != c.info {
				t.Fatalf("got ev=%v info=%d ok=%v, want ev=%v info=%d ok=%v", ev, info, ok, c.ev, c.info, c.ok)
			}
			if c.from == "" && from.IsValid() || c.from != "" && from.String() != c.from {
				t.Fatalf("router = %v, want %q", from, c.from)
			}
		})
	}
}

func TestTheTagOfAProbeSurvivesShortQuotes(t *testing.T) {
	const run, ttl, seq = 0x5a, 7, 0x0102
	full := []byte{run, ttl, 0x01, 0x02, 0, 0}
	for _, c := range []struct {
		name   string
		quoted []byte
		want   bool
	}{
		{"all of the tag", full, true},
		{"another probe's sequence number (a late answer)", []byte{run, ttl, 0x01, 0x01}, false},
		{"another ttl", []byte{run, ttl + 1, 0x01, 0x02}, false},
		{"another run", []byte{run + 1, ttl, 0x01, 0x02}, false},
		{"only run and ttl were quoted", full[:3], true},
		{"only run and ttl were quoted, but another ttl", []byte{run, ttl + 1, 0x01}, false},
		{"nothing of the payload was quoted (IP header plus 8 bytes)", nil, true},
		{"one byte", full[:1], true},
	} {
		if got := tagMatches(c.quoted, run, ttl, seq); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// ---- the trace, driven by a fake socket ----

type sentProbe struct{ ttl, size, nth int }

// fakeConn answers each probe from a script: answer gets the ttl, the size and how many probes with that ttl and size there
// were before, and says what comes back.
type fakeConn struct {
	answer func(p sentProbe) (pathEvent, netip.Addr, int)
	sendFn func(p sentProbe) error
	kernel int

	sent    []sentProbe // by sequence number - 1
	awaited []int       // the sequence numbers awaited
	counts  map[[2]int]int
}

func (f *fakeConn) send(ttl, size int) (uint16, error) {
	if f.counts == nil {
		f.counts = map[[2]int]int{}
	}
	k := [2]int{ttl, size}
	p := sentProbe{ttl, size, f.counts[k]}
	f.counts[k]++
	f.sent = append(f.sent, p)
	if f.sendFn != nil {
		if err := f.sendFn(p); err != nil {
			return 0, err
		}
	}
	return uint16(len(f.sent)), nil
}

func (f *fakeConn) await(_ context.Context, ttl int, seq uint16) (pathEvent, netip.Addr, int, error) {
	f.awaited = append(f.awaited, int(seq))
	p := f.sent[seq-1]
	if p.ttl != ttl {
		return evOther, netip.Addr{}, 0, fmt.Errorf("awaited ttl %d for a probe sent at ttl %d", ttl, p.ttl)
	}
	ev, a, info := f.answer(p)
	return ev, a, info, nil
}

func (f *fakeConn) pathMTU() (int, error) {
	if f.kernel == 0 {
		return 0, unix.ENOENT
	}
	return f.kernel, nil
}

var fakeRoute = []netip.Addr{a("10.0.0.1"), a("172.16.0.1"), a("192.0.2.9")}

// routed answers like a three-router path whose last router is the destination, unless the script says otherwise.
func routed(special func(p sentProbe) (ev pathEvent, handled bool)) func(sentProbe) (pathEvent, netip.Addr, int) {
	return func(p sentProbe) (pathEvent, netip.Addr, int) {
		if special != nil {
			if ev, ok := special(p); ok {
				if ev == evTimeout {
					return evTimeout, netip.Addr{}, 0
				}
				return ev, fakeRoute[p.ttl-1], 0
			}
		}
		if p.ttl >= len(fakeRoute) {
			return evReached, fakeRoute[len(fakeRoute)-1], 0
		}
		return evTimeExceeded, fakeRoute[p.ttl-1], 0
	}
}

func newFakePathProber() PathProber { return PathProber{SilentLimit: 5, MaxHops: 30} }

func TestARouterThatAnswersLateOrRarelyDoesNotChangeTheRouteFingerprint(t *testing.T) {
	clean := &fakeConn{answer: routed(nil)}
	ref, err := newFakePathProber().trace(context.Background(), clean, 1500, 576)
	if err != nil || !ref.Reached || ref.Hops != 3 || ref.Route == "" || ref.MTU != 1500 {
		t.Fatalf("%+v %v", ref, err)
	}
	// the middle router misses the first two probes (rate limiting, or a reply that came after the timeout)
	flaky := &fakeConn{answer: routed(func(p sentProbe) (pathEvent, bool) {
		if p.ttl == 2 && p.nth < 2 {
			return evTimeout, true
		}
		return 0, false
	})}
	got, err := newFakePathProber().trace(context.Background(), flaky, 1500, 576)
	if err != nil {
		t.Fatal(err)
	}
	if got.Blackhole || !got.Reached || got.Route != ref.Route || got.Hops != 3 {
		t.Fatalf("a router that answered on the third probe changed the result: %+v vs %+v", got, ref)
	}
	for _, p := range flaky.sent {
		if p.size != 1500 {
			t.Errorf("a %d byte probe was sent although every hop answered a full-size one", p.size)
		}
	}
}

func TestABlackHoleNeedsEveryFullSizeProbeSilentAndTheSmallOneAnswered(t *testing.T) {
	swallowsBig := func(p sentProbe) (pathEvent, bool) {
		if p.ttl == 2 && p.size > 600 {
			return evTimeout, true
		}
		return 0, false
	}
	f := &fakeConn{answer: routed(swallowsBig)}
	got, err := newFakePathProber().trace(context.Background(), f, 1500, 576)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blackhole || got.MTU != 0 || got.Reached || got.Route != "" {
		t.Fatalf("%+v", got)
	}
	big := 0
	for _, p := range f.sent {
		if p.ttl == 2 && p.size == 1500 {
			big++
		}
	}
	if big != hopAttempts {
		t.Errorf("%d full-size probes at the suspect hop, want %d", big, hopAttempts)
	}
	last := f.sent[len(f.sent)-1]
	if last.size != 576 || f.awaited[len(f.awaited)-1] != len(f.sent) {
		t.Errorf("the verdict must rest on the answer to the small probe itself: last=%+v awaited=%v of %d sent", last, f.awaited, len(f.sent))
	}

	// One lost full-size probe, then it answers: not a black hole, however the small probe would fare.
	lost := &fakeConn{answer: routed(func(p sentProbe) (pathEvent, bool) {
		if p.ttl == 2 && p.size == 1500 && p.nth == 0 {
			return evTimeout, true
		}
		return 0, false
	})}
	got, err = newFakePathProber().trace(context.Background(), lost, 1500, 576)
	if err != nil || got.Blackhole || !got.Reached || got.MTU != 1500 {
		t.Fatalf("one lost probe was taken for a black hole: %+v %v", got, err)
	}
	for _, p := range lost.sent {
		if p.size != 1500 {
			t.Errorf("the small probe was sent although the hop answered a full-size one: %+v", p)
		}
	}

	// A router that answers nothing at all is not a black hole either.
	mute := &fakeConn{answer: routed(func(p sentProbe) (pathEvent, bool) {
		if p.ttl >= 2 {
			return evTimeout, true
		}
		return 0, false
	})}
	got, err = newFakePathProber().trace(context.Background(), mute, 1500, 576)
	if err != nil || got.Blackhole || got.Reached {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestNothingIsClaimedAboutTheMTUOrTheRouteWhenTheDestinationNeverAnswered(t *testing.T) {
	// The first router answers, the rest is firewalled: the interface MTU is all this host knows.
	f := &fakeConn{answer: routed(func(p sentProbe) (pathEvent, bool) {
		if p.ttl >= 2 {
			return evTimeout, true
		}
		return 0, false
	})}
	got, err := newFakePathProber().trace(context.Background(), f, 1500, 576)
	if err != nil {
		t.Fatal(err)
	}
	if got.MTU != 0 || got.Route != "" || got.Reached || got.Blackhole {
		t.Fatalf("%+v", got)
	}

	// A router that asked for a smaller size did teach it.
	f = &fakeConn{answer: func(p sentProbe) (pathEvent, netip.Addr, int) {
		switch {
		case p.ttl == 1 && p.size > 1400:
			return evFragNeeded, fakeRoute[0], 1400
		case p.ttl == 1:
			return evTimeExceeded, fakeRoute[0], 0
		}
		return evTimeout, netip.Addr{}, 0
	}}
	got, err = newFakePathProber().trace(context.Background(), f, 1500, 576)
	if err != nil || got.MTU != 1400 || got.Reached || got.Route != "" {
		t.Fatalf("%+v %v", got, err)
	}

	// So did the kernel, when the write was refused as too big.
	f = &fakeConn{kernel: 1380, answer: routed(nil), sendFn: func(p sentProbe) error {
		if p.size > 1380 {
			return unix.EMSGSIZE
		}
		return nil
	}}
	got, err = newFakePathProber().trace(context.Background(), f, 1500, 576)
	if err != nil || got.MTU != 1380 || !got.Reached {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestAnErrorMidTraceFailsTheProbeInsteadOfReportingAShortRoute(t *testing.T) {
	boom := errors.New("network is unreachable")
	f := &fakeConn{answer: routed(nil), sendFn: func(p sentProbe) error {
		if p.ttl == 3 {
			return boom
		}
		return nil
	}}
	if got, err := newFakePathProber().trace(context.Background(), f, 1500, 576); !errors.Is(err, boom) || got.Route != "" || got.MTU != 0 {
		t.Fatalf("%+v %v", got, err)
	}

	// the same when the context ends in the middle
	ctx, cancel := context.WithCancel(context.Background())
	f = &fakeConn{answer: func(p sentProbe) (pathEvent, netip.Addr, int) {
		if p.ttl == 2 {
			cancel()
		}
		return evTimeExceeded, fakeRoute[p.ttl-1], 0
	}}
	if got, err := newFakePathProber().trace(ctx, f, 1500, 576); !errors.Is(err, context.Canceled) || got.Route != "" {
		t.Fatalf("%+v %v", got, err)
	}

	// and when the kernel cannot say what fits
	f = &fakeConn{answer: routed(nil), sendFn: func(sentProbe) error { return unix.EMSGSIZE }}
	if _, err := newFakePathProber().trace(context.Background(), f, 1500, 576); err == nil {
		t.Fatal("a path below the minimum MTU was not an error")
	}
}

// ---- the real socket, against the loopback ----

func loopbackConn(t *testing.T, port int, timeout time.Duration) *udpConn {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		t.Skipf("no socket: %v", err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_RECVERR, 1); err != nil {
		t.Skipf("no IP_RECVERR: %v", err)
	}
	if err := unix.Connect(fd, &unix.SockaddrInet4{Port: port, Addr: [4]byte{127, 0, 0, 1}}); err != nil {
		t.Skipf("connect: %v", err)
	}
	return &udpConn{p: PathProber{HopTimeout: timeout}, fd: fd, level: unix.IPPROTO_IP, optMTU: unix.IP_MTU, optTTL: unix.IP_TTL, hdr: 28, run: 0x42}
}

// closedUDPPort returns a port nothing listens on.
func closedUDPPort(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	return port
}

func TestTheSocketTakesOnlyTheAnswerToTheProbeItWaitsFor(t *testing.T) {
	c := loopbackConn(t, closedUDPPort(t), 300*time.Millisecond)
	seq, err := c.send(64, 576)
	if err != nil {
		t.Fatal(err)
	}
	// Waiting for a later probe: this answer is a late one for an earlier probe and must not count.
	if ev, _, _, err := c.await(context.Background(), 64, seq+1); err != nil || ev != evTimeout {
		t.Fatalf("the answer to another probe was taken: ev=%v err=%v", ev, err)
	}
	seq, err = c.send(64, 576)
	if err != nil {
		t.Fatal(err)
	}
	ev, from, _, err := c.await(context.Background(), 64, seq)
	if err != nil || ev != evReached || from != a("127.0.0.1") {
		t.Fatalf("ev=%v from=%v err=%v", ev, from, err)
	}
	// Another ttl is another probe too.
	seq, _ = c.send(63, 576)
	if ev, _, _, _ := c.await(context.Background(), 64, seq); ev != evTimeout {
		t.Fatalf("ev=%v", ev)
	}
}

func TestAnErrorLeftInTheQueueDoesNotFailTheNextSend(t *testing.T) {
	c := loopbackConn(t, closedUDPPort(t), 300*time.Millisecond)
	for i := 0; i < 4; i++ { // each answer is left unread: the socket keeps a pending error
		if _, err := c.send(64, 576); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWaitingForAnAnswerDoesNotSpinWhenAnOrdinaryDatagramIsReadable(t *testing.T) {
	srv, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer srv.Close()
	c := loopbackConn(t, srv.LocalAddr().(*net.UDPAddr).Port, 400*time.Millisecond)
	seq, err := c.send(64, 576)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	_, from, err := srv.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.WriteTo([]byte("hello"), from); err != nil { // an ordinary reply: readable, but not an error
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cpu := func() time.Duration {
		var ru unix.Rusage
		if err := unix.Getrusage(unix.RUSAGE_THREAD, &ru); err != nil {
			t.Skip(err)
		}
		return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
	}
	before, start := cpu(), time.Now()
	ev, _, _, err := c.await(context.Background(), 64, seq)
	if err != nil || ev != evTimeout {
		t.Fatalf("ev=%v err=%v", ev, err)
	}
	if used, took := cpu()-before, time.Since(start); used > took/4 {
		t.Fatalf("the wait used %v of CPU in %v", used, took)
	}
}

func TestTheInterfaceMTUIsFoundFromTheLocalAddress(t *testing.T) {
	lo, err := net.InterfaceByName("lo")
	if err != nil {
		t.Skip(err)
	}
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	rc, err := pc.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var got int
	_ = rc.Control(func(fd uintptr) { got = interfaceMTU(int(fd)) })
	if got != lo.MTU {
		t.Fatalf("interfaceMTU = %d, lo has %d", got, lo.MTU)
	}
}

func TestALoopbackAddressIsNotPathProbed(t *testing.T) {
	if _, err := (PathProber{}).Probe(context.Background(), a("127.0.0.1")); !errors.Is(err, ErrPathUnsupported) {
		t.Fatalf("err = %v", err)
	}
}
