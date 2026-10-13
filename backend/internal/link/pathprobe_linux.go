//go:build linux

package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// PathProber finds the route length and the path MTU to an address the way tracepath does, without any privilege: UDP
// packets with the don't-fragment bit set and a rising TTL, and the ICMP errors that routers send back read from the
// socket's error queue (IP_RECVERR). Each step tells the next router's address (time exceeded), a smaller MTU (fragmentation
// needed / packet too big) or that the destination was reached (port unreachable).
type PathProber struct {
	// HopTimeout is how long an answer is waited for at each hop.
	HopTimeout time.Duration
	// MaxHops bounds the route length.
	MaxHops int
	// DstPort is a UDP port nothing listens on, so the destination answers "port unreachable" (the traceroute default).
	DstPort int
	// SrcPort, when not zero, is the local port to send from, so that routers which balance by flow keep choosing the same
	// route from one probe to the next. It falls back to any port if it is taken.
	SrcPort int
	// SilentLimit is how many silent hops in a row end the trace.
	SilentLimit int
}

// pathIPv6Tested gates the IPv6 half of the prober. It follows the same steps with the ICMPv6 equivalents, but it has only been
// run against IPv4 paths (the kernel it was built on has no IPv6), so it stays off until someone has watched it work.
const pathIPv6Tested = false

const (
	minMTUv4 = 576
	minMTUv6 = 1280
)

// Probe traces the path to dst.
func (p PathProber) Probe(ctx context.Context, dst netip.Addr) (PathResult, error) {
	p = p.withDefaults()
	dst = dst.Unmap()
	if !dst.IsValid() || dst.IsUnspecified() || dst.IsMulticast() {
		return PathResult{}, errors.New("not a unicast address")
	}
	// A loopback path has no routers: there is no route or MTU to find, and the interface MTU (65536) does not even fit a UDP
	// datagram.
	if dst.IsLoopback() {
		return PathResult{}, ErrPathUnsupported
	}
	v6 := dst.Is6()
	if v6 && !pathIPv6Tested {
		return PathResult{}, ErrPathUnsupported
	}
	fam, level, optRecvErr, optMTUDisc, optMTU, optTTL, probeMode := unix.AF_INET, unix.IPPROTO_IP, unix.IP_RECVERR, unix.IP_MTU_DISCOVER, unix.IP_MTU, unix.IP_TTL, unix.IP_PMTUDISC_PROBE
	hdr, minMTU := 20+8, minMTUv4
	if v6 {
		fam, level, optRecvErr, optMTUDisc, optMTU, optTTL, probeMode = unix.AF_INET6, unix.IPPROTO_IPV6, unix.IPV6_RECVERR, unix.IPV6_MTU_DISCOVER, unix.IPV6_MTU, unix.IPV6_UNICAST_HOPS, unix.IPV6_PMTUDISC_PROBE
		hdr, minMTU = 40+8, minMTUv6
	}
	fd, err := unix.Socket(fam, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC|unix.SOCK_NONBLOCK, 0)
	if err != nil {
		return PathResult{}, fmt.Errorf("socket: %w", err)
	}
	defer unix.Close(fd)
	for _, o := range []struct{ opt, val int }{{optRecvErr, 1}, {optMTUDisc, probeMode}} {
		if err := unix.SetsockoptInt(fd, level, o.opt, o.val); err != nil {
			return PathResult{}, fmt.Errorf("setsockopt: %w", err)
		}
	}
	if p.SrcPort != 0 {
		var sa unix.Sockaddr = &unix.SockaddrInet4{Port: p.SrcPort}
		if v6 {
			sa = &unix.SockaddrInet6{Port: p.SrcPort}
		}
		_ = unix.Bind(fd, sa) // a port that is taken is not an error: any port will do
	}
	var to unix.Sockaddr
	if v6 {
		to = &unix.SockaddrInet6{Port: p.DstPort, Addr: dst.As16()}
	} else {
		to = &unix.SockaddrInet4{Port: p.DstPort, Addr: dst.As4()}
	}
	if err := unix.Connect(fd, to); err != nil {
		return PathResult{}, fmt.Errorf("connect: %w", err)
	}
	mtu, err := unix.GetsockoptInt(fd, level, optMTU)
	if err != nil || mtu < minMTU {
		return PathResult{}, fmt.Errorf("no route to %s", dst)
	}
	// Start from the outgoing interface's own MTU, not from the kernel's idea of this route: that already holds whatever an
	// earlier packet-too-big taught it, for minutes after, which would hide both a path that has improved and a black hole.
	if im := interfaceMTU(fd); im >= minMTU {
		mtu = im
	}
	if !v6 && mtu > 65535 { // the largest IPv4 packet; a bigger MTU (loopback) would make every write fail
		mtu = 65535
	}
	c := &udpConn{p: p, fd: fd, v6: v6, level: level, optMTU: optMTU, optTTL: optTTL, hdr: hdr, run: byte(time.Now().UnixNano() >> 7)}
	return p.trace(ctx, c, mtu, minMTU)
}

