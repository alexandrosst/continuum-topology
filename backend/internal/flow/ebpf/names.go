// Package ebpf: the two hostnames a connection still carries in the clear before anything is encrypted -
// the domain name in a DNS query, and the server name (SNI) in a TLS ClientHello. The kernel side (see
// flow.c's observe_egress) does no protocol parsing at all: it copies a bounded prefix of the raw packet
// bytes into a ring buffer, and everything below - DNS name decompression, walking TLS extensions -
// happens here, in ordinary, fully unit-testable Go, deliberately kept off the unverifiable kernel side.
//
// Neither parser is a general-purpose DNS or TLS library: each reads just enough of one specific message
// shape to pull out one name, and returns ok=false rather than guessing at anything malformed, truncated,
// or simply not what it expected - a packet that merely starts with the right magic bytes (see flow.c's
// own comment on that) is exactly the case this has to be able to reject cheaply.
package ebpf

import "encoding/binary"

// ParseDNSQueryName reads the question name out of a raw DNS message (the payload flow.c copied
// verbatim starting at the UDP header). It only looks at the first question, which is what every
// resolver library actually sends (multiple questions in one message is legal DNS but not real traffic),
// and never follows a compression pointer (0xC0 high bits) - the question name in a query is always
// written out in full, since there is nothing earlier in the message it could point back to.
func ParseDNSQueryName(payload []byte) (string, bool) {
	// Header (12 bytes): ID(2) flags(2) qdcount(2) ancount(2) nscount(2) arcount(2).
	if len(payload) < 12 {
		return "", false
	}
	qdcount := binary.BigEndian.Uint16(payload[4:6])
	if qdcount == 0 {
		return "", false
	}
	// QR bit (top bit of byte 2) must be 0: a query, not a response, is all this program ever captures
	// (it only runs on egress), but a defensive check costs nothing and documents the assumption.
	if payload[2]&0x80 != 0 {
		return "", false
	}
	name, next, ok := readDNSName(payload, 12)
	if !ok || name == "" {
		return "", false
	}
	// A question also has qtype(2) qclass(2) right after the name; require them to be present so a
	// truncated capture (NAME_CAP cut it off mid-message) is rejected rather than handed over as if
	// complete.
	if next+4 > len(payload) {
		return "", false
	}
	return name, true
}

// readDNSName reads one DNS name (a sequence of length-prefixed labels ending in a zero length) starting
// at off, without following compression pointers - the caller (a query's own question name) never needs
// them, and rejecting one outright is simpler and safer than partially supporting a feature whose whole
// point is jumping backward through the message.
func readDNSName(payload []byte, off int) (string, int, bool) {
	var labels []string
	total := 0
	for {
		if off >= len(payload) {
			return "", 0, false
		}
		n := int(payload[off])
		if n == 0 {
			off++
			break
		}
		if n&0xc0 != 0 {
			return "", 0, false // a compression pointer - not expected here, see the doc comment
		}
		off++
		if off+n > len(payload) {
			return "", 0, false
		}
		label := payload[off : off+n]
		for _, c := range label {
			// A DNS label is any byte in the wire format; keep this to what is worth showing and safe to
			// carry as a string elsewhere (a log line, a UI chip) rather than passing arbitrary bytes
			// through as if they were text.
			if c < 0x20 || c == 0x7f {
				return "", 0, false
			}
		}
		labels = append(labels, string(label))
		off += n
		total += n + 1
		if total > 253 { // RFC 1035's own limit on a whole name's wire length
			return "", 0, false
		}
		if len(labels) > 127 { // a name has at most 127 labels (each at least 2 bytes incl. length)
			return "", 0, false
		}
	}
	if len(labels) == 0 {
		return "", off, false // the root name alone is not a query anyone is asking about
	}
	name := labels[0]
	for _, l := range labels[1:] {
		name += "." + l
	}
	return name, off, true
}

// ParseTLSClientHelloSNI reads the server_name extension's HostName entry out of a raw TLS ClientHello
// (the payload flow.c copied verbatim starting at the record layer). flow.c has already checked the
// record type, the major version, and the handshake type before ever submitting this, so those bytes are
// re-checked here mostly as documentation of the shape being walked, not as the first line of defense.
func ParseTLSClientHelloSNI(payload []byte) (string, bool) {
	// TLS record header: type(1) version(2) length(2).
	if len(payload) < 5 || payload[0] != 0x16 {
		return "", false
	}
	recordLen := int(binary.BigEndian.Uint16(payload[3:5]))
	body := payload[5:]
	if recordLen < len(body) {
		body = body[:recordLen] // a second flight tacked onto the same packet - stop at this record's end
	}
	// Handshake header: type(1) length(3).
	if len(body) < 4 || body[0] != 0x01 {
		return "", false
	}
	hsLen := int(body[1])<<16 | int(body[2])<<8 | int(body[3])
	body = body[4:]
	if hsLen < len(body) {
		body = body[:hsLen]
	}
	// ClientHello: client_version(2) random(32) session_id_len(1) session_id(var) cipher_suites_len(2)
	// cipher_suites(var) compression_methods_len(1) compression_methods(var) [extensions_len(2) extensions(var)].
	if len(body) < 34 {
		return "", false
	}
	off := 2 + 32
	sidLen := int(body[off])
	off++
	off += sidLen
	if off+2 > len(body) {
		return "", false
	}
	csLen := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2 + csLen
	if off+1 > len(body) {
		return "", false
	}
	cmLen := int(body[off])
	off++
	off += cmLen
	if off+2 > len(body) {
		return "", false
	}
	extTotal := int(binary.BigEndian.Uint16(body[off : off+2]))
	off += 2
	extEnd := off + extTotal
	if extEnd > len(body) {
		extEnd = len(body) // a capture truncated mid-extensions: read what is actually there
	}
	for off+4 <= extEnd {
		extType := binary.BigEndian.Uint16(body[off : off+2])
		extLen := int(binary.BigEndian.Uint16(body[off+2 : off+4]))
		off += 4
		if off+extLen > extEnd {
			return "", false // a length that runs past what we have: stop rather than read garbage
		}
		if extType == 0 { // server_name
			return parseSNIExtension(body[off : off+extLen])
		}
		off += extLen
	}
	return "", false
}

// parseSNIExtension reads the server_name extension's body: server_name_list_len(2), then one or more
// entries of name_type(1) name_len(2) name(var). Only name_type 0 (host_name) exists in practice; a
// client can in principle send more than one entry, but no real client does, so only the first is read.
func parseSNIExtension(b []byte) (string, bool) {
	if len(b) < 2 {
		return "", false
	}
	listLen := int(binary.BigEndian.Uint16(b[0:2]))
	b = b[2:]
	if listLen < len(b) {
		b = b[:listLen]
	}
	if len(b) < 3 || b[0] != 0 {
		return "", false
	}
	nameLen := int(binary.BigEndian.Uint16(b[1:3]))
	b = b[3:]
	if nameLen > len(b) {
		return "", false
	}
	name := b[:nameLen]
	if len(name) == 0 || len(name) > 255 {
		return "", false
	}
	for _, c := range name {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	return string(name), true
}
