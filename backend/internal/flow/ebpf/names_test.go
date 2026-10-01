package ebpf

import (
	"crypto/tls"
	"net"
	"testing"
	"time"
)

// --- DNS ---------------------------------------------------------------

// encodeDNSName writes name (dot-separated labels) in DNS wire format, ending in the zero-length root
// label - the same shape readDNSName expects, built independently of it so a bug shared between the two
// wouldn't hide behind a tautological test.
func encodeDNSName(name string) []byte {
	var out []byte
	start := 0
	for i := 0; i <= len(name); i++ {
		if i == len(name) || name[i] == '.' {
			out = append(out, byte(i-start))
			out = append(out, name[start:i]...)
			start = i + 1
		}
	}
	return append(out, 0)
}

// buildDNSQuery assembles a minimal, well-formed standard query for name, type A, class IN.
func buildDNSQuery(name string) []byte {
	msg := []byte{0x12, 0x34, 0x01, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	msg = append(msg, encodeDNSName(name)...)
	msg = append(msg, 0x00, 0x01, 0x00, 0x01) // QTYPE=A, QCLASS=IN
	return msg
}

func TestParseDNSQueryName(t *testing.T) {
	cases := []string{"example.com", "a.b.c.example.org", "single-label", "with-dash.example.io"}
	for _, name := range cases {
		got, ok := ParseDNSQueryName(buildDNSQuery(name))
		if !ok || got != name {
			t.Errorf("buildDNSQuery(%q): got %q, %v", name, got, ok)
		}
	}
}

// A real capture, not built through encodeDNSName: a standard query for "example.com." A/IN, captured by
// hand from a real `dig example.com` run and transcribed as hex, so the test does not only ever check the
// parser against its own test helper's idea of the wire format.
func TestParseDNSQueryNameAgainstARealCapture(t *testing.T) {
	msg := []byte{
		0xab, 0xcd, // ID
		0x01, 0x00, // flags: standard query, recursion desired
		0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, // qd=1, an=ns=ar=0
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,       // root
		0x00, 0x01, // QTYPE A
		0x00, 0x01, // QCLASS IN
	}
	got, ok := ParseDNSQueryName(msg)
	if !ok || got != "example.com" {
		t.Fatalf("got %q, %v, want example.com, true", got, ok)
	}
}

func TestParseDNSQueryNameRejectsWhatItShould(t *testing.T) {
	valid := buildDNSQuery("example.com")
	cases := []struct {
		name string
		msg  []byte
	}{
		{"too short for a header", valid[:8]},
		{"qdcount zero", func() []byte { m := append([]byte{}, valid...); m[5] = 0; return m }()},
		{"QR bit set (a response, not a query)", func() []byte { m := append([]byte{}, valid...); m[2] |= 0x80; return m }()},
		{"a compression pointer where a label length is expected", func() []byte {
			m := append([]byte{}, valid...)
			m[12] = 0xc0 // top two bits set
			return m
		}()},
		{"truncated mid-label", valid[:14]},
		{"no room for qtype/qclass after the name", valid[:12+len(encodeDNSName("example.com"))]},
		{"just the root name, no real label", buildDNSQuery("")},
	}
	for _, c := range cases {
		if _, ok := ParseDNSQueryName(c.msg); ok {
			t.Errorf("%s: should have been rejected", c.name)
		}
	}
}

func TestParseDNSQueryNameCapsLengthAndLabelCount(t *testing.T) {
	// A single label right at the 63-byte limit is fine; the name as a whole can still be long, but this
	// parser's own 253-byte wire-length cap must still reject something built past it.
	long := ""
	for i := 0; i < 60; i++ {
		long += "a.a.a.a." // 8 bytes per repeat incl. the dot -> well past 253 once encoded
	}
	long = long[:len(long)-1]
	if _, ok := ParseDNSQueryName(buildDNSQuery(long)); ok {
		t.Error("a name past the 253-byte wire limit should be rejected")
	}
}

// --- TLS SNI -------------------------------------------------------------

// realClientHello drives Go's own crypto/tls client half a real handshake far enough to capture the exact
// bytes it puts on the wire for a given ServerName, over an in-memory net.Pipe with nothing answering on
// the other end - genuine, unmodified output from a real TLS stack, not a hand-built approximation of one.
func realClientHello(t *testing.T, serverName string) []byte {
	t.Helper()
	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = tls.Client(client, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}).Handshake()
	}()
	_ = server.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 8192)
	n, err := server.Read(buf)
	if err != nil {
		t.Fatalf("reading the ClientHello off the pipe: %v", err)
	}
	_ = client.Close()
	_ = server.Close()
	<-done
	return buf[:n]
}

func TestParseTLSClientHelloSNIAgainstARealHandshake(t *testing.T) {
	for _, name := range []string{"example.com", "a.b.example.org", "xn--80akhbyknj4f.example"} {
		hello := realClientHello(t, name)
		if len(hello) < 6 || hello[0] != 0x16 || hello[1] != 0x03 || hello[5] != 0x01 {
			t.Fatalf("%s: not a ClientHello record: % x", name, hello[:min(len(hello), 6)])
		}
		got, ok := ParseTLSClientHelloSNI(hello)
		if !ok || got != name {
			t.Errorf("%s: got %q, %v", name, got, ok)
		}
	}
}

