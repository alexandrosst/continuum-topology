package geoip

import (
	"net/netip"
	"strings"
	"unicode/utf8"
)

// Result is a suggested location for an address. It is a suggestion: an agent behind a cloud NAT
// resolves to the provider's egress, not to the cluster.
type Result struct {
	Country     string   `json:"country"`
	CountryName string   `json:"countryName,omitempty"`
	City        string   `json:"city,omitempty"`
	Region      string   `json:"region,omitempty"`
	Lat         *float64 `json:"lat,omitempty"`
	Lng         *float64 `json:"lng,omitempty"`
	AccuracyKm  *int     `json:"accuracyKm,omitempty"`
	Level       string   `json:"level"` // "city" when a city and coordinates are known, else "country"
	Database    string   `json:"database,omitempty"`
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Locatable reports whether an address could ever have a location: private, loopback, link-local,
// carrier-grade NAT, unspecified and multicast addresses cannot.
func Locatable(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsMulticast() && !ip.IsUnspecified() && !cgnat.Contains(ip)
}

// Lookup returns the suggested location of ip. It reports false for addresses that can never be
// located (without consulting the database), when the database has no record, and when the record
// is unreadable.
func (db *DB) Lookup(ip netip.Addr) (Result, bool) {
	r, ok, _ := db.lookup(ip)
	return r, ok
}

func (db *DB) lookup(ip netip.Addr) (Result, bool, error) {
	ip = ip.Unmap().WithZone("")
	if !Locatable(ip) {
		return Result{}, false, nil
	}
	var rec map[string]any
	var found bool
	var err error
	if ip.Is4() {
		a := ip.As4()
		rec, found, err = db.find(a[:], true)
	} else if db.ipVersion == 6 {
		a := ip.As16()
		rec, found, err = db.find(a[:], false)
	}
	if err != nil || !found {
		return Result{}, false, err
	}
	res := extract(rec)
	if res.Country == "" && res.City == "" && res.Lat == nil {
		return Result{}, false, nil
	}
	res.Database = db.dbType
	return res, true, nil
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func text(v any) string {
	s, _ := v.(string)
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	if len(s) > 128 {
		s = s[:128]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

func englishName(m map[string]any) string { return text(asMap(m["names"])["en"]) }

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, n == n // not NaN
	case uint64:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func extract(rec map[string]any) Result {
	var r Result
	country := asMap(rec["country"])
	if len(country) == 0 {
		country = asMap(rec["registered_country"])
	}
	if code := text(country["iso_code"]); len(code) == 2 {
		r.Country = strings.ToUpper(code)
	}
	r.CountryName = englishName(country)
	r.City = englishName(asMap(rec["city"]))
	if subs, ok := rec["subdivisions"].([]any); ok && len(subs) > 0 {
		r.Region = englishName(asMap(subs[0]))
	}
	if loc := asMap(rec["location"]); loc != nil {
		lat, ok1 := number(loc["latitude"])
		lng, ok2 := number(loc["longitude"])
		if ok1 && ok2 && lat >= -90 && lat <= 90 && lng >= -180 && lng <= 180 {
			r.Lat, r.Lng = &lat, &lng
		}
		if acc, ok := number(loc["accuracy_radius"]); ok && acc >= 0 && acc <= 20000 {
			km := int(acc)
			r.AccuracyKm = &km
		}
	}
	r.Level = "country"
	if r.City != "" && r.Lat != nil && r.Lng != nil {
		r.Level = "city"
	}
	return r
}
