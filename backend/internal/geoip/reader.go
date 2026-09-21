// Package geoip is a small, dependency-free reader for MaxMind DB (.mmdb) files, the format used
// by MaxMind GeoLite2 and DB-IP Lite. It answers one question: given a public IP address, where is
// it probably? The answer is only ever a suggestion for a human to confirm. Nothing here talks to
// the network; the operator supplies the database file.
//
// The reader is defensive: a corrupt or truncated file yields errors or misses, never a panic or an
// unbounded loop (pointer depth, recursion depth, collection sizes and total work are all capped).
package geoip

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"time"
)

const (
	metaMarker    = "\xAB\xCD\xEFMaxMind.com"
	maxMetaBytes  = 128 * 1024 // the spec caps the metadata section at 128 KiB
	dataSeparator = 16         // zero bytes between the search tree and the data section

	maxDepth      = 32    // nesting of maps/arrays plus pointer hops
	maxCollection = 4096  // entries in one decoded map or array
	maxWork       = 50000 // values decoded for one record (guards pointer fan-out)
)

var errCorrupt = errors.New("geoip: corrupt database")

func corrupt(format string, a ...any) error {
	return fmt.Errorf("%w: %s", errCorrupt, fmt.Sprintf(format, a...))
}

// decoder reads the data section (or the metadata section) of a database. Offsets are relative to
// the start of buf, which is also what pointers are relative to.
type decoder struct {
	buf  []byte
	work int
}

func be(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

// decode returns the value at off and the offset just past it (pointers do not advance into their target).
func (d *decoder) decode(off, depth int) (any, int, error) {
	if depth > maxDepth {
		return nil, 0, corrupt("nesting too deep")
	}
	if d.work--; d.work < 0 {
		return nil, 0, corrupt("record too large")
	}
	if off < 0 || off >= len(d.buf) {
		return nil, 0, corrupt("offset %d outside data", off)
	}
	ctrl := d.buf[off]
	off++
	typ := int(ctrl >> 5)

	if typ == 1 { // pointer
		ss := int(ctrl>>3) & 3
		n := ss + 1
		if off+n > len(d.buf) {
			return nil, 0, corrupt("truncated pointer")
		}
		v := be(d.buf[off : off+n])
		switch ss {
		case 0, 1, 2:
			v |= uint64(ctrl&7) << (8 * uint(n))
			if ss == 1 {
				v += 2048
			} else if ss == 2 {
				v += 526336
			}
		}
		if v >= uint64(len(d.buf)) {
			return nil, 0, corrupt("pointer outside data")
		}
		val, _, err := d.decode(int(v), depth+1)
		return val, off + n, err
	}

	if typ == 0 { // extended type
		if off >= len(d.buf) {
			return nil, 0, corrupt("truncated type")
		}
		typ = 7 + int(d.buf[off])
		off++
		if typ < 8 {
			return nil, 0, corrupt("bad extended type")
		}
	}

	size := int(ctrl & 0x1f)
	switch {
	case size == 29:
		if off+1 > len(d.buf) {
			return nil, 0, corrupt("truncated size")
		}
		size = 29 + int(d.buf[off])
		off++
	case size == 30:
		if off+2 > len(d.buf) {
			return nil, 0, corrupt("truncated size")
		}
		size = 285 + int(be(d.buf[off:off+2]))
		off += 2
	case size == 31:
		if off+3 > len(d.buf) {
			return nil, 0, corrupt("truncated size")
		}
		size = 65821 + int(be(d.buf[off:off+3]))
		off += 3
	}

	// payload returns the size bytes that follow, or an error when the file ends first.
	payload := func(max int) ([]byte, error) {
		if size > max || size < 0 || off+size > len(d.buf) {
			return nil, corrupt("bad length %d for type %d", size, typ)
		}
		return d.buf[off : off+size], nil
	}

	switch typ {
	case 2: // string
		b, err := payload(len(d.buf))
		return string(b), off + size, err
	case 4: // bytes
		b, err := payload(len(d.buf))
		return b, off + size, err
	case 3: // double
		if size != 8 {
			return nil, 0, corrupt("double of %d bytes", size)
		}
		b, err := payload(8)
		if err != nil {
			return nil, 0, err
		}
		return math.Float64frombits(be(b)), off + 8, nil
	case 15: // float
		if size != 4 {
			return nil, 0, corrupt("float of %d bytes", size)
		}
		b, err := payload(4)
		if err != nil {
			return nil, 0, err
		}
		return float64(math.Float32frombits(uint32(be(b)))), off + 4, nil
	case 5, 6, 9: // uint16, uint32, uint64
		b, err := payload(map[int]int{5: 2, 6: 4, 9: 8}[typ])
		if err != nil {
			return nil, 0, err
		}
		return be(b), off + size, nil
	case 10: // uint128: never needed here, skipped
		if _, err := payload(16); err != nil {
			return nil, 0, err
		}
		return nil, off + size, nil
	case 8: // int32
		b, err := payload(4)
		if err != nil {
			return nil, 0, err
		}
		return int64(int32(uint32(be(b)))), off + size, nil
	case 14: // boolean: the size field is the value
		if size > 1 {
			return nil, 0, corrupt("bad boolean")
		}
		return size == 1, off, nil
	case 7: // map
		if size > maxCollection || size*2 > len(d.buf)-off {
			return nil, 0, corrupt("map of %d entries", size)
		}
		m := make(map[string]any, size)
		for i := 0; i < size; i++ {
			k, next, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, 0, corrupt("map key is not a string")
			}
			v, next, err := d.decode(next, depth+1)
			if err != nil {
				return nil, 0, err
			}
			m[ks], off = v, next
		}
		return m, off, nil
	case 11: // array
		if size > maxCollection || size > len(d.buf)-off {
			return nil, 0, corrupt("array of %d entries", size)
		}
		a := make([]any, 0, size)
		for i := 0; i < size; i++ {
			v, next, err := d.decode(off, depth+1)
			if err != nil {
				return nil, 0, err
			}
			a, off = append(a, v), next
		}
		return a, off, nil
	}
	return nil, 0, corrupt("unsupported type %d", typ)
}

