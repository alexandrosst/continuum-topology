package ebpf

import (
	"encoding/binary"
	"testing"

	continuumv1 "continuum/gen/continuumv1"
)

// TestHandleNameRecordNeverPanicsOnGarbage is the defense-in-depth complement to
// TestParseTLSClientHelloSNIBodyExactly34BytesDoesNotPanic in names_test.go: handleNameRecord runs on
// every captured packet from any workload on the node, so no input to it - however malformed, truncated,
// or adversarially crafted - may ever panic the collector process. A recover() backs this up, but the
// goal is that this test never needs it: every case here should also pass with the recover() removed.
func TestHandleNameRecordNeverPanicsOnGarbage(t *testing.T) {
	const eventLen = 16 + 16 + 2 + 2 + 1 + 3 + 4
	cases := [][]byte{
		nil,
		{},
		make([]byte, eventLen-1), // shorter than the fixed header alone
		make([]byte, eventLen),   // header only, zero-length payload
		append(make([]byte, eventLen), make([]byte, 34)...), // the exact 34-byte-body TLS crash shape, routed as if it were TLS
	}
	// The TLS case above needs `kind` set to nameKindTLSClientHello (byte 36) and a plausible `length`
	// (byte 40, little-endian u32 = 34) to actually reach the TLS parser rather than being skipped by the
	// dport sentinel header. The DNS equivalent reuses the same cases with the DNS kind.
	tlsCase := append([]byte{}, cases[len(cases)-1]...)
	tlsCase[36] = nameKindTLSClientHello
	tlsCase[40] = 34
	cases[len(cases)-1] = tlsCase

	o := &Observer{}
	for i, raw := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d: handleNameRecord panicked: %v", i, r)
				}
			}()
			o.handleNameRecord(raw)
		}()
	}
}

// TestHandleNameRecordDecodesDnsLatencyPastNameCap pins a real bug caught before this shipped:
// NAME_KIND_DNS_LATENCY repurposes name_event's `len` field as microseconds, not a byte count, but the
// original code read `length` only after it had already been clamped to NAME_CAP (1500) for the
// byte-count cases - a DNS lookup taking longer than 1.5ms (the overwhelming majority of real ones) would
// have been silently truncated down to 1500us. This builds a raw record by hand, past that boundary, and
// checks the decoded observedName carries the real, unclamped value.
func TestHandleNameRecordDecodesDnsLatencyPastNameCap(t *testing.T) {
	const eventLen = 16 + 16 + 2 + 2 + 1 + 3 + 4
	raw := make([]byte, eventLen)                                                    // no data[] payload at all for this kind - just the fixed header
	copy(raw[0:16], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 10, 0, 0, 1})   // saddr = ::ffff:10.0.0.1
	copy(raw[16:32], []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 10, 0, 0, 53}) // daddr = ::ffff:10.0.0.53
	binary.LittleEndian.PutUint16(raw[34:36], 53)                                    // dport, matching the query event's own dport=53 convention
	raw[36] = nameKindDNSLatency
	const wantUs = 47_500 // well past NAME_CAP=1500, representative of a perfectly ordinary real lookup
	binary.LittleEndian.PutUint32(raw[40:44], wantUs)

	o := &Observer{}
	o.handleNameRecord(raw)
	names := o.takeNames()
	if len(names) != 1 {
		t.Fatalf("got %d decoded names, want 1", len(names))
	}
	got := names[0]
	if got.kind != nameKindDNSLatency {
		t.Errorf("kind = %d, want nameKindDNSLatency", got.kind)
	}
	if got.dnsRttUs != wantUs {
		t.Errorf("dnsRttUs = %d, want %d (must not be clamped to NAME_CAP)", got.dnsRttUs, wantUs)
	}
	if got.name != "" {
		t.Errorf("name = %q, want empty - this kind carries no payload", got.name)
	}
}

// TestTlsHandshakeOutcomeMapsCSideConstantsExplicitly locks in tlsHandshakeOutcome's deliberate,
// non-numeric mapping from flow.c's own TLS_HANDSHAKE_* constants onto the wire-stable
// continuumv1.TlsHandshakeOutcome enum (see tlsHandshakeOutcome's own doc comment for why the two
// numberings are not, and must never become, the same thing).
func TestTlsHandshakeOutcomeMapsCSideConstantsExplicitly(t *testing.T) {
	cases := []struct {
		raw  uint8
		want continuumv1.TlsHandshakeOutcome
	}{
		{0, continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_UNKNOWN},  // TLS_HANDSHAKE_UNKNOWN
		{1, continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_OK},       // TLS_HANDSHAKE_OK
		{2, continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_FAILED},   // TLS_HANDSHAKE_FAILED
		{99, continuumv1.TlsHandshakeOutcome_TLS_HANDSHAKE_OUTCOME_UNKNOWN}, // anything unrecognized stays unknown, never guessed
	}
	for _, c := range cases {
		if got := tlsHandshakeOutcome(c.raw); got != c.want {
			t.Errorf("tlsHandshakeOutcome(%d) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// TestThermalThrottleCountersReadZeroWithoutAttaching covers CpuFreqChangeCount/ThermalTripCount/
// ThermalThrottle's own nil-map guard - the state a zero-value Observer is in before Open() ever runs,
// and the state it stays in forever on a kernel where openCpuFreqChange/openThermalTrip both failed
// (see their own doc comments): o.objs.CpuFreqChangeCount/ThermalTripCount are nil, and every accessor
// here must read that as a plain 0, never panic on the nil map the way a bare .Lookup call would.
func TestThermalThrottleCountersReadZeroWithoutAttaching(t *testing.T) {
	o := &Observer{}
	if got := o.CpuFreqChangeCount(); got != 0 {
		t.Errorf("CpuFreqChangeCount() = %d, want 0 on a nil map", got)
	}
	if got := o.ThermalTripCount(); got != 0 {
		t.Errorf("ThermalTripCount() = %d, want 0 on a nil map", got)
	}
	cpuFreq, trip := o.ThermalThrottle()
	if cpuFreq != 0 || trip != 0 {
		t.Errorf("ThermalThrottle() = (%d, %d), want (0, 0) on nil maps", cpuFreq, trip)
	}
}