func (p PathProber) withDefaults() PathProber {
	if p.HopTimeout <= 0 {
		p.HopTimeout = 700 * time.Millisecond
	}
	if p.MaxHops <= 0 || p.MaxHops > 64 {
		p.MaxHops = 30
	}
	if p.DstPort == 0 {
		p.DstPort = 33434
	}
	if p.SilentLimit <= 0 {
		p.SilentLimit = 5
	}
	return p
}

// hopAttempts is how many full-size probes a hop gets before it counts as silent: a router that rate-limits ICMP, or one lost
// reply, must not make the route look different from one run to the next, nor look like a black hole.
const hopAttempts = 3

// probeConn is the socket as the trace uses it, so that the trace can be driven by a fake in tests.
type probeConn interface {
	// send sends a probe of size bytes (IP header included) with this ttl and says which sequence number it carries.
	send(ttl, size int) (seq uint16, err error)
	// await waits for the answer to that very probe; answers to earlier probes are skipped. info is the MTU a router asked for.
	await(ctx context.Context, ttl int, seq uint16) (ev pathEvent, from netip.Addr, info int, err error)
	// pathMTU is the kernel's idea of the path MTU, or an error.
	pathMTU() (int, error)
}

// trace walks the path with probes of mtu bytes (lowered whenever a router asks for it). Any error ends the trace without a
// result: a route cut short by a socket error or a cancelled context says nothing about the path.
func (p PathProber) trace(ctx context.Context, c probeConn, mtu, minMTU int) (PathResult, error) {
	p = p.withDefaults()
	res := PathResult{}
	var hops []netip.Addr
	silent := 0
	learned := false // a router (or the kernel) told us a MTU: otherwise mtu is only this host's own interface
	// One step: send a probe at ttl and say what came back. The MTU is lowered, and the probe resent, as often as routers
	// ask for it.
	step := func(ttl int) (ev pathEvent, addr netip.Addr, err error) {
		for tries := 0; tries < 16; tries++ {
			seq, serr := c.send(ttl, mtu)
			if serr != nil {
				if errors.Is(serr, unix.EMSGSIZE) {
					m2, e2 := c.pathMTU()
					if e2 != nil || m2 < minMTU || m2 >= mtu {
						return evOther, netip.Addr{}, errors.New("path MTU below the minimum")
					}
					mtu, learned = m2, true
					continue
				}
				return evOther, netip.Addr{}, serr
			}
			e, a, info, werr := c.await(ctx, ttl, seq)
			if werr != nil {
				return evOther, netip.Addr{}, werr
			}
			if e == evFragNeeded {
				if info >= minMTU && info < mtu {
					mtu, learned = info, true
					continue
				}
				// A router said "too big" without a usable size: take the kernel's idea of the path MTU, if it is smaller.
				if m2, e2 := c.pathMTU(); e2 == nil && m2 >= minMTU && m2 < mtu {
					mtu, learned = m2, true
					continue
				}
				return evOther, a, nil
			}
			return e, a, nil
		}
		return evOther, netip.Addr{}, errors.New("path MTU kept changing")
	}
	// A hop is silent only when it did not answer several probes in a row.
	hop := func(ttl int) (ev pathEvent, addr netip.Addr, err error) {
		for attempt := 0; attempt < hopAttempts; attempt++ {
			if ev, addr, err = step(ttl); err != nil || ev != evTimeout {
				return ev, addr, err
			}
			if ctx.Err() != nil {
				return evOther, netip.Addr{}, ctx.Err()
			}
		}
		return evTimeout, netip.Addr{}, nil
	}

trace:
	for ttl := 1; ttl <= p.MaxHops; ttl++ {
		if ctx.Err() != nil {
			return PathResult{}, ctx.Err()
		}
		ev, addr, err := hop(ttl)
		if err != nil {
			return PathResult{}, err
		}
		switch ev {
		case evTimeExceeded:
			silent = 0
			hops = append(hops, addr)
		case evReached:
			res.Reached, res.Hops = true, ttl
			hops = append(hops, addr)
			res.MTU = mtu // a full-size probe got to the destination
			res.Route = routeFingerprint(hops)
			return res, nil
		case evTimeout:
			silent++
			hops = append(hops, netip.Addr{})
			// Did this hop swallow every full-size packet while it passes a small one? Then nothing ever told this host the MTU
			// is smaller further on: a black hole, the reason big transfers stall on a link whose pings work. The small probe
			// carries its own sequence number, so a late answer to a full-size one cannot be taken for it.
			if silent == 1 && mtu > minMTU+200 {
				seq, serr := c.send(ttl, minMTU)
				if serr != nil {
					return PathResult{}, serr
				}
				e2, _, _, werr := c.await(ctx, ttl, seq)
				if werr != nil {
					return PathResult{}, werr
				}
				if e2 == evTimeExceeded || e2 == evReached {
					// No MTU and no route: the trace stopped here, and a black hole is not a route to compare.
					res.Blackhole = true
					return res, nil
				}
			}
			if silent >= p.SilentLimit {
				break trace
			}
		default:
			silent++
			hops = append(hops, netip.Addr{})
		}
	}
	// The destination did not answer: the route is cut short, so it is not offered as a fingerprint, and the MTU is only
	// reported if a router (or the kernel) lowered it. The interface's own MTU says nothing about the path.
	if learned {
		res.MTU = mtu
	}
	return res, nil
}

