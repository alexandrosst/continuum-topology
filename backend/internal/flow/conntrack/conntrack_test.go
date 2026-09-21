package conntrack

import (
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

const (
	// A pod (10.42.0.5) calling a Service (10.43.12.9:80) that kube-proxy rewrote to a pod (10.42.1.7:8080).
	viaService = "ipv4     2 tcp      6 431999 ESTABLISHED src=10.42.0.5 dst=10.43.12.9 sport=40000 dport=80 packets=10 bytes=1000 src=10.42.1.7 dst=10.42.0.5 sport=8080 dport=40000 packets=8 bytes=5000 [ASSURED] mark=0 zone=0 use=2"
	noAcct     = "ipv4     2 tcp      6 431999 ESTABLISHED src=10.42.0.5 dst=93.184.216.34 sport=41000 dport=443 src=93.184.216.34 dst=192.0.2.2 sport=443 dport=41000 [ASSURED] mark=0 zone=0 use=2"
	inbound    = "ipv4     2 tcp      6 100 TIME_WAIT src=203.0.113.9 dst=192.0.2.2 sport=50000 dport=30080 packets=4 bytes=400 src=10.42.1.7 dst=203.0.113.9 sport=8080 dport=50000 packets=3 bytes=900 [ASSURED] mark=0 use=1"
	udp        = "ipv4     2 udp      17 20 src=10.42.0.5 dst=10.43.0.10 sport=5353 dport=53 packets=1 bytes=70 src=10.42.0.2 dst=10.42.0.5 sport=53 dport=5353 packets=1 bytes=120 mark=0 use=1"
	coap       = "ipv4     2 udp      17 25 src=10.42.0.5 dst=10.43.7.7 sport=41234 dport=5683 packets=4 bytes=320 [UNREPLIED] src=10.42.2.2 dst=10.42.0.5 sport=5683 dport=41234 packets=0 bytes=0 mark=0 use=1"
	attempt    = "ipv4     2 tcp      6 60 SYN_SENT src=10.42.0.5 dst=10.43.12.9 sport=42000 dport=80 [UNREPLIED] src=10.43.12.9 dst=10.42.0.5 sport=80 dport=42000 mark=0 use=1"
	v6         = "ipv6     10 tcp      6 300 ESTABLISHED src=fd00::5 dst=fd01::9 sport=40001 dport=80 packets=2 bytes=200 src=fd00::7 dst=fd00::5 sport=8080 dport=40001 packets=2 bytes=300 [ASSURED] mark=0 use=1"
)

func TestParseLine(t *testing.T) {
	e, ok := ParseLine(viaService)
	if !ok {
		t.Fatal("not parsed")
	}
	if e.State != "ESTABLISHED" || e.Src != "10.42.0.5" || e.Dst != "10.43.12.9" || e.Sport != 40000 || e.Dport != 80 ||
		e.ReplySrc != "10.42.1.7" || e.ReplySport != 8080 || e.OutBytes != 1000 || e.InBytes != 5000 || !e.HasBytes {
		t.Errorf("entry = %+v", e)
	}
	if e, ok := ParseLine(noAcct); !ok || e.HasBytes || e.OutBytes != 0 || e.ReplySrc != "93.184.216.34" {
		t.Errorf("without accounting = %+v ok=%v", e, ok)
	}
	if e, ok := ParseLine(v6); !ok || e.Src != "fd00::5" || e.ReplySrc != "fd00::7" || e.InBytes != 300 {
		t.Errorf("ipv6 = %+v ok=%v", e, ok)
	}
	if e, ok := ParseLine(udp); !ok || e.Proto != "udp" || e.State != "" || e.Dport != 53 || e.ReplySrc != "10.42.0.2" || e.OutBytes != 70 || e.InBytes != 120 {
		t.Errorf("udp = %+v ok=%v", e, ok)
	}
	if e, ok := ParseLine(coap); !ok || e.Dport != 5683 || e.OutBytes != 320 || e.InBytes != 0 {
		t.Errorf("one-way udp = %+v ok=%v", e, ok)
	}
	for _, junk := range []string{"", "garbage", "ipv4 2 tcp 6", "ipv4 2 udp 17 20 broken"} {
		if _, ok := ParseLine(junk); ok {
			t.Errorf("should not have parsed %q", junk)
		}
	}
}

func table(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "nf_conntrack")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func find(fl []*continuumv1.RawFlow, client bool, local, peer string) *continuumv1.RawFlow {
	for _, f := range fl {
		if f.Client == client && f.LocalIp == local && f.PeerIp == peer {
			return f
		}
	}
	return nil
}

func TestCollectReportsWhatIsNewSinceTheLastReading(t *testing.T) {
	p := table(t, viaService)
	r, err := Open(p, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	// What existed at start is not reported: nobody counted it.
	if fl, _, _ := r.Collect(); len(fl) != 0 {
		t.Fatalf("existing connections leaked into the first window: %v", fl)
	}

	// The same connection moved more bytes, a new one appeared, one that never connected showed up.
	grown := strings.Replace(strings.Replace(viaService, "bytes=1000", "bytes=1600", 1), "bytes=5000", "bytes=5300", 1)
	os.WriteFile(p, []byte(strings.Join([]string{grown, inbound, attempt, udp}, "\n")+"\n"), 0o644)
	fl, lost, err := r.Collect()
	if err != nil || lost != 0 {
		t.Fatalf("err=%v lost=%d", err, lost)
	}

	// The caller's view keeps the Service address it dialed, and only the growth counts.
	c := find(fl, true, "10.42.0.5", "10.43.12.9")
	if c == nil || c.Connections != 0 || c.BytesOut != 600 || c.BytesIn != 300 || c.Port != 80 {
		t.Errorf("caller's view of the existing connection = %+v", c)
	}
	// The callee's view is the pod that finally answered, with the peer that connected.
	if s := find(fl, false, "10.42.1.7", "10.42.0.5"); s == nil || s.Port != 8080 || s.BytesOut != 600 {
		t.Errorf("callee's view = %+v", s)
	}
	// A connection from outside is visible from the callee's side, with the pod behind the NodePort.
	in := find(fl, false, "10.42.1.7", "203.0.113.9")
	if in == nil || in.Connections != 1 || in.Port != 8080 || in.BytesOut != 400 || in.BytesIn != 900 {
		t.Errorf("inbound = %+v", in)
	}
	if find(fl, true, "10.42.0.5", "10.43.12.9") != nil && find(fl, true, "10.42.0.5", "10.43.12.9").Connections != 0 {
		t.Error("a connection that was already counted was counted again")
	}
	for _, f := range fl {
		if f.Port == 53 || f.Port == 42000 {
			t.Errorf("a UDP entry or an unanswered attempt was reported though only TCP was asked for: %+v", f)
		}
	}
	if !r.BytesKnown() {
		t.Error("bytes were present in the table, the report should say they are known")
	}

	// Nothing changed: nothing reported.
	if fl, _, _ := r.Collect(); len(fl) != 0 {
		t.Errorf("an unchanged table produced %v", fl)
	}
}

func TestBytesUnknownWithoutAccounting(t *testing.T) {
	p := table(t)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte(noAcct+"\n"), 0o644)
	fl, _, _ := r.Collect()
	if f := find(fl, true, "10.42.0.5", "93.184.216.34"); f == nil || f.Connections != 1 || f.BytesOut != 0 {
		t.Errorf("flow = %+v", f)
	}
	if r.BytesKnown() {
		t.Error("the kernel is not counting bytes; the report must not claim they are known")
	}
}

func TestOpenFailsWhenTheTableIsMissing(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing table must be an error so the caller can say why nothing is observed")
	}
}

