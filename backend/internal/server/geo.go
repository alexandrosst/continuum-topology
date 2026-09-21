package server

import (
	"net/netip"
	"strings"
	"sync"

	"continuum/internal/geoip"
)

const geoCacheMax = 4096

// Geo turns an agent's connecting address into a suggested location using an offline database.
// A nil *Geo is valid and means the feature is off. Results are cached per address in memory so a
// state request never touches the file, and the cache is bounded.
type Geo struct {
	db *geoip.DB

	mu    sync.Mutex
	cache map[netip.Addr]*geoip.Result // nil value = looked up, no answer
}

// NewGeo wraps an opened database; nil disables the feature.
func NewGeo(db *geoip.DB) *Geo {
	if db == nil {
		return nil
	}
	return &Geo{db: db, cache: map[netip.Addr]*geoip.Result{}}
}

// Locate returns the suggested location of a textual address, or nil.
func (g *Geo) Locate(addr string) *geoip.Result {
	if g == nil {
		return nil
	}
	ip, err := netip.ParseAddr(addr)
	if err != nil {
		return nil
	}
	ip = ip.Unmap().WithZone("")
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.cache[ip]; ok {
		return copyResult(r)
	}
	var out *geoip.Result
	if r, ok := g.db.Lookup(ip); ok {
		out = &r
	}
	if len(g.cache) >= geoCacheMax {
		for k := range g.cache { // evict an arbitrary entry
			delete(g.cache, k)
			break
		}
	}
	g.cache[ip] = out
	return copyResult(out)
}

// copyResult hands callers their own value so nothing shared can be mutated.
func copyResult(r *geoip.Result) *geoip.Result {
	if r == nil {
		return nil
	}
	c := *r
	return &c
}

// GeoInfo is what /api/v1/info tells the UI about the feature.
type GeoInfo struct {
	Enabled     bool   `json:"enabled"`
	Database    string `json:"database,omitempty"`
	Description string `json:"description,omitempty"`
	BuiltAt     string `json:"builtAt,omitempty"`
	Attribution string `json:"attribution,omitempty"`
}

// Info describes the configured database.
func (g *Geo) Info() GeoInfo {
	if g == nil {
		return GeoInfo{}
	}
	in := g.db.Info()
	gi := GeoInfo{Enabled: true, Database: in.DatabaseType, Description: in.Description}
	if !in.BuiltAt.IsZero() {
		gi.BuiltAt = rfc(in.BuiltAt)
	}
	gi.Attribution = attributionFor(in.DatabaseType, in.Description)
	return gi
}

// attributionFor returns the credit line the database's licence asks for, or "" for an unknown source.
func attributionFor(dbType, description string) string {
	both := strings.ToLower(dbType + " " + description)
	switch {
	case strings.Contains(both, "db-ip") || strings.Contains(both, "dbip"):
		return "IP geolocation by DB-IP.com (CC BY 4.0)"
	case strings.Contains(both, "geolite2"):
		return "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com"
	}
	return ""
}
