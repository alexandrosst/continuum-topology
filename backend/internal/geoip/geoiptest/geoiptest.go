// Package geoiptest builds the small databases the tests of the packages that read geoip need, so they do not each carry a copy.
package geoiptest

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TinyGeoDB writes a valid IPv4 .mmdb (record size 24) that maps 203.0.113.0/24 to Patras.
func TinyGeoDB(t *testing.T) string {
	t.Helper()
	str := func(s string) []byte { return append([]byte{0x40 | byte(len(s))}, s...) }
	dbl := func(f float64) []byte { return binary.BigEndian.AppendUint64([]byte{0x68}, math.Float64bits(f)) }
	cat := func(parts ...[]byte) []byte {
		var o []byte
		for _, p := range parts {
			o = append(o, p...)
		}
		return o
	}
	names := func(en string) []byte { return cat([]byte{0xE1}, str("names"), []byte{0xE1}, str("en"), str(en)) }
	rec := cat([]byte{0xE3},
		str("city"), names("Patras"),
		str("country"), cat([]byte{0xE2}, str("iso_code"), str("GR"), str("names"), []byte{0xE1}, str("en"), str("Greece")),
		str("location"), cat([]byte{0xE2}, str("latitude"), dbl(38.2466), str("longitude"), dbl(21.7346)))

	const bits, nodes = 24, 24 // 203.0.113.0/24: 24 nodes, one per prefix bit
	addr := []byte{203, 0, 113}
	var tree []byte
	put3 := func(v int) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }
	for d := 0; d < bits; d++ {
		bit := int(addr[d>>3]>>(7-uint(d&7))) & 1
		next := d + 1
		if d == bits-1 {
			next = nodes + 16 // first byte of the data section
		}
		rec := [2]int{nodes, nodes} // nodes = "no data"
		rec[bit] = next
		tree = append(tree, put3(rec[0])...)
		tree = append(tree, put3(rec[1])...)
	}
	// build_epoch is a uint64: extended type 9 = 7+2, so control byte 0x04 (size 4) then 0x02
	meta := cat([]byte{0xE6},
		str("node_count"), []byte{0xC1, nodes},
		str("record_size"), []byte{0xA1, 24},
		str("ip_version"), []byte{0xA1, 4},
		str("database_type"), str("DBIP-City-Lite"),
		str("description"), cat([]byte{0xE1}, str("en"), str("DB-IP City Lite")),
		str("build_epoch"), []byte{0x04, 0x02, 0x65, 0xB0, 0x00, 0x00})
	file := cat(tree, make([]byte, 16), rec, []byte("\xAB\xCD\xEFMaxMind.com"), meta)
	p := filepath.Join(t.TempDir(), "geo.mmdb")
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TinyASNDB writes a valid IPv4 .mmdb (record size 24) that maps 203.0.113.0/24 to an ASN record -
// the same range TinyGeoDB places in Patras, so the two can be layered to test SetASN's merge.
func TinyASNDB(t *testing.T, asn uint32, org string) string {
	t.Helper()
	// Unlike TinyGeoDB's field names, "autonomous_system_organization" is 30 bytes - one over the 29
	// that fit in a control byte's own 5-bit size field - so, unlike TinyGeoDB's str(), this one must
	// also handle the one-byte-extended length form (spec size 29: actual length = 29 + next byte).
	str := func(s string) []byte {
		n := len(s)
		if n < 29 {
			return append([]byte{0x40 | byte(n)}, s...)
		}
		if n >= 285 {
			t.Fatalf("test string %q too long for this helper", s)
		}
		return append([]byte{0x40 | 29, byte(n - 29)}, s...)
	}
	u32 := func(v uint32) []byte { return []byte{0xC4, byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)} }
	cat := func(parts ...[]byte) []byte {
		var o []byte
		for _, p := range parts {
			o = append(o, p...)
		}
		return o
	}
	rec := cat([]byte{0xE2},
		str("autonomous_system_number"), u32(asn),
		str("autonomous_system_organization"), str(org))

	const bits, nodes = 24, 24 // 203.0.113.0/24: 24 nodes, one per prefix bit
	addr := []byte{203, 0, 113}
	var tree []byte
	put3 := func(v int) []byte { return []byte{byte(v >> 16), byte(v >> 8), byte(v)} }
	for d := 0; d < bits; d++ {
		bit := int(addr[d>>3]>>(7-uint(d&7))) & 1
		next := d + 1
		if d == bits-1 {
			next = nodes + 16
		}
		row := [2]int{nodes, nodes}
		row[bit] = next
		tree = append(tree, put3(row[0])...)
		tree = append(tree, put3(row[1])...)
	}
	meta := cat([]byte{0xE6},
		str("node_count"), []byte{0xC1, nodes},
		str("record_size"), []byte{0xA1, 24},
		str("ip_version"), []byte{0xA1, 4},
		str("database_type"), str("GeoLite2-ASN"),
		str("description"), cat([]byte{0xE1}, str("en"), str("GeoLite2 ASN")),
		str("build_epoch"), []byte{0x04, 0x02, 0x65, 0xB0, 0x00, 0x00})
	file := cat(tree, make([]byte, 16), rec, []byte("\xAB\xCD\xEFMaxMind.com"), meta)
	p := filepath.Join(t.TempDir(), "asn.mmdb")
	if err := os.WriteFile(p, file, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