// Against the real kernel table: a connection made now must show up. Skipped where the table is empty
// because nothing hooks connection tracking (no iptables/nftables rules use it), which is a property of
// the machine and not of this code.
func TestRealTable(t *testing.T) {
	r, err := Open("")
	if err != nil {
		t.Skipf("no conntrack table here: %v", err)
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint32(ln.Addr().(*net.TCPAddr).Port)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			io.Copy(io.Discard, c)
			c.Close()
		}
	}()
	// Dial our own non-loopback address, which is tracked where loopback often is not.
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Skip("no non-loopback address")
	}
	c, err := net.Dial("tcp", net.JoinHostPort(ip, strconv.FormatUint(uint64(port), 10)))
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("hello"))
	time.Sleep(200 * time.Millisecond)
	c.Close()
	time.Sleep(300 * time.Millisecond)

	fl, _, err := r.Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fl {
		if f.Client && f.Port == port {
			if f.Connections != 1 {
				t.Errorf("connections = %d", f.Connections)
			}
			return
		}
	}
	t.Skipf("the kernel did not track this connection (table empty of it); %d flows seen", len(fl))
}

func TestUDPFlowsAreReportedWhenAskedFor(t *testing.T) {
	p := table(t)
	r, err := Open(p, "udp")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(p, []byte(strings.Join([]string{viaService, udp, coap}, "\n")+"\n"), 0o644)
	fl, _, _ := r.Collect()
	if f := find(fl, true, "10.42.0.5", "10.43.0.10"); f == nil || f.Protocol != "udp" || f.Port != 53 || f.Connections != 1 || f.BytesOut != 70 || f.BytesIn != 120 {
		t.Errorf("dns = %+v", f)
	}
	// One-way traffic (a sensor publishing, statsd) is still a dependency even though nothing answers.
	if f := find(fl, true, "10.42.0.5", "10.43.7.7"); f == nil || f.Port != 5683 || f.BytesOut != 320 {
		t.Errorf("one-way udp = %+v", f)
	}
	for _, f := range fl {
		if f.Protocol == "tcp" {
			t.Errorf("tcp reported though only udp was asked for: %+v", f)
		}
	}
	// The same entry a second time, grown: only the growth.
	grown := strings.Replace(coap, "bytes=320", "bytes=420", 1)
	os.WriteFile(p, []byte(strings.Join([]string{viaService, udp, grown}, "\n")+"\n"), 0o644)
	fl, _, _ = r.Collect()
	if f := find(fl, true, "10.42.0.5", "10.43.7.7"); f == nil || f.Connections != 0 || f.BytesOut != 100 {
		t.Errorf("growth = %+v", f)
	}
}

// UDP against the real kernel table, skipped where the kernel is not tracking this machine's traffic.
func TestRealTableUDP(t *testing.T) {
	r, err := Open("", "udp")
	if err != nil {
		t.Skipf("no conntrack table here: %v", err)
	}
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Skip("no non-loopback address")
	}
	pc, err := net.ListenPacket("udp", net.JoinHostPort(ip, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := uint32(pc.LocalAddr().(*net.UDPAddr).Port)
	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for i := 0; i < 3; i++ {
		c.Write(make([]byte, 100))
	}
	time.Sleep(300 * time.Millisecond)
	fl, _, _ := r.Collect()
	for _, f := range fl {
		if f.Client && f.Port == port && f.Protocol == "udp" {
			if f.Connections != 1 {
				t.Errorf("connections = %d", f.Connections)
			}
			return
		}
	}
	t.Skipf("the kernel did not track this datagram flow; %d flows seen", len(fl))
}
