package ebpf

import "testing"

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
