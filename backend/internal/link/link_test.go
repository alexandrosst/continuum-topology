package link

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	continuumv1 "continuum/gen/continuumv1"
)

func peer(id, name string, addrs ...string) *continuumv1.LinkPeer {
	return &continuumv1.LinkPeer{ClusterId: id, Name: name, Addresses: addrs}
}

func TestPeersPlaceAnAddressAndDropWhatCannotBePlaced(t *testing.T) {
	p := NewPeers()
	dropped := p.Set("cl-me", []*continuumv1.LinkPeer{
		peer("cl-a", "A", "10.0.0.1", "10.0.0.2", "::ffff:10.0.0.9", "127.0.0.1", "169.254.1.1", "not-an-ip", "0.0.0.0"),
		peer("cl-b", "B", "10.0.0.2", "192.168.5.5"), // 10.0.0.2 is claimed by both
		peer("cl-me", "me", "10.9.9.9"),              // never a peer of itself
		peer("cl-c", "C", "127.0.0.1"),               // nothing usable
		peer("cl-a", "A again", "10.7.7.7"),          // a repeated id is ignored
		nil,
	})
	if got := p.Lookup("10.0.0.1"); got != "cl-a" {
		t.Fatalf("10.0.0.1 = %q, want cl-a", got)
	}
	if got := p.Lookup("10.0.0.9"); got != "cl-a" { // given as an IPv4-mapped IPv6 literal
		t.Fatalf("the mapped form did not resolve: %q", got)
	}
	if got := p.Lookup("::ffff:192.168.5.5"); got != "cl-b" {
		t.Fatalf("a mapped lookup did not resolve: %q", got)
	}
	for _, ip := range []string{"10.0.0.2", "127.0.0.1", "169.254.1.1", "10.9.9.9", "10.7.7.7", "garbage", ""} {
		if got := p.Lookup(ip); got != "" {
			t.Errorf("Lookup(%q) = %q, want none", ip, got)
		}
	}
	if ids := idsOf(p.List()); ids != "cl-a,cl-b" {
		t.Fatalf("peers = %s, want cl-a,cl-b", ids)
	}
	joined := strings.Join(dropped, "|")
	if !strings.Contains(joined, "cl-c has no usable address") || !strings.Contains(joined, "more than one peer cluster") {
		t.Fatalf("the reasons were not reported: %q", joined)
	}
	if p.Own() != "cl-me" || p.Empty() {
		t.Fatal("own id or emptiness wrong")
	}
}

func TestPeersAreBounded(t *testing.T) {
	var in []*continuumv1.LinkPeer
	for i := 0; i < MaxPeers+10; i++ {
		in = append(in, peer("cl-"+string(rune('a'+i%26))+string(rune('a'+i/26)), "", "10.1."+itoaT(i)+".1"))
	}
	p := NewPeers()
	dropped := p.Set("me", in)
	if len(p.List()) != MaxPeers {
		t.Fatalf("kept %d peers, want %d", len(p.List()), MaxPeers)
	}
	if len(dropped) == 0 {
		t.Fatal("the cut was not reported")
	}
	addrs := make([]string, MaxAddresses+50)
	for i := range addrs {
		addrs[i] = "10.2." + itoaT(i/250) + "." + itoaT(i%250+1)
	}
	p.Set("me", []*continuumv1.LinkPeer{peer("cl-x", "", addrs...)})
	if n := len(p.List()[0].Addresses); n != MaxAddresses {
		t.Fatalf("kept %d addresses, want %d", n, MaxAddresses)
	}
}

