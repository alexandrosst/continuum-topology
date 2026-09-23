package geoip

import (
	"bytes"
	"encoding/binary"
	"math"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The helpers below write tiny but valid MaxMind DB files so the reader is tested without a
// fixture in the repository.

type kv struct {
	k string
	v any
}

type mapv []kv // ordered map
type f64 float64
type u16 uint16
type u32 uint32
type u64 uint64
type i32 int32
type u128 []byte
type f32 float32
type arr []any
type blob []byte

type encoder struct {
	buf  bytes.Buffer
	seen map[string]int // strings already written, for pointers
	ptrs bool
}

func (e *encoder) ctrl(typ, size int) {
	ext := typ > 7
	first := typ
	if ext {
		first = 0
	}
	var extra []byte
	b := byte(first << 5)
	switch {
	case size < 29:
		b |= byte(size)
	case size < 285:
		b |= 29
		extra = []byte{byte(size - 29)}
	case size < 65821:
		b |= 30
		extra = []byte{byte((size - 285) >> 8), byte(size - 285)}
	default:
		b |= 31
		s := size - 65821
		extra = []byte{byte(s >> 16), byte(s >> 8), byte(s)}
	}
	e.buf.WriteByte(b)
	if ext {
		e.buf.WriteByte(byte(typ - 7))
	}
	e.buf.Write(extra)
}

func (e *encoder) pointer(target int) {
	switch {
	case target < 2048:
		e.buf.Write([]byte{0x20 | byte(target>>8), byte(target)})
	case target < 2048+526336:
		t := target - 2048
		e.buf.Write([]byte{0x28 | byte(t>>16), byte(t >> 8), byte(t)})
	case target < 526336+1<<27:
		t := target - 526336
		e.buf.Write([]byte{0x30 | byte(t>>24), byte(t >> 16), byte(t >> 8), byte(t)})
	default:
		e.buf.Write([]byte{0x38, byte(target >> 24), byte(target >> 16), byte(target >> 8), byte(target)})
	}
}

func be2(v uint64, max int) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	n := max
	for n > 0 && b[8-n] == 0 {
		n--
	}
	return b[8-n:]
}

func (e *encoder) str(s string) {
	if e.ptrs {
		if at, ok := e.seen[s]; ok {
			e.pointer(at)
			return
		}
		e.seen[s] = e.buf.Len()
	}
	e.ctrl(2, len(s))
	e.buf.WriteString(s)
}

func (e *encoder) put(v any) {
	switch x := v.(type) {
	case string:
		e.str(x)
	case mapv:
		e.ctrl(7, len(x))
		for _, p := range x {
			e.str(p.k)
			e.put(p.v)
		}
	case arr:
		e.ctrl(11, len(x))
		for _, y := range x {
			e.put(y)
		}
	case f64:
		e.ctrl(3, 8)
		e.buf.Write(binary.BigEndian.AppendUint64(nil, math.Float64bits(float64(x))))
	case f32:
		e.ctrl(15, 4)
		e.buf.Write(binary.BigEndian.AppendUint32(nil, math.Float32bits(float32(x))))
	case u16:
		b := be2(uint64(x), 2)
		e.ctrl(5, len(b))
		e.buf.Write(b)
	case u32:
		b := be2(uint64(x), 4)
		e.ctrl(6, len(b))
		e.buf.Write(b)
	case u64:
		b := be2(uint64(x), 8)
		e.ctrl(9, len(b))
		e.buf.Write(b)
	case u128:
		e.ctrl(10, len(x))
		e.buf.Write(x)
	case i32:
		b := binary.BigEndian.AppendUint32(nil, uint32(int32(x)))
		e.ctrl(8, 4)
		e.buf.Write(b)
	case bool:
		n := 0
		if x {
			n = 1
		}
		e.ctrl(14, n)
	case blob:
		e.ctrl(4, len(x))
		e.buf.Write(x)
	default:
		panic("encoder: unsupported value")
	}
}

type entry struct {
	prefix string
	rec    any
}

type spec struct {
	recordSize int
	ipVersion  int
	dbType     string
	desc       string
	built      uint64
	pointers   bool
	entries    []entry
}

// ref is one child of a trie node: empty, another node, or a record.
type ref struct {
	kind byte // 0 empty, 'n' node, 'd' data
	idx  int
}

