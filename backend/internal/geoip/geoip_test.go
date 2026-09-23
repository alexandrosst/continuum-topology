package geoip

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"
)

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func openSpec(t *testing.T, s spec) *DB {
	t.Helper()
	db, err := Open(writeTemp(t, s.build()))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestLookupAcrossRecordSizesAndVersions(t *testing.T) {
	for _, rs := range []int{24, 28, 32} {
		for _, ver := range []int{4, 6} {
			db := openSpec(t, sample(rs, ver))
			name := func(s string) string {
				return s + " rs=" + string(rune('0'+rs/10)) + string(rune('0'+rs%10)) + " v" + string(rune('0'+ver))
			}

			r, ok := db.Lookup(ip("203.0.113.24"))
			if !ok || r.Country != "GR" || r.CountryName != "Greece" || r.City != "Patras" || r.Region != "Western Greece" || r.Level != "city" {
				t.Fatalf("%s: %+v %v", name("city hit"), r, ok)
			}
			if r.Lat == nil || *r.Lat != 38.2466 || r.Lng == nil || *r.Lng != 21.7346 || r.AccuracyKm == nil || *r.AccuracyKm != 20 {
				t.Fatalf("%s: bad coordinates %+v", name("city hit"), r)
			}
			if r.Database != "DBIP-City-Lite" {
				t.Fatalf("database = %q", r.Database)
			}

			r, ok = db.Lookup(ip("198.51.100.5")) // country-only record
			if !ok || r.Country != "US" || r.Level != "country" || r.City != "" || r.Lat != nil || r.Lng != nil || r.AccuracyKm != nil {
				t.Fatalf("%s: %+v", name("country"), r)
			}
			r, ok = db.Lookup(ip("198.51.100.200")) // narrower prefix inside it
			if !ok || r.City != "Springfield" || r.Level != "city" || *r.Lng != -89.65 {
				t.Fatalf("%s: %+v", name("nested"), r)
			}
			r, ok = db.Lookup(ip("192.0.2.9")) // registered_country fallback
			if !ok || r.Country != "NL" || r.CountryName != "Netherlands" || r.Level != "country" {
				t.Fatalf("%s: %+v", name("registered country"), r)
			}
			r, ok = db.Lookup(ip("8.8.8.8"))
			if !ok || r.Country != "US" {
				t.Fatalf("%s: broad range must lose to the specific one: %+v", name("8/8"), r)
			}
			if r, ok = db.Lookup(ip("77.1.1.1")); !ok || r.Country != "AU" {
				t.Fatalf("%s: %+v", name("broad"), r)
			}
			if r, ok = db.Lookup(ip("::ffff:203.0.113.24")); !ok || r.City != "Patras" { // mapped form
				t.Fatalf("%s: %+v", name("v4-mapped"), r)
			}
			if _, ok = db.Lookup(ip("199.1.1.1")); ok {
				t.Fatal("an address in no range must miss")
			}

			r, ok = db.Lookup(ip("2001:db8::1"))
			if ver == 6 {
				if !ok || r.City != "Berlin" || r.Country != "DE" || r.Level != "city" {
					t.Fatalf("%s: %+v", name("v6"), r)
				}
				if r, ok = db.Lookup(ip("2a00:1450::1")); !ok || r.Country != "FR" || r.Level != "country" {
					t.Fatalf("%s: %+v", name("v6 country"), r)
				}
				if _, ok = db.Lookup(ip("2600::1")); ok {
					t.Fatal("v6 miss expected")
				}
			} else if ok {
				t.Fatal("an IPv6 address cannot match in an IPv4 tree")
			}
		}
	}
}

func TestLookupASN(t *testing.T) {
	for _, rs := range []int{24, 28, 32} {
		for _, ver := range []int{4, 6} {
			db := openSpec(t, asnSample(rs, ver))

			a, ok := db.LookupASN(ip("203.0.113.24"))
			if !ok || a.ASN != 64512 || a.Org != "Example Networks LLC" {
				t.Fatalf("rs=%d v%d: %+v %v", rs, ver, a, ok)
			}
			if a, ok = db.LookupASN(ip("198.51.100.5")); !ok || a.ASN != 15169 || a.Org != "Google LLC" {
				t.Fatalf("rs=%d v%d: %+v", rs, ver, a)
			}
			if _, ok = db.LookupASN(ip("199.1.1.1")); ok {
				t.Fatal("an address in no range must miss")
			}
			if _, ok = db.LookupASN(ip("10.0.0.1")); ok {
				t.Fatal("a private address must never reach the database")
			}

			if ver == 6 {
				if a, ok = db.LookupASN(ip("2001:db8::1")); !ok || a.ASN != 64512 {
					t.Fatalf("rs=%d v6: %+v", rs, a)
				}
			}

			// An ASN-shaped record has no city/country fields at all: the ordinary Lookup must find nothing
			// usable in it rather than returning a zero-value hit.
			if _, ok := db.Lookup(ip("203.0.113.24")); ok {
				t.Fatal("Lookup must not treat an ASN record as a location")
			}
		}
	}
}

func TestInfo(t *testing.T) {
	db := openSpec(t, sample(24, 6))
	in := db.Info()
	if in.DatabaseType != "DBIP-City-Lite" || in.Description != "DB-IP City Lite test" || in.IPVersion != 6 || in.BuiltAt.Unix() != 1789000000 || in.BuiltAt.Location() != time.UTC {
		t.Fatalf("%+v", in)
	}
	if db.Close() != nil {
		t.Fatal("close")
	}
}

func TestUnlocatableAddressesNeverReachTheDatabase(t *testing.T) {
	// A database whose every address has a record: only the short-circuit can produce a miss.
	s := spec{recordSize: 24, ipVersion: 6, dbType: "x", pointers: true, entries: []entry{{"0.0.0.0/0", countryRec("ZZ", "Everywhere")}, {"2000::/3", countryRec("ZZ", "Everywhere")}}}
	db := openSpec(t, s)
	if _, ok := db.Lookup(ip("8.8.4.4")); !ok {
		t.Fatal("control lookup should hit")
	}
	for _, a := range []string{
		"10.1.2.3", "172.16.0.1", "192.168.1.1", "127.0.0.1", "169.254.1.1", "100.64.0.1", "100.127.255.255", "0.0.0.0",
		"224.0.0.1", "239.1.1.1", "::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "::ffff:10.0.0.1", "::ffff:100.100.100.100",
	} {
		if r, ok := db.Lookup(ip(a)); ok {
			t.Errorf("%s located as %+v", a, r)
		}
	}
	for _, a := range []string{"100.63.255.255", "100.128.0.0", "172.32.0.1", "203.0.113.24", "2001:4860:4860::8888"} {
		if _, ok := db.Lookup(ip(a)); !ok {
			t.Errorf("%s should be locatable", a)
		}
	}
	if _, ok := db.Lookup(netip.Addr{}); ok {
		t.Error("the zero address must miss")
	}
}

func TestIPv4MissesWhenTheV6TreeHasNoMappedSubtree(t *testing.T) {
	s := spec{recordSize: 28, ipVersion: 6, dbType: "x", entries: []entry{{"2001:db8::/32", countryRec("DE", "Germany")}}}
	db := openSpec(t, s)
	if _, ok := db.Lookup(ip("8.8.8.8")); ok {
		t.Fatal("no IPv4 data: expected a miss")
	}
	if _, ok := db.Lookup(ip("2001:db8::7")); !ok {
		t.Fatal("v6 hit expected")
	}
}

func TestLargeSizesAndFarPointers(t *testing.T) {
	// A filler record pushes later strings past the 3-byte and 4-byte pointer thresholds and exercises
	// the 285+2 and 65821+3 size encodings.
	long := strings.Repeat("x", 300)
	s := spec{recordSize: 32, ipVersion: 4, dbType: "big", pointers: true, entries: []entry{
		{"10.0.0.0/8", mapv{{"filler", blob(make([]byte, 600000))}, {"long", long}}},
		{"11.0.0.0/8", cityRec("GR", "Greece", "Athens", "Attica", 37.98, 23.73, 10)},
		{"12.0.0.0/8", cityRec("GR", "Greece", "Athens", "Attica", 37.98, 23.73, 10)}, // strings now come by far pointers
		{"13.0.0.0/8", countryRec("GR", "Greece")},
	}}
	raw := s.build()
	if len(raw) < 600000 {
		t.Fatal("fixture too small")
	}
	db, err := openBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"11.1.1.1", "12.1.1.1", "13.1.1.1"} {
		r, ok := db.Lookup(ip(a))
		if !ok || r.Country != "GR" || r.CountryName != "Greece" {
			t.Fatalf("%s: %+v", a, r)
		}
	}
	if r, _ := db.Lookup(ip("12.1.1.1")); r.City != "Athens" {
		t.Fatalf("far-pointer city = %q", r.City)
	}
	rec, found, err := db.find([]byte{10, 1, 1, 1}, true) // filler record decodes, private-range check is in Lookup only
	if err != nil || !found || len(rec["long"].(string)) != 300 || len(rec["filler"].([]byte)) != 600000 {
		t.Fatalf("large record: found=%v err=%v", found, err)
	}
}