func TestAnEmptyConfigClearsThePeers(t *testing.T) {
	p := NewPeers()
	p.Set("me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	p.Set("me", nil)
	if !p.Empty() || p.Lookup("10.0.0.1") != "" {
		t.Fatal("peers survived an empty Config")
	}
}

func TestMeterCountsPerPeerAndIgnoresEveryoneElse(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "Alpha", "10.0.0.1"), peer("cl-b", "Beta", "10.0.0.2")})
	m := NewMeter(p)
	m.Observe([]*continuumv1.RawFlow{
		// we dialed A: we sent bytes_out
		{Client: true, PeerIp: "10.0.0.1", Connections: 2, BytesOut: 1000, BytesIn: 50, SegsOut: 100, Retransmits: 3, RtoRetransmits: 1, RttUs: 12000, JitterUs: 800, HandshakeUs: 30000},
		// A dialed us: what we sent is bytes_in
		{Client: false, PeerIp: "10.0.0.1", Connections: 1, BytesOut: 7, BytesIn: 400, SegsOut: 10, FailedAttempts: 5, FailedRefused: 1, FailedTimeout: 2},
		{Client: true, PeerIp: "10.0.0.2", Connections: 1, RttUs: 300000},
		{Client: true, PeerIp: "8.8.8.8", Connections: 99, BytesOut: 99999}, // not a peer
		nil,
	})
	got := render(m)
	want := map[string]string{
		`continuum_link_connections_total{cluster="cl-me",peer="cl-a"}`:                          "3",
		`continuum_link_sent_bytes_total{cluster="cl-me",peer="cl-a"}`:                           "1400", // 1000 as caller + 400 as callee
		`continuum_link_received_bytes_total{cluster="cl-me",peer="cl-a"}`:                       "57",
		`continuum_link_segments_sent_total{cluster="cl-me",peer="cl-a"}`:                        "110",
		`continuum_link_retransmits_total{cluster="cl-me",peer="cl-a"}`:                          "3",
		`continuum_link_rto_retransmits_total{cluster="cl-me",peer="cl-a"}`:                      "1",
		`continuum_link_connection_failures_total{cluster="cl-me",peer="cl-a",reason="refused"}`: "1",
		`continuum_link_connection_failures_total{cluster="cl-me",peer="cl-a",reason="timeout"}`: "2",
		`continuum_link_connection_failures_total{cluster="cl-me",peer="cl-a",reason="other"}`:   "2", // 5 failed, 3 named
		`continuum_link_connections_total{cluster="cl-me",peer="cl-b"}`:                          "1",
		`continuum_link_rtt_seconds_count{cluster="cl-me",peer="cl-a"}`:                          "1",
		`continuum_link_rtt_seconds_bucket{cluster="cl-me",peer="cl-a",le="0.02"}`:               "1",
		`continuum_link_rtt_seconds_bucket{cluster="cl-me",peer="cl-a",le="0.01"}`:               "0",
		`continuum_link_rtt_seconds_bucket{cluster="cl-me",peer="cl-b",le="0.2"}`:                "0",
		`continuum_link_rtt_seconds_bucket{cluster="cl-me",peer="cl-b",le="0.5"}`:                "1",
		`continuum_link_rtt_seconds_bucket{cluster="cl-me",peer="cl-b",le="+Inf"}`:               "1",
		`continuum_link_jitter_seconds_count{cluster="cl-me",peer="cl-a"}`:                       "1",
		`continuum_link_handshake_seconds_count{cluster="cl-me",peer="cl-a"}`:                    "1",
		`continuum_link_peer_info{cluster="cl-me",peer="cl-a",peer_name="Alpha"}`:                "1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for k := range got {
		if strings.Contains(k, "8.8.8.8") || strings.Contains(k, `peer=""`) {
			t.Errorf("a non-peer leaked into the output: %s", k)
		}
	}
}

func TestMeterShowsPeersBeforeAnyTrafficAndForgetsRemovedOnes(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m := NewMeter(p)
	got := render(m)
	if got[`continuum_link_retransmits_total{cluster="cl-me",peer="cl-a"}`] != "0" {
		t.Fatal("a configured peer with no traffic should report zero, not be absent")
	}
	if _, ok := got[`continuum_link_last_observation_timestamp_seconds{cluster="cl-me",peer="cl-a"}`]; ok {
		t.Fatal("a last-observation time was reported before any traffic")
	}
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 1}})
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-b", "B", "10.0.0.2")})
	m.Forget()
	got = render(m)
	for k := range got {
		if strings.Contains(k, `peer="cl-a"`) {
			t.Errorf("a removed peer still has a series: %s", k)
		}
	}
	if len(m.per) != 0 {
		t.Fatal("the removed peer's totals were kept")
	}
}