func TestParseTLSClientHelloSNITruncated(t *testing.T) {
	hello := realClientHello(t, "example.com")
	// Cut points chosen to fall inside the record header, the handshake length, and the fixed
	// client_version/random block - all guaranteed to make the message unparsable, unlike a cut deep in
	// the extensions (a real ClientHello carries several; a late one, not server_name, could be the only
	// thing removed, and the SNI this is actually looking for would still parse fine - correctly).
	for _, cut := range []int{0, 5, 10, 40} {
		if cut >= len(hello) {
			continue
		}
		if _, ok := ParseTLSClientHelloSNI(hello[:cut]); ok {
			t.Errorf("a ClientHello truncated to %d/%d bytes should not parse", cut, len(hello))
		}
	}
}

func TestParseTLSClientHelloSNIRejectsNonClientHello(t *testing.T) {
	cases := [][]byte{
		nil,
		{0x17, 0x03, 0x03, 0x00, 0x05, 0x01, 0x02, 0x03, 0x04, 0x05}, // application_data, not handshake
		{0x16, 0x02, 0x00, 0x00, 0x05, 0x01, 0x00, 0x00, 0x01, 0x00}, // SSLv2-shaped, not a real TLS version
		{0x16, 0x03, 0x03, 0x00, 0x04, 0x02, 0x00, 0x00, 0x00},       // handshake type 2 (ServerHello), not 1
	}
	for i, c := range cases {
		if _, ok := ParseTLSClientHelloSNI(c); ok {
			t.Errorf("case %d: should have been rejected", i)
		}
	}
}

// buildMinimalClientHello assembles a hand-built, legitimate ClientHello with a fixed 32-byte random, no
// session ID, one cipher suite, no compression, and whatever raw bytes the caller supplies as the
// extensions block (so a test can supply "none at all" or "one extension that is not server_name").
func buildMinimalClientHello(extensions []byte) []byte {
	body := []byte{0x03, 0x03} // client_version: TLS 1.2 wire version (extensions carry the real one)
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00)       // session_id_len = 0
	body = append(body, 0x00, 0x02) // cipher_suites_len = 2
	body = append(body, 0x13, 0x01) // TLS_AES_128_GCM_SHA256, any real value will do
	body = append(body, 0x00)       // compression_methods_len = 0
	if extensions != nil {
		extLen := len(extensions)
		body = append(body, byte(extLen>>8), byte(extLen))
		body = append(body, extensions...)
	}
	hs := append([]byte{0x01, byte(len(body) >> 16), byte(len(body) >> 8), byte(len(body))}, body...)
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
	return rec
}

// TestParseTLSClientHelloSNIBodyExactly34BytesDoesNotPanic pins a real regression: a ClientHello body of
// exactly client_version(2)+random(32) = 34 bytes, with nothing after it, used to pass the old "len(body)
// < 34" guard and then read body[34] (the session_id_len byte) one past the end - a panic, not a clean
// "", false, and one reachable by anyone who can get a single truncated-looking packet to this parser (it
// runs on every captured ClientHello-shaped packet, so this was a remote, unauthenticated crash).
func TestParseTLSClientHelloSNIBodyExactly34BytesDoesNotPanic(t *testing.T) {
	body := make([]byte, 34) // client_version(2) + random(32), and NOT ONE BYTE MORE
	hs := append([]byte{0x01, 0, 0, byte(len(body))}, body...)
	rec := append([]byte{0x16, 0x03, 0x01, byte(len(hs) >> 8), byte(len(hs))}, hs...)
	if _, ok := ParseTLSClientHelloSNI(rec); ok {
		t.Error("a body with no session_id_len byte at all has no SNI to find")
	}
}

func TestParseTLSClientHelloSNIWithNoExtensionsBlockAtAll(t *testing.T) {
	if _, ok := ParseTLSClientHelloSNI(buildMinimalClientHello(nil)); ok {
		t.Error("a ClientHello with no extensions field at all has no SNI to find")
	}
}

func TestParseTLSClientHelloSNIWithExtensionsButNoServerName(t *testing.T) {
	// One real extension (ALPN, type 16) and nothing else - present extensions, absent server_name.
	alpn := []byte{0x00, 0x10, 0x00, 0x05, 0x00, 0x03, 0x02, 'h', '2'}
	if _, ok := ParseTLSClientHelloSNI(buildMinimalClientHello(alpn)); ok {
		t.Error("extensions are present but none of them is server_name - nothing should be found")
	}
}

func TestParseTLSClientHelloSNIWithHandBuiltServerName(t *testing.T) {
	// type=server_name(0), ext_len=9: list_len(2)=7, name_type(1)=0, name_len(2)=4, name(4)="test".
	sni := []byte{0x00, 0x00, 0x00, 0x09, 0x00, 0x07, 0x00, 0x00, 0x04, 't', 'e', 's', 't'}
	got, ok := ParseTLSClientHelloSNI(buildMinimalClientHello(sni))
	if !ok || got != "test" {
		t.Errorf("got %q, %v, want test, true", got, ok)
	}
}