// udpConn is the probe socket.
type udpConn struct {
	p                     PathProber
	fd                    int
	v6                    bool
	level, optMTU, optTTL int
	hdr                   int // IP and UDP headers
	run                   byte
	seq                   uint16
}

// Each probe's payload starts with a tag the ICMP error brings back: the run, the ttl and the probe's sequence number.
const tagLen = 4

func (c *udpConn) send(ttl, size int) (uint16, error) {
	if err := unix.SetsockoptInt(c.fd, c.level, c.optTTL, ttl); err != nil {
		return 0, err
	}
	c.seq++
	buf := make([]byte, size-c.hdr)
	buf[0], buf[1] = c.run, byte(ttl)
	binary.BigEndian.PutUint16(buf[2:tagLen], c.seq)
	// A late or duplicate error left in the queue keeps the socket's pending error set, and that fails the next write.
	c.drain()
	_, err := unix.Write(c.fd, buf)
	if errors.Is(err, unix.ECONNREFUSED) || errors.Is(err, unix.EHOSTUNREACH) || errors.Is(err, unix.ENETUNREACH) {
		c.drain()
		_, err = unix.Write(c.fd, buf)
	}
	return c.seq, err
}

func (c *udpConn) pathMTU() (int, error) { return unix.GetsockoptInt(c.fd, c.level, c.optMTU) }