func TestMeterWithNoPeersIsSilentAndANilMeterIsSafe(t *testing.T) {
	m := NewMeter(NewPeers())
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 1}})
	if len(m.per) != 0 {
		t.Fatal("recorded traffic with no peers")
	}
	var nilMeter *Meter
	nilMeter.Observe([]*continuumv1.RawFlow{{PeerIp: "10.0.0.1"}})
	nilMeter.Forget()
}

func TestOutputIsWellFormedPrometheusText(t *testing.T) {
	p := NewPeers()
	p.Set(`we"ird`, []*continuumv1.LinkPeer{peer("cl-a", "A \"quoted\"\nname", "10.0.0.1")})
	m := NewMeter(p)
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", RttUs: 5000}})
	var b bytes.Buffer
	m.Write(&b)
	text := b.String() // the scanner below drains the buffer
	typed := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			t.Fatal("a blank line in the exposition")
		}
		if strings.HasPrefix(line, "# TYPE ") {
			typed[strings.Fields(line)[2]] = true
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		name := line[:strings.IndexAny(line, "{ ")]
		base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
		if !typed[name] && !typed[base] {
			t.Errorf("sample %q has no preceding TYPE", name)
		}
		if strings.Count(line, `"`)%2 != 0 && !strings.Contains(line, `\"`) {
			t.Errorf("unbalanced quotes: %s", line)
		}
		if strings.Contains(line, "\n") {
			t.Errorf("a raw newline survived in %q", line)
		}
	}
	if !strings.Contains(text, `peer_name="A \"quoted\"\nname"`) {
		t.Errorf("label values were not escaped:\n%s", text)
	}
}

func render(m *Meter) map[string]string {
	var b bytes.Buffer
	m.Write(&b)
	out := map[string]string{}
	sc := bufio.NewScanner(&b)
	for sc.Scan() {
		l := sc.Text()
		if strings.HasPrefix(l, "#") || l == "" {
			continue
		}
		i := strings.LastIndex(l, " ")
		out[l[:i]] = l[i+1:]
	}
	return out
}

func idsOf(ps []Peer) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.ID)
	}
	return strings.Join(s, ",")
}

func itoaT(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func TestMetricsEndpointServesTheMeterAndOnlyGET(t *testing.T) {
	p := NewPeers()
	p.Set("cl-me", []*continuumv1.LinkPeer{peer("cl-a", "A", "10.0.0.1")})
	m := NewMeter(p)
	m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 4}})
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("status %d, type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(body), `continuum_link_connections_total{cluster="cl-me",peer="cl-a"} 4`) {
		t.Fatalf("body:\n%s", body)
	}
	for _, path := range []string{"/", "/healthz", "/debug/pprof/"} {
		r2, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
		if r2.StatusCode != 404 {
			t.Errorf("%s answered %d; only /metrics should be served", path, r2.StatusCode)
		}
	}
	r3, _ := http.Post(srv.URL+"/metrics", "text/plain", strings.NewReader("x"))
	r3.Body.Close()
	if r3.StatusCode == 200 {
		t.Error("POST /metrics was accepted")
	}
}

