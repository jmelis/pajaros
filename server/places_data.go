package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Bucket names in places.bolt (built by cmd/gensnapshot's `places`
// subcommand from a GeoNames gazetteer dump — see ARCHITECTURE.md).
const (
	placeByNameBucket = "place_by_name"
	placeMetaBucket   = "place_meta"
	placeCountMetaKey = "count"
)

// Place is one place-search result — a town/city to center a hotspot search
// on, not a hotspot itself.
type Place struct {
	Name        string  `json:"name"`
	CountryCode string  `json:"countryCode"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	Population  int     `json:"population"`
}

// PlaceStore answers place-name search from places.bolt — a small (tens of
// MB) sibling of hotspots.bolt, opened as its own mmap-backed handle.
type PlaceStore struct {
	db    *bolt.DB
	count int
}

func openPlaceStore(db *bolt.DB) (*PlaceStore, error) {
	var count int
	err := db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{placeByNameBucket, placeMetaBucket} {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("bucket %q not found", name)
			}
		}
		v := tx.Bucket([]byte(placeMetaBucket)).Get([]byte(placeCountMetaKey))
		if v == nil {
			return fmt.Errorf("key %q not found in %q", placeCountMetaKey, placeMetaBucket)
		}
		n, _ := binary.Uvarint(v)
		count = int(n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &PlaceStore{db: db, count: count}, nil
}

// Len returns the total number of places in the store.
func (s *PlaceStore) Len() int { return s.count }

// TopPlacePerCountry returns, for every country code seen in the store, its
// most populous place — used to anchor "this country's best-known
// location" for things like cache warming, where an exact busiest-point
// lookup would need a full scan of hotspots.bolt instead of this one scan
// of the much smaller places.bolt. A full bucket scan (not a lookup bbolt
// is optimized for), but cmd/gensnapshot's places subcommand builds under
// a quarter-million rows, so it's cheap as a one-off offline pass.
func (s *PlaceStore) TopPlacePerCountry() map[string]Place {
	best := map[string]Place{}
	s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(placeByNameBucket)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			p := decodePlace(v)
			if cur, ok := best[p.CountryCode]; !ok || p.Population > cur.Population {
				best[p.CountryCode] = p
			}
		}
		return nil
	})
	return best
}

// maxPlaceScan bounds how many name-sorted candidates Search walks past a
// query's prefix before stopping, so a very common prefix (e.g. "san")
// can't make one request scan without limit. The tradeoff: an extremely
// common prefix may miss a low-population match that would have ranked in
// the top results had scanning gone further — acceptable for a search box,
// not for anything that needs to be exhaustive.
const maxPlaceScan = 3000

// Search returns up to limit places whose name starts with query,
// most-populous first. Matching is a prefix scan over place_by_name (keys
// are sorted by normalized name, so every match sits in one contiguous
// run) — not a substring or fuzzy search.
func (s *PlaceStore) Search(query string, limit int) []Place {
	defer bboltTimer("places", "search")()
	prefix := []byte(normalizePlaceName(query))
	if len(prefix) < 2 {
		return nil
	}
	var candidates []Place
	s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(placeByNameBucket)).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && len(candidates) < maxPlaceScan; k, v = c.Next() {
			candidates = append(candidates, decodePlace(v))
		}
		return nil
	})
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Population > candidates[j].Population })
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

// decodePlace reads place_by_name's value format — lat, lng, population,
// a fixed 2-byte country code, then the display name to the end of the
// buffer. Must exactly match cmd/gensnapshot/places.go's encodePlaceValue.
func decodePlace(buf []byte) Place {
	lat := math.Float64frombits(binary.LittleEndian.Uint64(buf[0:8]))
	lng := math.Float64frombits(binary.LittleEndian.Uint64(buf[8:16]))
	pop, m := binary.Uvarint(buf[16:])
	i := 16 + m
	cc := string(buf[i : i+2])
	name := string(buf[i+2:])
	return Place{Name: name, CountryCode: cc, Lat: lat, Lng: lng, Population: int(pop)}
}

// normalizePlaceName is the query-time and build-time name normalization —
// lowercased, trimmed. Must exactly match
// cmd/gensnapshot/places.go's normalizePlaceName.
func normalizePlaceName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
