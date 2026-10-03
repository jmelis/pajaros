package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jmelis/pajaros/server/internal/areas"
	"github.com/jmelis/pajaros/server/internal/placekey"

	bolt "go.etcd.io/bbolt"
)

// Bucket names in places.bolt (built by cmd/gensnapshot's `places`
// subcommand from OSM and Natural Earth — see ARCHITECTURE.md).
const (
	placeByNameBucket = "place_by_name"
	placeByIDBucket   = "place_by_id"
	placeShapesBucket = "place_shapes"
	placeByCellBucket = "place_by_cell"
	placeMetaBucket   = "place_meta"
	placeCountMetaKey = "count"
)

// placeTypeNames are the values of a place's type byte; must match
// cmd/gensnapshot/places.go's placeTypes. The first is the default.
var placeTypeNames = [...]string{"city", "country", "region", "natural", "park", "reserve", "island", "desert", "town", "village", "municipality", "district"}

// Place is one place-search result: a city, country, region or natural area
// whose seasonal species can be listed. Kind is its type (city, town, park,
// island, region...), which tells apart places that share a name. Country is the containing country's
// name, and is empty for places that are countries themselves or span several
// (the Sahara), which the UI shows by name alone. ID names the place's
// polygon (see PlaceStore.Shape); RadiusKm is the default circle for places
// with only a centre.
type Place struct {
	ID          uint64  `json:"id"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Country     string  `json:"country,omitempty"`
	CountryCode string  `json:"countryCode,omitempty"`
	Lat         float64 `json:"lat"`
	Lng         float64 `json:"lng"`
	RadiusKm    float64 `json:"radiusKm"`
	Population  int     `json:"population"`
}

// PlaceStore answers place-name search from places.bolt — a small (tens of
// MB) sibling of seasonal_cells.bolt, opened as its own mmap-backed handle.
type PlaceStore struct {
	db    *bolt.DB
	count int
}

func openPlaceStore(db *bolt.DB) (*PlaceStore, error) {
	var count int
	err := db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{placeByNameBucket, placeByIDBucket, placeShapesBucket, placeByCellBucket, placeMetaBucket} {
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
// lookup would need a full scan of seasonal_cells.bolt instead of this one scan
// of the much smaller places.bolt. A full bucket scan (not a lookup bbolt
// is optimized for), but cmd/gensnapshot's places subcommand builds under
// a quarter-million rows, so it's cheap as a one-off offline pass.
func (s *PlaceStore) TopPlacePerCountry() map[string]Place {
	best := map[string]Place{}
	s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(placeByNameBucket)).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			p := decodePlace(k, v)
			if p.Kind != "city" || p.CountryCode == "" {
				continue
			}
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
	prefix := []byte(placekey.Normalize(query))
	if len(prefix) < 2 {
		return nil
	}
	var candidates []Place
	seen := map[uint64]bool{}
	s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(placeByNameBucket)).Cursor()
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix) && len(candidates) < maxPlaceScan; k, v = c.Next() {
			// An area place is indexed under several names that can all
			// share the prefix; report it once.
			p := decodePlace(k, v)
			if !seen[p.ID] {
				seen[p.ID] = true
				candidates = append(candidates, p)
			}
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
// a 2-byte country code (blank when none), a kind byte, the default radius
// in 100 m units, the containing country's name (length byte + bytes), then
// the display name to the end of the buffer — and takes the place ID from
// the last 8 bytes of the key. Must exactly match
// cmd/gensnapshot/places.go's encodePlaceValue and placeKey.
func decodePlace(key, buf []byte) Place {
	lat := math.Float64frombits(binary.LittleEndian.Uint64(buf[0:8]))
	lng := math.Float64frombits(binary.LittleEndian.Uint64(buf[8:16]))
	pop, m := binary.Uvarint(buf[16:])
	i := 16 + m
	cc := strings.TrimSpace(string(buf[i : i+2]))
	kind := int(buf[i+2])
	i += 3
	r, m := binary.Uvarint(buf[i:])
	i += m
	n := int(buf[i])
	country := string(buf[i+1 : i+1+n])
	name := string(buf[i+1+n:])
	if kind >= len(placeTypeNames) {
		kind = 0
	}
	return Place{
		ID:   binary.BigEndian.Uint64(key[len(key)-8:]),
		Name: name, Kind: placeTypeNames[kind], Country: country, CountryCode: cc,
		Lat: lat, Lng: lng, RadiusKm: float64(r) / 10, Population: int(pop),
	}
}

// ByID returns the place with the given ID.
func (s *PlaceStore) ByID(id uint64) (Place, bool) {
	var p Place
	found := false
	s.db.View(func(tx *bolt.Tx) error {
		p, found = placeByID(tx, id)
		return nil
	})
	return p, found
}

func placeByID(tx *bolt.Tx, id uint64) (Place, bool) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], id)
	v := tx.Bucket([]byte(placeByIDBucket)).Get(key[:])
	if v == nil {
		return Place{}, false
	}
	return decodePlace(key[:], v), true
}

// Shape returns the polygon of an area place (a country, region or natural
// feature), or false when the place has none or the ID is unknown.
func (s *PlaceStore) Shape(id uint64) (*areas.Shape, bool) {
	var sh *areas.Shape
	s.db.View(func(tx *bolt.Tx) error {
		sh = placeShape(tx, id)
		return nil
	})
	return sh, sh != nil
}

// Rings returns the raw polygon rings, (lng, lat) pairs, of an area place.
func (s *PlaceStore) Rings(id uint64) ([][][2]float64, bool) {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], id)
	var rings [][][2]float64
	s.db.View(func(tx *bolt.Tx) error {
		if v := tx.Bucket([]byte(placeShapesBucket)).Get(key[:]); v != nil {
			rings = decodeShape(v)
		}
		return nil
	})
	return rings, len(rings) > 0
}

func placeShape(tx *bolt.Tx, id uint64) *areas.Shape {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], id)
	v := tx.Bucket([]byte(placeShapesBucket)).Get(key[:])
	if v == nil {
		return nil
	}
	rings := decodeShape(v)
	if len(rings) == 0 {
		return nil
	}
	return areas.NewShape(rings)
}

// place_by_cell classes; must match cmd/gensnapshot/places.go.
const (
	cellsPerDeg           = 2
	cellClassLocality     = 0
	cellClassCountry      = 1
	cellClassMunicipality = 2
	cellClassRegionBase   = 8
	// maxNearKm is how far the nearest locality may be and still name a point
	// that is inside no municipality.
	maxNearKm = 60
)

// regionLevelRank orders the OSM admin levels a region may have by how well
// they name where a point is: level 4 (state, province, autonomous
// community) first.
var regionLevelRank = map[int]int{4: 0, 5: 1, 6: 2, 7: 3, 3: 4}

// Locality describes where a point is: the municipality containing it (else
// the nearest town or village), the state or province containing it, and its
// country. Any part may be empty (open sea, desert).
type Locality struct {
	Near        string
	Region      string
	Country     string
	CountryCode string
}

func cellRowCol(lat, lng float64) (int, int) {
	i := int(math.Floor((lat + 90) * cellsPerDeg))
	j := int(math.Floor((lng + 180) * cellsPerDeg))
	return min(max(i, 0), 180*cellsPerDeg-1), min(max(j, 0), 360*cellsPerDeg-1)
}

func cellEntries(c *bolt.Bucket, i, j int, visit func(id uint64, class int)) {
	if i < 0 || i >= 180*cellsPerDeg {
		return
	}
	j = (j + 360*cellsPerDeg) % (360 * cellsPerDeg)
	var key [4]byte
	binary.BigEndian.PutUint32(key[:], uint32(i)<<16|uint32(j))
	buf := c.Get(key[:])
	for len(buf) > 0 {
		v, n := binary.Uvarint(buf)
		if n <= 0 {
			return
		}
		buf = buf[n:]
		visit(v>>4, int(v&15))
	}
}

// Locate names the point from places.bolt's cell index: the municipality
// polygon containing it (else the closest locality within maxNearKm among the
// surrounding cells), the broadest region polygon containing it and the
// country polygon containing it.
func (s *PlaceStore) Locate(lat, lng float64) Locality {
	defer bboltTimer("places", "locate")()
	var loc Locality
	s.db.View(func(tx *bolt.Tx) error {
		cells := tx.Bucket([]byte(placeByCellBucket))
		ci, cj := cellRowCol(lat, lng)
		var near Place
		nearKm := math.MaxFloat64
		for di := -1; di <= 1; di++ {
			for dj := -1; dj <= 1; dj++ {
				cellEntries(cells, ci+di, cj+dj, func(id uint64, class int) {
					if class != cellClassLocality {
						return
					}
					if p, ok := placeByID(tx, id); ok {
						if d := haversineKm(lat, lng, p.Lat, p.Lng); d < nearKm {
							near, nearKm = p, d
						}
					}
				})
			}
		}
		var muni, region, country Place
		var haveMuni, haveRegion, haveCountry bool
		regionRank := 0
		cellEntries(cells, ci, cj, func(id uint64, class int) {
			if class == cellClassLocality {
				return
			}
			rank := 0
			switch {
			case class >= cellClassRegionBase:
				r, ok := regionLevelRank[class-cellClassRegionBase]
				if !ok {
					return
				}
				rank = r
			case class == cellClassMunicipality && haveMuni, class == cellClassCountry && haveCountry:
				return
			}
			p, ok := placeByID(tx, id)
			if !ok {
				return
			}
			if class >= cellClassRegionBase && haveRegion && (rank > regionRank || (rank == regionRank && p.RadiusKm >= region.RadiusKm)) {
				return
			}
			if sh := placeShape(tx, id); sh == nil || !sh.Contains(lat, lng) {
				return
			}
			switch {
			case class == cellClassMunicipality:
				muni, haveMuni = p, true
			case class == cellClassCountry:
				country, haveCountry = p, true
			default:
				region, haveRegion, regionRank = p, true, rank
			}
		})
		switch {
		case haveMuni:
			loc.Near, loc.Country, loc.CountryCode = muni.Name, muni.Country, muni.CountryCode
		case nearKm <= maxNearKm:
			loc.Near, loc.Country, loc.CountryCode = near.Name, near.Country, near.CountryCode
		}
		if haveRegion {
			loc.Region = region.Name
			loc.Country, loc.CountryCode = region.Country, region.CountryCode
		}
		if haveCountry {
			loc.Country = country.Name
		}
		return nil
	})
	return loc
}

// decodeShape reads place_shapes' value format: uvarint ring count, then per
// ring a uvarint vertex count and float32 (lng, lat) pairs. Must match
// cmd/gensnapshot/naturalearth.go's encodeShape.
func decodeShape(buf []byte) [][][2]float64 {
	nRings, m := binary.Uvarint(buf)
	buf = buf[m:]
	rings := make([][][2]float64, 0, nRings)
	for range nRings {
		n, m := binary.Uvarint(buf)
		buf = buf[m:]
		r := make([][2]float64, n)
		for i := range r {
			r[i][0] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[0:4])))
			r[i][1] = float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[4:8])))
			buf = buf[8:]
		}
		rings = append(rings, r)
	}
	return rings
}