// An address two peers both claim is unusable for both, whichever came first: it must leave the first one's address list too,
// or the probes would aim at it and record the results under one peer only.
func TestAClashingAddressLeavesEveryPeersList(t *testing.T) {
	for name, in := range map[string][]*continuumv1.LinkPeer{
		"a first": {peer("a", "A", "10.0.0.1", "10.0.0.2"), peer("b", "B", "10.0.0.1", "10.0.0.3")},
		"b first": {peer("b", "B", "10.0.0.1", "10.0.0.3"), peer("a", "A", "10.0.0.1", "10.0.0.2")},
	} {
		p := NewPeers()
		p.Set("me", in)
		if got := p.Lookup("10.0.0.1"); got != "" {
			t.Errorf("%s: Lookup = %q", name, got)
		}
		got := map[string]string{}
		for _, pe := range p.List() {
			for _, a := range pe.Addresses {
				got[pe.ID] += a.String() + " "
			}
		}
		if got["a"] != "10.0.0.2 " || got["b"] != "10.0.0.3 " {
			t.Errorf("%s: addresses = %v", name, got)
		}
		for _, pd := range p.Doc().Peers {
			for _, a := range pd.Addresses {
				if a == "10.0.0.1" {
					t.Errorf("%s: the document still names the clashing address under %s", name, pd.ID)
				}
			}
		}
	}
	// A peer left with only clashing addresses is dropped, in either order.
	for name, in := range map[string][]*continuumv1.LinkPeer{
		"a first": {peer("a", "A", "10.0.0.5", "10.0.0.9"), peer("b", "B", "10.0.0.9")},
		"b first": {peer("b", "B", "10.0.0.9"), peer("a", "A", "10.0.0.5", "10.0.0.9")},
	} {
		p := NewPeers()
		dropped := p.Set("me", in)
		if ids := idsOf(p.List()); ids != "a" {
			t.Errorf("%s: peers = %s, want only a", name, ids)
		}
		if !strings.Contains(strings.Join(dropped, "|"), "b has no usable address") {
			t.Errorf("%s: dropped = %v", name, dropped)
		}
		if p.Lookup("10.0.0.5") != "a" || p.Lookup("10.0.0.9") != "" {
			t.Errorf("%s: lookups wrong", name)
		}
	}
}

func TestARepeatedAddressOfOnePeerIsKeptOnce(t *testing.T) {
	p := NewPeers()
	p.Set("me", []*continuumv1.LinkPeer{peer("a", "A", "203.0.113.5", "203.0.113.5", "::ffff:203.0.113.5")})
	l := p.List()
	if len(l) != 1 || len(l[0].Addresses) != 1 || p.Lookup("203.0.113.5") != "a" {
		t.Fatalf("%+v", l)
	}
}

// A scrape is rendered before it is written to the client, so a client that reads slowly cannot hold the meter's lock.
func TestAStalledScraperDoesNotBlockTheMeter(t *testing.T) {
	p := NewPeers()
	p.Set("me", []*continuumv1.LinkPeer{peer("a", "A", "10.0.0.1")})
	m := NewMeter(p)
	w := &blockingWriter{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	go func() { m.Write(w); close(done) }()
	<-w.entered
	observed := make(chan struct{})
	go func() {
		m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 1}})
		close(observed)
	}()
	select {
	case <-observed:
	case <-time.After(2 * time.Second):
		t.Fatal("Observe waited for the scraper")
	}
	close(w.release)
	<-done
	if !strings.Contains(w.buf.String(), "continuum_link_peer_info") {
		t.Errorf("incomplete scrape:\n%s", w.buf.String())
	}
}

type blockingWriter struct {
	buf              bytes.Buffer
	entered, release chan struct{}
	once             sync.Once
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	b.once.Do(func() { close(b.entered) })
	<-b.release
	return b.buf.Write(p)
}