// drain empties the error queue and clears the socket's pending error.
func (c *udpConn) drain() {
	pkt, oob := make([]byte, 64), make([]byte, 512)
	for i := 0; i < 64; i++ {
		if _, _, _, _, err := unix.Recvmsg(c.fd, pkt, oob, unix.MSG_ERRQUEUE|unix.MSG_DONTWAIT); err != nil {
			break
		}
	}
	_, _ = unix.GetsockoptInt(c.fd, unix.SOL_SOCKET, unix.SO_ERROR)
}

// await waits for the ICMP error that answers the probe of this run, ttl and sequence number. Errors for earlier probes that
// arrive late are skipped. info is the MTU a router asked for.
func (c *udpConn) await(ctx context.Context, ttl int, seq uint16) (ev pathEvent, from netip.Addr, info int, err error) {
	deadline := time.Now().Add(c.p.HopTimeout)
	pkt, oob := make([]byte, 64), make([]byte, 512)
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return evTimeout, netip.Addr{}, 0, nil
		}
		if ctx.Err() != nil {
			return evOther, netip.Addr{}, 0, ctx.Err()
		}
		wait := int(left / time.Millisecond)
		if wait > 100 {
			wait = 100
		}
		// Only the error queue is read, so only POLLERR is asked for: a normal datagram waiting on the socket would keep
		// POLLIN set and turn the wait into a spin.
		fds := []unix.PollFd{{Fd: int32(c.fd), Events: unix.POLLERR}}
		n, perr := unix.Poll(fds, wait)
		if perr != nil && !errors.Is(perr, unix.EINTR) {
			return evOther, netip.Addr{}, 0, perr
		}
		if n <= 0 {
			continue
		}
		rn, oobn, _, _, rerr := unix.Recvmsg(c.fd, pkt, oob, unix.MSG_ERRQUEUE)
		if rerr != nil {
			if errors.Is(rerr, unix.EAGAIN) {
				// An error is flagged but nothing is queued: clear it, or the poll returns at once again.
				_, _ = unix.GetsockoptInt(c.fd, unix.SOL_SOCKET, unix.SO_ERROR)
				continue
			}
			if errors.Is(rerr, unix.EINTR) {
				continue
			}
			return evOther, netip.Addr{}, 0, rerr
		}
		cmsgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
		if perr != nil {
			continue
		}
		for _, cm := range cmsgs {
			e, a, i, ok := parseRecvErr(cm, c.v6)
			if !ok {
				continue
			}
			// The original datagram comes back with the error: ignore answers to other probes.
			if !tagMatches(pkt[:rn], c.run, byte(ttl), seq) {
				continue
			}
			return e, a, i, nil
		}
	}
}

// tagMatches says whether the part of the probe that an ICMP error quoted belongs to the probe with this run, ttl and
// sequence number. RFC 792 only asks a router to quote the IP header and 8 more bytes, which is just the UDP header: with
// nothing of the payload there is no tag to compare, and the answer is taken (probes are sent one at a time, and the error
// queue is emptied before each, so a stale answer without a tag is rare). A quote of 2 or 3 bytes carries run and ttl only.
func tagMatches(quoted []byte, run, ttl byte, seq uint16) bool {
	if len(quoted) < 2 {
		return true
	}
	if quoted[0] != run || quoted[1] != ttl {
		return false
	}
	if len(quoted) >= tagLen {
		return binary.BigEndian.Uint16(quoted[2:tagLen]) == seq
	}
	return true
}

// interfaceMTU is the MTU of the interface the socket's local address belongs to, or 0. One address dump finds the interface
// index, instead of one dump per interface.
func interfaceMTU(fd int) int {
	sa, err := unix.Getsockname(fd)
	if err != nil {
		return 0
	}
	var local netip.Addr
	switch a := sa.(type) {
	case *unix.SockaddrInet4:
		local = netip.AddrFrom4(a.Addr)
	case *unix.SockaddrInet6:
		local = netip.AddrFrom16(a.Addr)
	default:
		return 0
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		return 0
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return 0
	}
	idx := addrInterfaceIndex(msgs, local.Unmap())
	if idx == 0 {
		return 0
	}
	ifc, err := net.InterfaceByIndex(idx)
	if err != nil {
		return 0
	}
	return ifc.MTU
}

