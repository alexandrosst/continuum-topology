package agents

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

// byteConn serves a fixed byte string as the front of a connection and counts what was read from it. Only Read is used.
type byteConn struct {
	net.Conn
	r    *bytes.Reader
	read int
}

func (c *byteConn) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.read += n
	return n, err
}

// readProxyHeader reads the front of a connection that anyone on the network can open, so whatever arrives it must end (the reader
// is finite), read exactly the header's bytes and nothing after them (the TLS handshake follows on the same connection), and give an
// address only when it is a usable one. The seeds run with every `go test`; `go test -fuzz=FuzzReadProxyHeader` looks for more.
func FuzzReadProxyHeader(f *testing.F) {
	f.Add([]byte("PROXY TCP4 203.0.113.7 198.51.100.1 51234 8443\r\nrest-of-stream"))
	f.Add([]byte("PROXY TCP6 2001:db8::1 2001:db8::2 1111 8443\r\nX"))
	f.Add([]byte("PROXY UNKNOWN\r\nhello"))
	f.Add([]byte("PROXY TCP4 999.1.1.1 1.1.1.1 1 2\r\n"))
	f.Add([]byte("PROXY TCP4 1.1.1.1 1.1.1.1 70000 2\r\n"))
	f.Add(append(append([]byte{}, proxyV2Sig...), 0x21, 0x11, 0, 12, 203, 0, 113, 7, 198, 51, 100, 1, 0xC8, 0x22, 0x20, 0xFB, 'T', 'L', 'S'))
	f.Add(append(append([]byte{}, proxyV2Sig...), 0x20, 0x00, 0, 0, 'T', 'L', 'S'))
	f.Add(append(append([]byte{}, proxyV2Sig...), 0x21, 0x21, 0, 36))
	f.Add([]byte("GET / HTTP/1.1\r\n"))
	f.Add([]byte("PROXY "))
	f.Fuzz(func(t *testing.T, data []byte) {
		c := &byteConn{r: bytes.NewReader(data)}
		addr, ok, err := readProxyHeader(c)
		switch {
		case err != nil && (ok || addr != nil):
			t.Fatalf("an error with an address: %v %v %v", addr, ok, err)
		case err == nil && ok:
			tcp, isTCP := addr.(*net.TCPAddr)
			if !isTCP || tcp.IP == nil || tcp.Port < 0 || tcp.Port > 65535 {
				t.Fatalf("accepted a header and gave %#v", addr)
			}
		case err == nil && !ok && addr != nil:
			t.Fatalf("no usable address but %v was returned", addr)
		}
		if err != nil {
			return
		}
		// Exactly the header: a v1 line ends at its first newline, a v2 header is 16 bytes and the length it declares.
		var want int
		if bytes.HasPrefix(data, proxyV1Prefix) {
			want = bytes.IndexByte(data, '\n') + 1
		} else {
			want = 16 + int(binary.BigEndian.Uint16(data[14:16]))
		}
		if c.read != want {
			t.Fatalf("read %d bytes of the connection, the header is %d", c.read, want)
		}
	})
}
