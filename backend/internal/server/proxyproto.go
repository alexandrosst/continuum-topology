package server

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// PROXY protocol support for the agent listener (https://www.haproxy.org/download/2.8/doc/proxy-protocol.txt).
//
// TrustAgentProxy exists for the same reason Admin.TrustProxy does, but cannot reuse it: that one reads
// X-Forwarded-For from an HTTP request, which only exists once TLS is already terminated. The agent
// listener does its own TLS handshake on a raw net.Conn (see accept() in grpc.go) so that agents can
// present the client certificate this server issued them, so a TLS-terminating HTTP proxy is not an
// option here without either breaking that certificate check or having the proxy re-issue one, and
// PROXY protocol is how an L4 load balancer or reverse proxy that just forwards the TCP connection
// (AWS NLB, HAProxy, Envoy, nginx's stream module - all can be configured to send it) tells the server
// which address it accepted the connection from before either side starts speaking TLS at all.
//
// Both the human-readable v1 header and the binary v2 header are accepted, since that is the proxy's
// choice to make, not this server's - the two are distinguished by their first bytes.

var (
	proxyV1Prefix = []byte("PROXY ")
	proxyV2Sig    = []byte{0x0D, 0x0A, 0x0D, 0x0A, 0x00, 0x0D, 0x0A, 0x51, 0x55, 0x49, 0x54, 0x0A}
)

// maxProxyV1Line is the v1 spec's own bound on header length, including "PROXY " and the trailing CRLF.
const maxProxyV1Line = 107

// readProxyHeader consumes a PROXY protocol header from the front of conn and returns the source address
// it declares, or ok=false when the header itself is well formed but carries no usable address (v1
// "UNKNOWN", v2 LOCAL - both are legitimate, sent by a load balancer's own health check rather than a
// real client, and the spec says to accept the connection and fall back to the proxy's own address, not
// to reject it). Either way, exactly the header's bytes are read off the wire and nothing more, so the
// TLS handshake that follows sees only what the real client sent.
//
// An error means what arrived does not start with a PROXY protocol header at all, or claims to be one
// and is malformed - the caller must refuse that connection rather than quietly falling back to the
// proxy's own address, because trusting this listener at all is conditional on every connection actually
// going through the proxy; one that skips the header is not proof the proxy was bypassed, but treating it
// as if it went through the proxy would let a real bypass through unnoticed too.
func readProxyHeader(conn net.Conn) (addr net.Addr, ok bool, err error) {
	var sig [12]byte
	if _, err := io.ReadFull(conn, sig[:6]); err != nil {
		return nil, false, fmt.Errorf("reading proxy protocol header: %w", err)
	}
	if bytes.Equal(sig[:6], proxyV1Prefix) {
		return readProxyV1(conn)
	}
	if _, err := io.ReadFull(conn, sig[6:]); err != nil {
		return nil, false, fmt.Errorf("reading proxy protocol header: %w", err)
	}
	if !bytes.Equal(sig[:], proxyV2Sig) {
		return nil, false, errors.New("connection does not start with a PROXY protocol header")
	}
	return readProxyV2(conn)
}

