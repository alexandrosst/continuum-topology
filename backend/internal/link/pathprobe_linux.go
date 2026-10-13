//go:build linux

package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	dst = dst.Unmap()
	if !dst.IsValid() || dst.IsUnspecified() || dst.IsMulticast() {
		return PathResult{}, errors.New("not a unicast address")
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
	if im := interfaceMTU(fd); im >= minMTU && im < mtu {
		mtu = im
	} else if im > mtu {
		mtu = im
	}
	res := PathResult{}
	var hops []netip.Addr
	runID := byte(time.Now().UnixNano() >> 7)
	silent := 0
	send := func(ttl, size int) error {
		if err := unix.SetsockoptInt(fd, level, optTTL, ttl); err != nil {
			return err
		}
		buf := make([]byte, size-hdr)
		buf[0], buf[1] = runID, byte(ttl)
		_, err := unix.Write(fd, buf)
		return err
	}
	// One step: send a probe at ttl and say what came back. The MTU is lowered, and the probe resent, as often as routers
	// ask for it.
	step := func(ttl int) (ev pathEvent, addr netip.Addr, err error) {
		for tries := 0; tries < 16; tries++ {
			if serr := send(ttl, mtu); serr != nil {
				if errors.Is(serr, unix.EMSGSIZE) {
					m2, e2 := unix.GetsockoptInt(fd, level, optMTU)
					if e2 != nil || m2 < minMTU || m2 >= mtu {
						return evOther, netip.Addr{}, errors.New("path MTU below the minimum")
					}
					mtu = m2
					continue
				}
				if errors.Is(serr, unix.ENETUNREACH) || errors.Is(serr, unix.EHOSTUNREACH) || errors.Is(serr, unix.EPERM) {
					return evOther, netip.Addr{}, serr
				}
				return evOther, netip.Addr{}, serr
			}
			e, a, info, werr := p.await(ctx, fd, v6, runID, byte(ttl))
			if werr != nil {
				return evOther, netip.Addr{}, werr
			}
			if e == evFragNeeded {
				if info >= minMTU && info < mtu {
					mtu = info
					continue
				}
				// A router said "too big" without a usable size: take the kernel's idea of the path MTU, if it is smaller.
				if m2, e2 := unix.GetsockoptInt(fd, level, optMTU); e2 == nil && m2 >= minMTU && m2 < mtu {
					mtu = m2
					continue
				}
				return evOther, a, nil
			}
			return e, a, nil
		}
		return evOther, netip.Addr{}, errors.New("path MTU kept changing")
	}

	for ttl := 1; ttl <= p.MaxHops; ttl++ {
		if ctx.Err() != nil {
			return PathResult{}, ctx.Err()
		}
		ev, addr, err := step(ttl)
		if err != nil {
			if len(hops) == 0 && res.MTU == 0 {
				return PathResult{}, err
			}
			break
		}
		switch ev {
		case evTimeExceeded:
			silent = 0
			hops = append(hops, addr)
		case evReached:
			res.Reached, res.Hops = true, ttl
			hops = append(hops, addr)
			res.MTU = mtu
			res.Route = routeFingerprint(hops)
			return res, nil
		case evTimeout:
			silent++
			hops = append(hops, netip.Addr{})
			// Did this hop swallow the full-size packet while it would have passed a small one? Then nothing ever told this host
			// the MTU is smaller further on: a black hole, the reason big transfers stall on a link whose pings work.
			if mtu > minMTU+200 && silent == 1 {
				if serr := send(ttl, minMTU); serr == nil {
					if e2, _, _, _ := p.await(ctx, fd, v6, runID, byte(ttl)); e2 == evTimeExceeded || e2 == evReached {
						res.Blackhole = true
						res.MTU = 0
						res.Route = routeFingerprint(hops)
						return res, nil
					}
				}
			}
			if silent >= p.SilentLimit {
				ttl = p.MaxHops
			}
		default:
			silent++
			hops = append(hops, netip.Addr{})
		}
	}
	res.MTU = mtu
	if mtu == 0 {
		return PathResult{}, errors.New("no answer from the path")
	}
	res.Route = routeFingerprint(hops)
	return res, nil
}

// interfaceMTU is the MTU of the interface the socket's local address belongs to, or 0.
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
	ifs, err := net.Interfaces()
	if err != nil {
		return 0
	}
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a.String()); err == nil && p.Addr().Unmap() == local.Unmap() {
				return ifc.MTU
			}
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

// await waits for the ICMP error that answers the probe of this run and ttl. Errors for earlier probes that arrive late are
// skipped. info is the MTU a router asked for.
func (p PathProber) await(ctx context.Context, fd int, v6 bool, run, ttl byte) (ev pathEvent, from netip.Addr, info int, err error) {
	deadline := time.Now().Add(p.HopTimeout)
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
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLERR | unix.POLLIN}}
		n, perr := unix.Poll(fds, wait)
		if perr != nil && !errors.Is(perr, unix.EINTR) {
			return evOther, netip.Addr{}, 0, perr
		}
		if n <= 0 {
			continue
		}
		pkt := make([]byte, 64)
		oob := make([]byte, 512)
		_, oobn, _, _, rerr := unix.Recvmsg(fd, pkt, oob, unix.MSG_ERRQUEUE)
		if rerr != nil {
			if errors.Is(rerr, unix.EAGAIN) || errors.Is(rerr, unix.EINTR) {
				continue
			}
			return evOther, netip.Addr{}, 0, rerr
		}
		cmsgs, perr := unix.ParseSocketControlMessage(oob[:oobn])
		if perr != nil {
			continue
		}
		for _, c := range cmsgs {
			e, a, i, ok := parseRecvErr(c, v6)
			if !ok {
				continue
			}
			// The original datagram comes back with the error: ignore answers to other probes.
			if len(pkt) >= 2 && (pkt[0] != run || pkt[1] != ttl) {
				continue
			}
			return e, a, i, nil
		}
	}
}

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