// addrInterfaceIndex finds the index of the interface that owns the address in a dump of RTM_NEWADDR messages, or 0.
func addrInterfaceIndex(msgs []syscall.NetlinkMessage, local netip.Addr) int {
	for i := range msgs {
		m := &msgs[i]
		if m.Header.Type != syscall.RTM_NEWADDR || len(m.Data) < syscall.SizeofIfAddrmsg {
			continue
		}
		attrs, err := syscall.ParseNetlinkRouteAttr(m)
		if err != nil {
			continue
		}
		// IFA_LOCAL is the address itself; IFA_ADDRESS is the far end on a point-to-point link, and the address elsewhere.
		var addr, lcl netip.Addr
		for _, at := range attrs {
			a, ok := netip.AddrFromSlice(at.Value)
			switch {
			case !ok:
			case at.Attr.Type == syscall.IFA_LOCAL:
				lcl = a.Unmap()
			case at.Attr.Type == syscall.IFA_ADDRESS:
				addr = a.Unmap()
			}
		}
		if lcl.IsValid() {
			addr = lcl
		}
		if addr == local {
			return int(binary.NativeEndian.Uint32(m.Data[4:8])) // ifa_index
		}
	}
	return 0
}

type pathEvent int

const (
	evOther pathEvent = iota
	evTimeout
	evTimeExceeded
	evReached
	evFragNeeded
)

// parseRecvErr reads one IP_RECVERR / IPV6_RECVERR control message: a sock_extended_err followed by the address of the
// router (or host) that sent the ICMP message.
func parseRecvErr(c unix.SocketControlMessage, v6 bool) (ev pathEvent, from netip.Addr, info int, ok bool) {
	wantLevel, wantType := unix.IPPROTO_IP, unix.IP_RECVERR
	icmpOrigin := uint8(2) // SO_EE_ORIGIN_ICMP
	if v6 {
		wantLevel, wantType = unix.IPPROTO_IPV6, unix.IPV6_RECVERR
		icmpOrigin = 3 // SO_EE_ORIGIN_ICMP6
	}
	if int(c.Header.Level) != wantLevel || int(c.Header.Type) != wantType || len(c.Data) < 16 {
		return evOther, netip.Addr{}, 0, false
	}
	origin, typ, code := c.Data[4], c.Data[5], c.Data[6]
	info = int(binary.NativeEndian.Uint32(c.Data[8:12]))
	if origin != icmpOrigin {
		return evOther, netip.Addr{}, 0, false
	}
	rest := c.Data[16:]
	if v6 && len(rest) >= 24 { // sockaddr_in6: family(2) port(2) flowinfo(4) addr(16) scope(4)
		from = netip.AddrFrom16([16]byte(rest[8:24]))
	} else if !v6 && len(rest) >= 8 { // sockaddr_in: family(2) port(2) addr(4)
		from = netip.AddrFrom4([4]byte(rest[4:8]))
	}
	if v6 {
		switch {
		case typ == 3: // time exceeded
			return evTimeExceeded, from, 0, true
		case typ == 1 && code == 4: // destination unreachable: port unreachable
			return evReached, from, 0, true
		case typ == 2: // packet too big
			return evFragNeeded, from, info, true
		}
		return evOther, from, 0, true
	}
	switch {
	case typ == 11: // time exceeded
		return evTimeExceeded, from, 0, true
	case typ == 3 && code == 3: // port unreachable
		return evReached, from, 0, true
	case typ == 3 && code == 4: // fragmentation needed and DF set
		return evFragNeeded, from, info, true
	}
	return evOther, from, 0, true
}