// DB is an open database. It is immutable after Open and safe for concurrent use.
type DB struct {
	buf        []byte // whole file
	data       []byte // data section
	meta       map[string]any
	nodeCount  int
	recordSize int
	ipVersion  int
	nodeBytes  int
	v4Start    int // node where IPv4 lookups begin (0 in an IPv4 tree)

	dbType      string
	description string
	buildEpoch  int64
}

// Info describes the database for display and attribution.
type Info struct {
	DatabaseType string
	Description  string // description.en when the database has one
	BuiltAt      time.Time
	IPVersion    int
}

// Open reads the whole file into memory (8 to 130 MB for the free databases) and validates it.
func Open(path string) (*DB, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("geoip: %w", err)
	}
	return openBytes(buf)
}

// Close is a no-op, kept so callers can treat the database like other resources.
func (db *DB) Close() error { return nil }

func uintField(m map[string]any, k string) (uint64, bool) {
	switch v := m[k].(type) {
	case uint64:
		return v, true
	case int64:
		if v >= 0 {
			return uint64(v), true
		}
	}
	return 0, false
}

func openBytes(buf []byte) (*DB, error) {
	at := bytes.LastIndex(buf, []byte(metaMarker))
	if at < 0 {
		return nil, corrupt("not a MaxMind DB file (no metadata marker)")
	}
	metaBuf := buf[at+len(metaMarker):]
	if len(metaBuf) > maxMetaBytes {
		metaBuf = metaBuf[:maxMetaBytes]
	}
	md := &decoder{buf: metaBuf, work: maxWork}
	mv, _, err := md.decode(0, 0)
	if err != nil {
		return nil, fmt.Errorf("geoip: metadata: %w", err)
	}
	meta, ok := mv.(map[string]any)
	if !ok {
		return nil, corrupt("metadata is not a map")
	}
	nodes, ok1 := uintField(meta, "node_count")
	rs, ok2 := uintField(meta, "record_size")
	ipv, ok3 := uintField(meta, "ip_version")
	if !ok1 || !ok2 || !ok3 {
		return nil, corrupt("metadata lacks node_count, record_size or ip_version")
	}
	if nodes == 0 || nodes > math.MaxInt32 {
		return nil, corrupt("node_count %d", nodes)
	}
	if rs != 24 && rs != 28 && rs != 32 {
		return nil, corrupt("record_size %d", rs)
	}
	if ipv != 4 && ipv != 6 {
		return nil, corrupt("ip_version %d", ipv)
	}
	if major, ok := uintField(meta, "binary_format_major_version"); ok && major != 2 {
		return nil, corrupt("unsupported format version %d", major)
	}
	db := &DB{buf: buf, meta: meta, nodeCount: int(nodes), recordSize: int(rs), ipVersion: int(ipv), nodeBytes: int(rs) / 4}
	treeSize := db.nodeCount * db.nodeBytes
	if treeSize+dataSeparator > at {
		return nil, corrupt("search tree runs past the end of the file")
	}
	db.data = buf[treeSize+dataSeparator : at]
	db.dbType, _ = meta["database_type"].(string)
	if d, ok := meta["description"].(map[string]any); ok {
		db.description, _ = d["en"].(string)
	}
	if e, ok := uintField(meta, "build_epoch"); ok && e < 1<<40 {
		db.buildEpoch = int64(e)
	}
	if db.ipVersion == 6 { // IPv4 lives under ::/96, 96 zero bits down from the root
		node := 0
		for i := 0; i < 96 && node < db.nodeCount; i++ {
			if node, err = db.child(node, 0); err != nil {
				return nil, err
			}
		}
		db.v4Start = node
	}
	return db, nil
}