func TestDecoderTypesAndPointerForms(t *testing.T) {
	e := &encoder{}
	e.put(mapv{
		{"i", i32(-7)}, {"f", f32(1.5)}, {"b", true}, {"n", false}, {"u", u128(bytes.Repeat([]byte{1}, 16))},
		{"z", u32(0)}, {"big", u64(1 << 40)}, {"arr", arr{u16(1), "two", f64(3)}},
	})
	d := &decoder{buf: e.buf.Bytes(), work: 1000}
	v, next, err := d.decode(0, 0)
	if err != nil || next != len(e.buf.Bytes()) {
		t.Fatalf("err=%v next=%d/%d", err, next, e.buf.Len())
	}
	m := v.(map[string]any)
	if m["i"] != int64(-7) || m["f"] != 1.5 || m["b"] != true || m["n"] != false || m["z"] != uint64(0) || m["big"] != uint64(1<<40) || m["u"] != nil {
		t.Fatalf("%v", m)
	}
	if a := m["arr"].([]any); len(a) != 3 || a[0] != uint64(1) || a[1] != "two" || a[2] != 3.0 {
		t.Fatalf("%v", a)
	}
	// ss=3 pointer (four-byte form): buf = [string "hi"] [pointer -> 0]
	buf := []byte{0x42, 'h', 'i', 0x38, 0, 0, 0, 0}
	d = &decoder{buf: buf, work: 10}
	if v, next, err := d.decode(3, 0); err != nil || v != "hi" || next != 8 {
		t.Fatalf("ss=3: %v %d %v", v, next, err)
	}
}