// build assembles the file: search tree, 16 zero bytes, data section, marker, metadata.
func (s spec) build() []byte {
	// data section, one copy per distinct record
	enc := &encoder{seen: map[string]int{}, ptrs: s.pointers}
	offsets := make([]int, len(s.entries))
	for i, e := range s.entries {
		offsets[i] = enc.buf.Len()
		enc.put(e.rec)
	}

	nodes := [][2]ref{{}}
	order := make([]int, len(s.entries))
	for i := range order {
		order[i] = i
	}
	plen := func(i int) int { return netip.MustParsePrefix(s.entries[i].prefix).Bits() }
	sort.SliceStable(order, func(a, b int) bool { return plen(order[a]) < plen(order[b]) })
	for _, i := range order {
		p := netip.MustParsePrefix(s.entries[i].prefix)
		var bits []byte
		n := p.Bits()
		if s.ipVersion == 6 {
			var a [16]byte
			if p.Addr().Is4() {
				a4 := p.Addr().As4()
				copy(a[12:], a4[:])
				n += 96
			} else {
				a = p.Addr().As16()
			}
			bits = a[:]
		} else {
			a4 := p.Addr().As4()
			bits = a4[:]
		}
		node := 0
		for d := 0; d < n; d++ {
			bit := int(bits[d>>3]>>(7-uint(d&7))) & 1
			if d == n-1 {
				nodes[node][bit] = ref{'d', i}
				break
			}
			c := nodes[node][bit]
			switch c.kind {
			case 'n':
				node = c.idx
			default: // empty, or a broader record that this prefix carves a hole in
				nodes = append(nodes, [2]ref{c, c})
				nodes[node][bit] = ref{'n', len(nodes) - 1}
				node = len(nodes) - 1
			}
		}
	}

	count := len(nodes)
	val := func(r ref) uint32 {
		switch r.kind {
		case 'n':
			return uint32(r.idx)
		case 'd':
			return uint32(count + dataSeparator + offsets[r.idx])
		}
		return uint32(count)
	}
	var out bytes.Buffer
	for _, n := range nodes {
		l, r := val(n[0]), val(n[1])
		switch s.recordSize {
		case 24:
			out.Write([]byte{byte(l >> 16), byte(l >> 8), byte(l), byte(r >> 16), byte(r >> 8), byte(r)})
		case 28:
			out.Write([]byte{byte(l >> 16), byte(l >> 8), byte(l), byte(l>>24)<<4 | byte(r>>24)&0xf, byte(r >> 16), byte(r >> 8), byte(r)})
		case 32:
			out.Write(binary.BigEndian.AppendUint32(nil, l))
			out.Write(binary.BigEndian.AppendUint32(nil, r))
		}
	}
	out.Write(make([]byte, dataSeparator))
	out.Write(enc.buf.Bytes())
	out.WriteString(metaMarker)
	me := &encoder{}
	me.put(mapv{
		{"binary_format_major_version", u16(2)},
		{"binary_format_minor_version", u16(0)},
		{"build_epoch", u64(s.built)},
		{"database_type", s.dbType},
		{"description", mapv{{"en", s.desc}}},
		{"ip_version", u16(s.ipVersion)},
		{"languages", arr{"en"}},
		{"node_count", u32(count)},
		{"record_size", u16(s.recordSize)},
	})
	out.Write(me.buf.Bytes())
	return out.Bytes()
}

func names(en string) mapv { return mapv{{"names", mapv{{"en", en}, {"de", en + "-de"}}}} }

func countryRec(iso, name string) mapv {
	return mapv{{"country", append(mapv{{"iso_code", iso}}, names(name)...)}}
}

func cityRec(iso, country, city, region string, lat, lng float64, acc int) mapv {
	return mapv{
		{"city", names(city)},
		{"country", append(mapv{{"iso_code", iso}, {"geoname_id", u32(390903)}, {"is_in_european_union", true}}, names(country)...)},
		{"location", mapv{{"accuracy_radius", u16(acc)}, {"latitude", f64(lat)}, {"longitude", f64(lng)}, {"time_zone", "Europe/Athens"}}},
		{"subdivisions", arr{names(region), names("ignored")}},
	}
}

// sample is the standard fixture: a city record, a country-only record, a registered_country-only
// record, an IPv6 record, and a narrower prefix inside a broader one.
func sample(recordSize, ipVersion int) spec {
	s := spec{recordSize: recordSize, ipVersion: ipVersion, dbType: "DBIP-City-Lite", desc: "DB-IP City Lite test", built: 1789000000, pointers: true}
	s.entries = []entry{
		{"203.0.113.0/24", cityRec("gr", "Greece", "Patras", "Western Greece", 38.2466, 21.7346, 20)},
		{"198.51.100.0/24", countryRec("US", "United States")},
		{"198.51.100.128/25", cityRec("US", "United States", "Springfield", "Illinois", 39.78, -89.65, 50)},
		{"192.0.2.0/24", mapv{{"registered_country", append(mapv{{"iso_code", "NL"}}, names("Netherlands")...)}}},
		{"8.0.0.0/8", countryRec("US", "United States")},
		{"0.0.0.0/1", countryRec("AU", "Australia")}, // a very broad range: the 8/8 above must win
	}
	if ipVersion == 6 {
		s.entries = append(s.entries,
			entry{"2001:db8::/32", cityRec("DE", "Germany", "Berlin", "Berlin", 52.52, 13.405, 5)},
			entry{"2a00::/12", countryRec("FR", "France")})
	}
	return s
}

func asnRec(asn uint32, org string) mapv {
	return mapv{{"autonomous_system_number", u32(asn)}, {"autonomous_system_organization", org}}
}

// asnSample mirrors sample() but with the ASN database's own record shape (no city/country at all):
// LookupASN must read it, and the plain Lookup must find nothing useful in it.
func asnSample(recordSize, ipVersion int) spec {
	s := spec{recordSize: recordSize, ipVersion: ipVersion, dbType: "GeoLite2-ASN", desc: "GeoLite2 ASN test", built: 1789000000, pointers: true}
	s.entries = []entry{
		{"203.0.113.0/24", asnRec(64512, "Example Networks LLC")},
		{"198.51.100.0/24", asnRec(15169, "Google LLC")},
	}
	if ipVersion == 6 {
		s.entries = append(s.entries, entry{"2001:db8::/32", asnRec(64512, "Example Networks LLC")})
	}
	return s
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.mmdb")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
