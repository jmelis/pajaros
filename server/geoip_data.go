package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"net"
	"sort"
)

// GeoIPStore answers "which of the app's non-English UI languages is spoken
// in the country an IP address geolocates to", from an embedded, offline
// -built table — never a live lookup, matching this project's rule that
// Wikimedia is the only upstream call made at request time (see
// ARCHITECTURE.md). Used once, at a brand-new account's first login, to
// pick its default language (see completeLogin in auth.go); an account can
// always change it afterward in settings.
//
// The table is built by `gensnapshot geoip` from db-ip.com's free "IP to
// Country Lite" database, filtered down to just the countries in
// cmd/gensnapshot/geoip.go's spanishCountries/frenchCountries — every other
// country (most of them) needs no entry, since a lookup that finds nothing
// means English.
//
//go:embed data/geoip_country.json.gz
var embeddedGeoIPCountry []byte

// geoIPRangeIn is the on-disk JSON shape of data/geoip_country.json.gz —
// see cmd/gensnapshot/geoip.go, which produces it.
type geoIPRangeIn struct {
	Start string `json:"s"`
	End   string `json:"e"`
	Lang  string `json:"l"`
}

// geoIPRange is one loaded range, endpoints in net.IP.To16() form so IPv4
// and IPv6 ranges compare and sort with the same byte-slice comparison —
// an IPv4 address's 16-byte form always sorts before every real IPv6
// address, so the two families never collide despite sharing one table.
type geoIPRange struct {
	start, end [16]byte
	lang       string
}

// GeoIPStore looks up geoIPRanges, sorted by start, with binary search.
type GeoIPStore struct {
	ranges []geoIPRange
}

// loadGeoIPStore decodes the embedded table. Called once at startup.
func loadGeoIPStore() (*GeoIPStore, error) {
	gz, err := gzip.NewReader(bytes.NewReader(embeddedGeoIPCountry))
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var in []geoIPRangeIn
	if err := json.NewDecoder(gz).Decode(&in); err != nil {
		return nil, err
	}

	ranges := make([]geoIPRange, 0, len(in))
	for _, r := range in {
		start := net.ParseIP(r.Start)
		end := net.ParseIP(r.End)
		if start == nil || end == nil {
			return nil, fmt.Errorf("geoip: invalid range %q-%q", r.Start, r.End)
		}
		var gr geoIPRange
		copy(gr.start[:], start.To16())
		copy(gr.end[:], end.To16())
		gr.lang = r.Lang
		ranges = append(ranges, gr)
	}
	sort.Slice(ranges, func(i, j int) bool {
		return bytes.Compare(ranges[i].start[:], ranges[j].start[:]) < 0
	})
	return &GeoIPStore{ranges: ranges}, nil
}

// LangForIP returns the language ("es" or "fr") recorded for ip's
// geolocated country, and false when ip isn't covered by any recorded
// range — which includes every English-speaking country and every country
// the table doesn't bother recording. A nil receiver (a store that failed
// to load, or wasn't wired up, e.g. in tests) always reports not-found
// rather than panicking, so callers can treat it as an optional lookup.
func (g *GeoIPStore) LangForIP(ip net.IP) (string, bool) {
	if g == nil || ip == nil {
		return "", false
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return "", false
	}

	// Last range whose start is <= ip16.
	i := sort.Search(len(g.ranges), func(i int) bool {
		return bytes.Compare(g.ranges[i].start[:], ip16) > 0
	}) - 1
	if i < 0 {
		return "", false
	}
	r := g.ranges[i]
	if bytes.Compare(ip16, r.end[:]) > 0 {
		return "", false
	}
	return r.lang, true
}
