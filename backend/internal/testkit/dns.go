package testkit

import (
	"encoding/binary"
	"net"
	"testing"
)

// FakeDNS answers A queries for any name with whatever answer() returns, and nothing for AAAA.
func FakeDNS(t *testing.T, answer func() net.IP) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			q := append([]byte(nil), buf[:n]...)
			i := 12
			for i < n && q[i] != 0 {
				i += int(q[i]) + 1
			}
			end := i + 5 // the zero byte, type, class
			if end > n {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[i+1:])
			resp := append([]byte(nil), q[:end]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available, no error
			resp[6], resp[7], resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0, 0, 0
			if qtype == 1 {
				resp[7] = 1
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 0, 0, 4)
				resp = append(resp, answer().To4()...)
			}
			_, _ = pc.WriteTo(resp, from)
		}
	}()
	return pc.LocalAddr().String()
}