// Rows that carry no byte counts (the conntrack collector with no byte accounting) must not turn into byte counters of zero
// that read as a quiet link: the series are absent, the peers document says the totals are unknown, and the load check gets no
// rate from them.
func TestUnknownBytesAreNotReportedAsZero(t *testing.T) {
	p := NewPeers()
	p.Set("me", []*continuumv1.LinkPeer{peer("a", "A", "10.0.0.1"), peer("b", "B", "10.0.0.2")})
	m := NewMeter(p)
	m.ObserveRows([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", Connections: 3, BytesOut: 0}}, false)
	m.ObserveRows([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.2", Connections: 1, BytesOut: 500}}, true)
	got := render(m)
	if _, ok := got[`continuum_link_sent_bytes_total{cluster="me",peer="a"}`]; ok {
		t.Error("a byte counter of zero was written for a peer whose bytes are unknown")
	}
	if got[`continuum_link_sent_bytes_total{cluster="me",peer="b"}`] != "500" || got[`continuum_link_connections_total{cluster="me",peer="a"}`] != "3" {
		t.Errorf("the rest must be unaffected: %v", got)
	}
	d := m.peersDoc()
	if !d.Peers[0].BytesUnknown || d.Peers[1].BytesUnknown || d.Peers[1].BytesSent != 500 {
		t.Errorf("%+v", d)
	}
	// Once a node that can count bytes reports, the counters appear.
	m.ObserveRows([]*continuumv1.RawFlow{{Client: true, PeerIp: "10.0.0.1", BytesOut: 70}}, true)
	if render(m)[`continuum_link_sent_bytes_total{cluster="me",peer="a"}`] != "70" || m.peersDoc().Peers[0].BytesUnknown {
		t.Error("known bytes did not clear the unknown state")
	}
	// The load check takes no rate from an unknown total, and does not treat it as an idle link.
	l := &Load{}
	now := time.Unix(1_700_000_000, 0)
	l.Observe(PeersDoc{Peers: []PeerDoc{{ID: "a", BytesUnknown: true}}}, now)
	l.Observe(PeersDoc{Peers: []PeerDoc{{ID: "a", BytesUnknown: true}}}, now.Add(time.Minute))
	if r, ok := l.Rate("a", now.Add(time.Minute), time.Hour); ok {
		t.Errorf("a rate %+v from unknown bytes", r)
	}
}

// The prober's own traffic (the one-way probe's control and test ports, the TCP connect timer, iperf3) is not the link's:
// it must not show in any series nor in the totals the throughput gate reads as load.
func TestTheProbersOwnPortsAreNotCounted(t *testing.T) {
	p := NewPeers()
	p.Set("me", []*continuumv1.LinkPeer{peer("a", "A", "10.0.0.1")})
	m := NewMeter(p)
	if err := m.IgnorePorts("8861, 18900-18999,8862"); err != nil {
		t.Fatal(err)
	}
	row := func(client bool, proto string, port uint32) *continuumv1.RawFlow {
		return &continuumv1.RawFlow{Client: client, Protocol: proto, PeerIp: "10.0.0.1", Port: port, Connections: 1, BytesOut: 1000, BytesIn: 1000, RttUs: 500, HandshakeUs: 300, FailedAttempts: 1, FailedTimeout: 1}
	}
	m.Observe([]*continuumv1.RawFlow{
		row(true, "tcp", 8861), row(false, "tcp", 8861), // OWAMP control, both ends
		row(true, "tcp", 8862), row(false, "tcp", 8862), row(true, "udp", 8862), // iperf3
		row(false, "udp", 18900), row(true, "udp", 18999), // OWAMP test packets
	})
	if len(m.per) != 0 {
		t.Fatalf("the prober's traffic was counted: %+v", m.per["a"])
	}
	if d := m.peersDoc(); d.Peers[0].BytesSent != 0 || d.Peers[0].BytesReceived != 0 {
		t.Errorf("%+v", d)
	}
	// The ports just outside the ranges, and real traffic, are still the link's.
	m.Observe([]*continuumv1.RawFlow{row(true, "tcp", 8860), row(true, "udp", 19000), row(true, "tcp", 443)})
	if s := m.per["a"]; s == nil || s.connections != 3 {
		t.Errorf("%+v", s)
	}
	for _, bad := range []string{"x", "0", "70000", "9-8", "1-", "-5"} {
		if err := NewMeter(NewPeers()).IgnorePorts(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := NewMeter(NewPeers()).IgnorePorts(""); err != nil {
		t.Errorf("empty: %v", err)
	}
}