func TestHostileDataNeverLoopsOrPanics(t *testing.T) {
	cases := map[string][]byte{
		"pointer to itself":  {0x20, 0x00},
		"pointer cycle":      {0x20, 0x02, 0x00, 0x20, 0x00},
		"huge map":           {0xFF, 0xFF, 0xFF, 0xFF},                              // size 65821+, then truncated
		"map claims 4000":    append([]byte{0xFE, 0x0E, 0x83}, make([]byte, 20)...), // 285+3715 entries, no room
		"truncated string":   {0x45, 'a'},
		"bad double":         {0x63, 1, 2, 3},
		"key not string":     {0xE1, 0xA1, 1, 0x40},
		"extended zero":      {0x00, 0x00},
		"container":          {0x00, 0x05},
		"end marker":         {0x00, 0x06},
		"pointer past end":   {0x3F, 0xFF, 0xFF, 0xFF, 0xFF},
		"empty":              {},
		"nested arrays deep": bytes.Repeat([]byte{0x01, 0x04}, 200), // array of 1, 200 levels
	}
	// An array whose elements all point back at the array: exponential unless the work budget stops it.
	cases["fan-out"] = append([]byte{0x1E, 0x00, 0x04}, bytes.Repeat([]byte{0x20, 0x00}, 285+4)...)
	for name, b := range cases {
		done := make(chan error, 1)
		go func() {
			d := &decoder{buf: b, work: maxWork}
			_, _, err := d.decode(0, 0)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil && name != "nested arrays deep" {
				t.Errorf("%s: expected an error", name)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: decoder did not finish", name)
		}
	}
}

func TestMetadataValidation(t *testing.T) {
	base := sample(24, 6).build()
	if _, err := openBytes(base); err != nil {
		t.Fatal(err)
	}
	if _, err := openBytes([]byte("not a database at all")); err == nil {
		t.Fatal("no marker must fail")
	}
	if _, err := openBytes(nil); err == nil {
		t.Fatal("empty must fail")
	}
	if _, err := Open("/nonexistent/x.mmdb"); err == nil {
		t.Fatal("missing file must fail")
	}
	if _, err := Open(writeTemp(t, []byte("garbage"))); err == nil {
		t.Fatal("garbage file must fail")
	}
	meta := func(nodes, rs, ipv uint64) []byte {
		var out bytes.Buffer
		out.Write(make([]byte, 64))
		out.WriteString(metaMarker)
		e := &encoder{}
		e.put(mapv{{"node_count", u32(nodes)}, {"record_size", u16(rs)}, {"ip_version", u16(ipv)}, {"database_type", "x"}})
		out.Write(e.buf.Bytes())
		return out.Bytes()
	}
	for name, b := range map[string][]byte{
		"zero nodes":         meta(0, 24, 6),
		"record size 30":     meta(1, 30, 6),
		"ip version 5":       meta(1, 24, 5),
		"tree beyond file":   meta(100000, 24, 6),
		"nodes beyond int32": meta(1<<32-1, 32, 4),
	} {
		if _, err := openBytes(b); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := openBytes(meta(1, 24, 4)); err != nil { // control: the helper itself builds something valid
		t.Fatalf("control metadata rejected: %v", err)
	}
	// The metadata marker must be the LAST occurrence: a marker-looking string in the data is not it.
	s := sample(24, 4)
	s.entries[1].rec = countryRec("US", metaMarker)
	if db, err := openBytes(s.build()); err != nil {
		t.Fatalf("marker inside data: %v", err)
	} else if r, ok := db.Lookup(ip("198.51.100.5")); !ok || r.Country != "US" {
		t.Fatalf("lookup with marker in data: %+v", r)
	}
}

func TestJSONShape(t *testing.T) {
	db := openSpec(t, sample(24, 6))
	r, _ := db.Lookup(ip("203.0.113.24"))
	b, _ := json.Marshal(r)
	want := `{"country":"GR","countryName":"Greece","city":"Patras","region":"Western Greece","lat":38.2466,"lng":21.7346,"accuracyKm":20,"level":"city","database":"DBIP-City-Lite"}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	r, _ = db.Lookup(ip("192.0.2.9"))
	b, _ = json.Marshal(r)
	if string(b) != `{"country":"NL","countryName":"Netherlands","level":"country","database":"DBIP-City-Lite"}` {
		t.Fatalf("country-only shape: %s", b)
	}
	// A latitude or longitude of exactly 0 must survive omitempty.
	s := spec{recordSize: 24, ipVersion: 4, dbType: "x", entries: []entry{{"203.0.113.0/24", cityRec("GH", "Ghana", "Null Island", "", 0, 0, 1)}}}
	r, _ = openSpec(t, s).Lookup(ip("203.0.113.1"))
	b, _ = json.Marshal(r)
	if !strings.Contains(string(b), `"lat":0`) || !strings.Contains(string(b), `"lng":0`) {
		t.Fatalf("zero coordinates dropped: %s", b)
	}
	// Out-of-range coordinates are discarded, and the level falls back to country.
	s.entries[0].rec = cityRec("GH", "Ghana", "Nowhere", "", 123, 0, 1)
	if r, _ = openSpec(t, s).Lookup(ip("203.0.113.1")); r.Lat != nil || r.Level != "country" {
		t.Fatalf("bad latitude accepted: %+v", r)
	}
}

// probe exercises every entry point of a possibly corrupt database and fails the test if it panics
// or does not return promptly.
func probe(t *testing.T, label string, b []byte) {
	t.Helper()
	done := make(chan any, 1)
	go func() {
		defer func() { done <- recover() }()
		db, err := openBytes(b)
		if err != nil {
			return
		}
		db.Info()
		for _, a := range []string{"203.0.113.24", "198.51.100.5", "198.51.100.200", "8.8.8.8", "77.1.1.1", "2001:db8::1", "2a00::1", "2600::1", "::ffff:203.0.113.1", "1.2.3.4", "255.255.255.255"} {
			db.Lookup(ip(a))
		}
	}()
	select {
	case p := <-done:
		if p != nil {
			t.Fatalf("%s: panic: %v", label, p)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("%s: did not finish (hang)", label)
	}
}

func TestCorruptFilesNeverPanicOrHang(t *testing.T) {
	for _, rs := range []int{24, 28, 32} {
		for _, ver := range []int{4, 6} {
			good := sample(rs, ver).build()
			for i := 0; i <= 64; i++ { // truncate at every 1/64th
				probe(t, "truncate", good[:len(good)*i/64])
			}
			rng := rand.New(rand.NewSource(int64(rs*10 + ver)))
			for n := 0; n < 1500; n++ { // flip random bytes, more often in the metadata and the tree
				b := append([]byte(nil), good...)
				for k := rng.Intn(6) + 1; k > 0; k-- {
					pos := rng.Intn(len(b))
					switch rng.Intn(3) {
					case 0:
						pos = len(b) - 1 - rng.Intn(min(len(b), 120))
					case 1:
						pos = rng.Intn(min(len(b), 200))
					}
					b[pos] ^= byte(1 + rng.Intn(255))
				}
				probe(t, "flip", b)
			}
		}
	}
	// The same through a real file.
	good := sample(28, 6).build()
	if _, err := Open(writeTemp(t, good[:len(good)/2])); err == nil {
		t.Fatal("half a file opened")
	}
}

// Optional check against real databases, e.g.
//
//	CONTINUUM_GEOIP_TEST_DB=/tmp/claude-0/geo/country.mmdb CONTINUUM_GEOIP_TEST_CITY_DB=/tmp/claude-0/geo/city.mmdb go test ./internal/geoip -run Real -v
func TestRealDatabases(t *testing.T) {
	country, city := os.Getenv("CONTINUUM_GEOIP_TEST_DB"), os.Getenv("CONTINUUM_GEOIP_TEST_CITY_DB")
	if country == "" && city == "" {
		t.Skip("CONTINUUM_GEOIP_TEST_DB / CONTINUUM_GEOIP_TEST_CITY_DB not set")
	}
	show := func(path string, want map[string]string) *DB {
		db, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		in := db.Info()
		t.Logf("%s: %s (%s) built %s ipv%d", path, in.DatabaseType, in.Description, in.BuiltAt.Format("2006-01-02"), in.IPVersion)
		for a, c := range want {
			r, ok := db.Lookup(ip(a))
			j, _ := json.Marshal(r)
			t.Logf("  %-24s %v %s", a, ok, j)
			if c != "" && (!ok || r.Country != c) {
				t.Errorf("%s: want country %s, got %+v", a, c, r)
			}
		}
		return db
	}
	if country != "" {
		show(country, map[string]string{"8.8.8.8": "US", "1.1.1.1": "", "9.9.9.9": "", "195.251.32.1": "GR", "194.63.238.4": "GR", "2607:f8b0:4005::1": "US", "2001:648::1": "GR", "2001:4860:4860::8888": "", "10.0.0.1": "", "100.64.1.1": ""})
	}
	if city != "" {
		db := show(city, map[string]string{"195.251.32.1": "GR", "8.8.8.8": "US", "2001:648::1": "GR"})
		if r, ok := db.Lookup(ip("195.251.32.1")); !ok || r.Lat == nil || r.Lng == nil {
			t.Errorf("expected coordinates for a Greek address in the city database: %+v", r)
		}
	}
}
