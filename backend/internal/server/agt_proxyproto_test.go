package server

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// pipe returns a connection readProxyHeader reads from, plus the sender-side to feed it bytes on, so a
// test can both control exactly what arrives and check what's left over afterwards (the TLS bytes that
// would follow a real header in production).
func pipe(t *testing.T) (readSide net.Conn, sendSide net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func send(t *testing.T, w net.Conn, b []byte) {
	t.Helper()
	go func() {
		_, _ = w.Write(b)
	}()
}

func TestReadProxyHeaderV1TCP4(t *testing.T) {
	r, w := pipe(t)
	send(t, w, []byte("PROXY TCP4 203.0.113.7 198.51.100.1 51234 8443\r\nrest-of-stream"))
	addr, ok, err := readProxyHeader(r)
	if err != nil || !ok {
		t.Fatalf("readProxyHeader: ok=%v err=%v", ok, err)
	}
	tcp, isTCP := addr.(*net.TCPAddr)
	if !isTCP || tcp.IP.String() != "203.0.113.7" || tcp.Port != 51234 {
		t.Fatalf("addr = %#v, want 203.0.113.7:51234", addr)
	}
	assertRemainder(t, r, "rest-of-stream")
}

func TestReadProxyHeaderV1TCP6(t *testing.T) {
	r, w := pipe(t)
	send(t, w, []byte("PROXY TCP6 2001:db8::1 2001:db8::2 1111 8443\r\nX"))
	addr, ok, err := readProxyHeader(r)
	if err != nil || !ok {
		t.Fatalf("readProxyHeader: ok=%v err=%v", ok, err)
	}
	tcp := addr.(*net.TCPAddr)
	if tcp.IP.String() != "2001:db8::1" || tcp.Port != 1111 {
		t.Fatalf("addr = %#v, want [2001:db8::1]:1111", addr)
	}
	assertRemainder(t, r, "X")
}

func TestReadProxyHeaderV1Unknown(t *testing.T) {
	r, w := pipe(t)
	send(t, w, []byte("PROXY UNKNOWN\r\nhello"))
	addr, ok, err := readProxyHeader(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || addr != nil {
		t.Fatalf("UNKNOWN should report ok=false, addr=nil; got ok=%v addr=%v", ok, addr)
	}
	assertRemainder(t, r, "hello")
}

func TestReadProxyHeaderV1Malformed(t *testing.T) {
	for name, line := range map[string]string{
		"bad ip":         "PROXY TCP4 not-an-ip 198.51.100.1 1 2\r\n",
		"bad port":       "PROXY TCP4 203.0.113.7 198.51.100.1 not-a-port 2\r\n",
		"missing field":  "PROXY TCP4 203.0.113.7\r\n",
		"unknown family": "PROXY BOGUS 1 2 3 4\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			r, w := pipe(t)
			send(t, w, []byte(line))
			if _, _, err := readProxyHeader(r); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestReadProxyHeaderV1LineTooLong(t *testing.T) {
	r, w := pipe(t)
	send(t, w, append([]byte("PROXY TCP4 "), bytes.Repeat([]byte("9"), 200)...))
	if _, _, err := readProxyHeader(r); err == nil {
		t.Fatal("expected an error for an over-long v1 line")
	}
}

func v2Header(t *testing.T, cmd, fam byte, body []byte) []byte {
	t.Helper()
	h := make([]byte, 16+len(body))
	copy(h, proxyV2Sig)
	h[12] = 0x20 | cmd   // version 2, command
	h[13] = fam<<4 | 0x1 // family, protocol TCP
	binary.BigEndian.PutUint16(h[14:16], uint16(len(body)))
	copy(h[16:], body)
	return h
}

func TestReadProxyHeaderV2Inet(t *testing.T) {
	body := make([]byte, 12)
	copy(body[0:4], net.ParseIP("203.0.113.9").To4())
	copy(body[4:8], net.ParseIP("198.51.100.2").To4())
	binary.BigEndian.PutUint16(body[8:10], 61234)
	binary.BigEndian.PutUint16(body[10:12], 8443)

	r, w := pipe(t)
	send(t, w, append(v2Header(t, v2CmdProxy, v2FamInet, body), []byte("tls-bytes")...))
	addr, ok, err := readProxyHeader(r)
	if err != nil || !ok {
		t.Fatalf("readProxyHeader: ok=%v err=%v", ok, err)
	}
	tcp := addr.(*net.TCPAddr)
	if tcp.IP.String() != "203.0.113.9" || tcp.Port != 61234 {
		t.Fatalf("addr = %#v, want 203.0.113.9:61234", addr)
	}
	assertRemainder(t, r, "tls-bytes")
}

func TestReadProxyHeaderV2Inet6(t *testing.T) {
	src := net.ParseIP("2001:db8::10")
	dst := net.ParseIP("2001:db8::20")
	body := make([]byte, 36)
	copy(body[0:16], src.To16())
	copy(body[16:32], dst.To16())
	binary.BigEndian.PutUint16(body[32:34], 2222)
	binary.BigEndian.PutUint16(body[34:36], 8443)

	r, w := pipe(t)
	send(t, w, v2Header(t, v2CmdProxy, v2FamInet6, body))
	addr, ok, err := readProxyHeader(r)
	if err != nil || !ok {
		t.Fatalf("readProxyHeader: ok=%v err=%v", ok, err)
	}
	tcp := addr.(*net.TCPAddr)
	if !tcp.IP.Equal(src) || tcp.Port != 2222 {
		t.Fatalf("addr = %#v, want [2001:db8::10]:2222", addr)
	}
}

func TestReadProxyHeaderV2Local(t *testing.T) {
	r, w := pipe(t)
	send(t, w, append(v2Header(t, v2CmdLocal, 0x0, nil), []byte("healthcheck-tls")...))
	addr, ok, err := readProxyHeader(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok || addr != nil {
		t.Fatalf("LOCAL should report ok=false, addr=nil; got ok=%v addr=%v", ok, addr)
	}
	assertRemainder(t, r, "healthcheck-tls")
}

func TestReadProxyHeaderV2WrongVersion(t *testing.T) {
	h := v2Header(t, v2CmdProxy, v2FamInet, make([]byte, 12))
	h[12] = 0x10 // version 1 in the v2 signature's header byte - not valid
	r, w := pipe(t)
	send(t, w, h)
	if _, _, err := readProxyHeader(r); err == nil {
		t.Fatal("expected an error for an unsupported v2 version")
	}
}

func TestReadProxyHeaderNoHeaderAtAll(t *testing.T) {
	// What a real TLS ClientHello starts with: not a PROXY protocol header of either version. This is the
	// case that matters most - a client that reached the listener without going through the proxy at all
	// must be refused, not quietly accepted under its own TCP-level address.
	r, w := pipe(t)
	send(t, w, []byte{0x16, 0x03, 0x01, 0x00, 0xa5, 0x01, 0x00, 0x00, 0xa1, 0x03, 0x03, 0x00})
	if _, _, err := readProxyHeader(r); err == nil {
		t.Fatal("expected an error when no PROXY protocol header is present")
	}
}

func TestReadProxyHeaderShortRead(t *testing.T) {
	r, w := pipe(t)
	send(t, w, []byte("PR"))
	w.Close()
	if _, _, err := readProxyHeader(r); err == nil {
		t.Fatal("expected an error when the connection closes mid-header")
	}
}

func assertRemainder(t *testing.T, r net.Conn, want string) {
	t.Helper()
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(want))
	if _, err := io.ReadFull(r, got); err != nil {
		t.Fatalf("reading remainder: %v", err)
	}
	if string(got) != want {
		t.Fatalf("remainder = %q, want %q", got, want)
	}
}

func TestProxiedConnReportsSubstitutedAddr(t *testing.T) {
	r, _ := pipe(t)
	fake := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 4444}
	p := &proxiedConn{Conn: r, remote: fake}
	if p.RemoteAddr().String() != fake.String() {
		t.Fatalf("RemoteAddr() = %v, want %v", p.RemoteAddr(), fake)
	}
}
