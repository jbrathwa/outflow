// Package location maps destination IPs to a country, cloud region or CDN.
package location

import (
	"net/netip"
	"sort"
	"strings"
)

// HowKnown says how a location was determined.
type HowKnown string

const (
	CloudRange HowKnown = "cloud range"
	GeoIP      HowKnown = "GeoIP"
	AnycastCDN HowKnown = "anycast/CDN"
	None       HowKnown = "none"
)

// Location is the result of an IP lookup.
type Location struct {
	Country  string   `json:"country"`
	Provider string   `json:"provider"`
	Region   string   `json:"region"`
	Service  string   `json:"service"`
	Global   bool     `json:"global"`
	HowKnown HowKnown `json:"howKnown"`
}

// Known reports whether a concrete country is known.
func (l Location) Known() bool { return l.Country != "" && !l.Global }

var providerNames = map[string]string{"azure": "Azure", "aws": "AWS", "gcp": "GCP"}

func (l Location) String() string {
	switch {
	case l.HowKnown == AnycastCDN:
		return "CDN (" + l.Provider + ")"
	case l.Global:
		return "global (anycast)"
	case l.Provider != "":
		name := providerNames[l.Provider]
		if name == "" {
			name = l.Provider
		}
		s := name
		if l.Region != "" {
			s += " " + l.Region
		}
		if l.Country != "" {
			s += " (" + l.Country + ")"
		}
		return s
	case l.Country != "":
		return l.Country
	}
	return "unknown"
}

type cloudEntry struct {
	provider, region, service string
	global                    bool
}

type geoRange struct {
	start, end netip.Addr
	country    string
}

// DB holds the prefix tables used for lookups.
type DB struct {
	version string // set by LoadDir; Build derives it from sources
	sources []SourceInfo
	cdn     prefixTable[string]
	cloud   prefixTable[cloudEntry]
	geo     []geoRange // sorted by start
}

// prefixTable does longest-prefix matching, bucketed by prefix length.
type prefixTable[T any] struct {
	byBits map[int]map[netip.Prefix]T
	lens   []int // descending
}

func (t *prefixTable[T]) add(p netip.Prefix, v T) {
	p = p.Masked()
	if t.byBits == nil {
		t.byBits = map[int]map[netip.Prefix]T{}
	}
	m := t.byBits[p.Bits()]
	if m == nil {
		m = map[netip.Prefix]T{}
		t.byBits[p.Bits()] = m
	}
	m[p] = v
}

func (t *prefixTable[T]) finish() {
	t.lens = t.lens[:0]
	for b := range t.byBits {
		t.lens = append(t.lens, b)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(t.lens)))
}

func (t *prefixTable[T]) lookup(ip netip.Addr) (T, bool) {
	for _, b := range t.lens {
		if b > ip.BitLen() {
			continue
		}
		p, err := ip.Prefix(b)
		if err != nil {
			continue
		}
		if v, ok := t.byBits[b][p]; ok {
			return v, true
		}
	}
	var zero T
	return zero, false
}

// Version returns the data version string. When every source is a sample, or
// the database came from LoadDir, that is a single version; otherwise it lists
// each source's version.
func (db *DB) Version() string {
	if db.version != "" || len(db.sources) == 0 {
		return db.version
	}
	same := true
	parts := make([]string, len(db.sources))
	for i, s := range db.sources {
		if !s.Sample {
			same = false
		}
		parts[i] = s.Name + " " + s.Version
	}
	if same {
		return db.sources[0].Version
	}
	return strings.Join(parts, ", ")
}

// Lookup resolves ip to a Location. Unknown addresses yield HowKnown None.
func (db *DB) Lookup(ip netip.Addr) Location {
	ip = ip.Unmap()
	if !ip.IsValid() {
		return Location{HowKnown: None}
	}
	if name, ok := db.cdn.lookup(ip); ok {
		return Location{Provider: name, Global: true, HowKnown: AnycastCDN}
	}
	if c, ok := db.cloud.lookup(ip); ok {
		l := Location{Provider: c.provider, Region: c.region, Service: c.service, HowKnown: CloudRange}
		if c.global {
			l.Global = true
		} else if l.Country = RegionCountry(c.provider, c.region); l.Country == "" {
			if country, ok := db.geoCountry(ip); ok {
				l.Country, l.HowKnown = country, GeoIP
			}
		}
		return l
	}
	if country, ok := db.geoCountry(ip); ok {
		return Location{Country: country, HowKnown: GeoIP}
	}
	return Location{HowKnown: None}
}

// HasGeoIP reports whether the database holds any GeoIP country ranges.
func (db *DB) HasGeoIP() bool { return len(db.geo) > 0 }

// geoCountry finds the GeoIP country range holding ip.
func (db *DB) geoCountry(ip netip.Addr) (string, bool) {
	i := sort.Search(len(db.geo), func(i int) bool { return db.geo[i].start.Compare(ip) > 0 })
	if i > 0 {
		if g := db.geo[i-1]; g.end.Compare(ip) >= 0 {
			return g.country, true
		}
	}
	return "", false
}
