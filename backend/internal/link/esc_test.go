package link

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

func TestEscLeavesAPlainValueAloneAndEscapesTheRest(t *testing.T) {
	for in, want := range map[string]string{
		"":                       "",
		"cl-a":                   "cl-a",
		"cluster 7 (eu-west)":    "cluster 7 (eu-west)",
		`back\slash`:             `back\\slash`,
		`say "hi"`:               `say \"hi\"`,
		"two\nlines":             `two\nlines`,
		"all\\ \"three\"\nhere ": `all\\ \"three\"\nhere `,
	} {
		if got := esc(in); got != want {
			t.Errorf("esc(%q) = %q, want %q", in, got, want)
		}
	}
	// A value with nothing to escape comes back as the same string, not a copy.
	plain := fmt.Sprintf("cl-%d", 42)
	if got := esc(plain); got != plain {
		t.Errorf("esc(%q) = %q", plain, got)
	}
	if n := testing.AllocsPerRun(100, func() { _ = esc(plain) }); n != 0 {
		t.Errorf("esc of a plain value allocates %v times", n)
	}
}

func BenchmarkMeterWrite64Peers(b *testing.B) {
	var lps []*continuumv1.LinkPeer
	for i := 0; i < MaxPeers; i++ {
		lp := peer(fmt.Sprintf("cl-%02d", i), fmt.Sprintf("Cluster %02d", i), fmt.Sprintf("10.%d.0.1", i+1))
		lp.Throughput = true
		lps = append(lps, lp)
	}
	p := NewPeers()
	p.Set("cl-me", lps)
	m := NewMeter(p)
	m.EnableProbes()
	m.EnableConnects()
	for _, pe := range p.List() {
		m.Observe([]*continuumv1.RawFlow{{Client: true, PeerIp: pe.Addresses[0].String(), Connections: 3, BytesOut: 1 << 20, RttUs: 1200}})
		m.ObserveProbe(pe.ID, Forward, OwampResult{Sent: 100, Lost: 1, HasDelay: true, Min: 0.01, Median: 0.012, Max: 0.03})
		m.ObserveProbe(pe.ID, Reverse, OwampResult{Sent: 100, Lost: 1, HasDelay: true, Min: 0.01, Median: 0.012, Max: 0.03})
		m.ObserveConnect(pe.ID, 1500000)
	}
	var buf bytes.Buffer
	m.Write(&buf)
	b.SetBytes(int64(buf.Len()))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Write(io.Discard)
	}
}