// readProxyV1 reads the rest of a v1 line one byte at a time (a bufio.Reader would risk buffering the TLS
// bytes that follow, which would then be lost to the handshake), up to the CRLF that ends it, and refuses
// to read past the spec's own maximum line length. "PROXY " (6 bytes) has already been consumed.
func readProxyV1(conn net.Conn) (net.Addr, bool, error) {
	line := make([]byte, 0, maxProxyV1Line)
	var b [1]byte
	for {
		if len(line) > maxProxyV1Line-6 {
			return nil, false, errors.New("proxy protocol v1 header exceeds the maximum line length")
		}
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return nil, false, fmt.Errorf("reading proxy protocol v1 header: %w", err)
		}
		if b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	fields := strings.Fields(strings.TrimSuffix(string(line), "\r"))
	if len(fields) == 0 {
		return nil, false, errors.New("empty proxy protocol v1 header")
	}
	switch fields[0] {
	case "UNKNOWN":
		return nil, false, nil
	case "TCP4", "TCP6":
		if len(fields) != 5 {
			return nil, false, fmt.Errorf("proxy protocol v1 %s header has %d fields, want 5", fields[0], len(fields))
		}
		ip := net.ParseIP(fields[1])
		if ip == nil {
			return nil, false, fmt.Errorf("proxy protocol v1 header: %q is not an IP address", fields[1])
		}
		port, err := strconv.Atoi(fields[3])
		if err != nil || port < 0 || port > 65535 {
			return nil, false, fmt.Errorf("proxy protocol v1 header: %q is not a valid port", fields[3])
		}
		return &net.TCPAddr{IP: ip, Port: port}, true, nil
	default:
		return nil, false, fmt.Errorf("proxy protocol v1 header: unrecognised protocol %q", fields[0])
	}
}

// v2 command/family/protocol bytes (proxy-protocol.txt §2.2).
const (
	v2CmdLocal = 0x0
	v2CmdProxy = 0x1
	v2FamInet  = 0x1
	v2FamInet6 = 0x2
)

// readProxyV2 reads a binary v2 header. The 12-byte signature has already been consumed.
func readProxyV2(conn net.Conn) (net.Addr, bool, error) {
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return nil, false, fmt.Errorf("reading proxy protocol v2 header: %w", err)
	}
	if head[0]>>4 != 0x2 {
		return nil, false, fmt.Errorf("proxy protocol v2 header has version %d, want 2", head[0]>>4)
	}
	cmd := head[0] & 0x0F
	fam, proto := head[1]>>4, head[1]&0x0F
	length := binary.BigEndian.Uint16(head[2:4])
	// The address block plus any trailing TLVs, always exactly `length` bytes - read it all so the
	// connection is positioned correctly for the handshake even when it's a command or family this
	// server doesn't need an address from.
	body := make([]byte, length)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, false, fmt.Errorf("reading proxy protocol v2 body: %w", err)
	}
	if cmd == v2CmdLocal {
		// The proxy's own health check, not a forwarded connection - accept it, use the TCP peer as is.
		return nil, false, nil
	}
	if cmd != v2CmdProxy {
		return nil, false, fmt.Errorf("proxy protocol v2 header: unrecognised command %#x", cmd)
	}
	// proto is TCP (0x1) or UDP (0x2) over the chosen family; this listener only ever sees TCP, but the
	// address layout is the same either way, so only the family (and that it's a real address) matters.
	_ = proto
	switch fam {
	case v2FamInet:
		if len(body) < 12 {
			return nil, false, errors.New("proxy protocol v2 header: IPv4 body too short")
		}
		port := binary.BigEndian.Uint16(body[8:10])
		return &net.TCPAddr{IP: net.IP(body[0:4]), Port: int(port)}, true, nil
	case v2FamInet6:
		if len(body) < 36 {
			return nil, false, errors.New("proxy protocol v2 header: IPv6 body too short")
		}
		port := binary.BigEndian.Uint16(body[32:34])
		return &net.TCPAddr{IP: net.IP(body[0:16]), Port: int(port)}, true, nil
	default:
		// AF_UNIX or "unspecified" - no routable address to substitute; accept, fall back to the TCP peer.
		return nil, false, nil
	}
}

// proxiedConn wraps a net.Conn to report a different RemoteAddr - the address a PROXY protocol header
// declared - to everything downstream (the TLS handshake, and later gRPC's own peer.FromContext, which
// reads RemoteAddr off exactly this connection). Reading and writing are untouched.
type proxiedConn struct {
	net.Conn
	remote net.Addr
}

func (p *proxiedConn) RemoteAddr() net.Addr { return p.remote }