// Info returns what the database says about itself.
func (db *DB) Info() Info {
	in := Info{DatabaseType: db.dbType, Description: db.description, IPVersion: db.ipVersion}
	if db.buildEpoch > 0 {
		in.BuiltAt = time.Unix(db.buildEpoch, 0).UTC()
	}
	return in
}

// child reads one record of a search tree node.
func (db *DB) child(node, bit int) (int, error) {
	if node < 0 || node >= db.nodeCount {
		return 0, corrupt("node %d outside tree", node)
	}
	b := db.buf[node*db.nodeBytes : (node+1)*db.nodeBytes]
	switch db.recordSize {
	case 24:
		return int(be(b[bit*3 : bit*3+3])), nil
	case 28:
		if bit == 0 {
			return int(b[3]>>4)<<24 | int(be(b[0:3])), nil
		}
		return int(b[3]&0x0f)<<24 | int(be(b[4:7])), nil
	default:
		return int(binary.BigEndian.Uint32(b[bit*4:])), nil
	}
}

// find walks the tree for the address and returns the decoded record.
func (db *DB) find(ip []byte, v4 bool) (map[string]any, bool, error) {
	node := 0
	if v4 {
		node = db.v4Start
	}
	for i := 0; i < len(ip)*8 && node < db.nodeCount; i++ {
		bit := int(ip[i>>3]>>(7-uint(i&7))) & 1
		var err error
		if node, err = db.child(node, bit); err != nil {
			return nil, false, err
		}
	}
	switch {
	case node == db.nodeCount:
		return nil, false, nil // the database knows nothing about this range
	case node < db.nodeCount:
		return nil, false, corrupt("search ran out of address bits")
	}
	off := node - db.nodeCount - dataSeparator
	if off < 0 {
		return nil, false, corrupt("record points into the separator")
	}
	d := &decoder{buf: db.data, work: maxWork}
	v, _, err := d.decode(off, 0)
	if err != nil {
		return nil, false, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false, corrupt("record is not a map")
	}
	return m, true, nil
}
