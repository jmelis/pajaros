package main

import (
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// gridDegrees buckets hotspots into ~1-degree cells so Nearby only fetches
// cells near the query instead of scanning the whole worldwide set (on the
// order of 20 million points at current scale). Must match
// cmd/gensnapshot/hotspots.go's constant of the same name exactly — it
// determines the bbolt key each hotspot's grid-cell entry was written
// under.
const gridDegrees = 1.0

// Bucket names in hotspots.bolt (built by cmd/gensnapshot — see
// ARCHITECTURE.md). hotspot_by_id and hotspot_by_grid_cell hold
// the metadata (lat, lng, name, totalCount) this file serves; species data
// lives in species_store.go's own sibling bucket.
const (
	hotspotByIDBucket   = "hotspot_by_id"
	hotspotByCellBucket = "hotspot_by_grid_cell"
	hotspotMetaBucket   = "hotspot_meta"
	hotspotCountMetaKey = "count"
)

// HotspotStore answers hotspot lookups from hotspots.bolt, the same
// mmap-backed bbolt file SpeciesStore reads from (species_store.go) —
// nothing is loaded into memory at startup. Info is a single Get against
// hotspot_by_id; Nearby does one Get per candidate grid cell against
// hotspot_by_grid_cell. Len() reads a precomputed count cmd/gensnapshot
// wrote to hotspot_meta, so it doesn't need a full bucket scan either.
type HotspotStore struct {
	db    *bolt.DB
	count int
}

// openHotspotStore opens db's hotspot buckets — db is already open (see
// main.go, which shares one *bolt.DB handle between this and
// openSpeciesStore) — and fails fast if any expected bucket is missing,
// so Info/Nearby never have to handle that case per call.
func openHotspotStore(db *bolt.DB) (*HotspotStore, error) {
	var count int
	err := db.View(func(tx *bolt.Tx) error {
		for _, name := range []string{hotspotByIDBucket, hotspotByCellBucket, hotspotMetaBucket} {
			if tx.Bucket([]byte(name)) == nil {
				return fmt.Errorf("bucket %q not found", name)
			}
		}
		b := tx.Bucket([]byte(hotspotMetaBucket))
		v := b.Get([]byte(hotspotCountMetaKey))
		if v == nil {
			return fmt.Errorf("key %q not found in %q", hotspotCountMetaKey, hotspotMetaBucket)
		}
		n, _ := binary.Uvarint(v)
		count = int(n)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &HotspotStore{db: db, count: count}, nil
}

type gridCell struct{ latCell, lngCell int }

func cellFor(lat, lng float64) gridCell {
	return gridCell{int(math.Floor(lat / gridDegrees)), int(math.Floor(lng / gridDegrees))}
}

// cellKeyOffset shifts cell coordinates into an always-non-negative range
// before big-endian encoding, so byte-order comparison of encoded keys
// matches numeric cell order (comfortably larger than any real cell
// coordinate: |latCell| <= 90, |lngCell| <= 180). Must match
// cmd/gensnapshot/hotspots.go's cellKey exactly, or these Gets simply won't
// find what the build wrote for a given cell.
const cellKeyOffset = 1 << 20

func cellKey(c gridCell) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf[0:4], uint32(c.latCell+cellKeyOffset))
	binary.BigEndian.PutUint32(buf[4:8], uint32(c.lngCell+cellKeyOffset))
	return buf
}

// decodeHotspotByID reads the hotspot_by_id bucket's value format — lat,
// lng, totalCount, name, with no ID (it's already the bucket key). Must
// exactly match cmd/gensnapshot/hotspots.go's encodeHotspotByIDValue.
func decodeHotspotByID(id string, buf []byte) Hotspot {
	lat := math.Float64frombits(binary.LittleEndian.Uint64(buf[0:8]))
	lng := math.Float64frombits(binary.LittleEndian.Uint64(buf[8:16]))
	total, m := binary.Uvarint(buf[16:])
	name := string(buf[16+m:])
	return Hotspot{ID: id, Lat: lat, Lng: lng, Name: name, TotalCount: int(total)}
}

// decodeHotspotEntry reads one {id, lat, lng, name, totalCount} entry from
// the front of buf, as packed back-to-back by
// cmd/gensnapshot/hotspots.go's encodeHotspotEntry, and returns how many
// bytes it consumed so callers can decode a hotspot_by_grid_cell value's
// entries in a loop.
func decodeHotspotEntry(buf []byte) (Hotspot, int) {
	i := 0
	idLen, m := binary.Uvarint(buf[i:])
	i += m
	id := string(buf[i : i+int(idLen)])
	i += int(idLen)
	lat := math.Float64frombits(binary.LittleEndian.Uint64(buf[i:]))
	i += 8
	lng := math.Float64frombits(binary.LittleEndian.Uint64(buf[i:]))
	i += 8
	total, m := binary.Uvarint(buf[i:])
	i += m
	nameLen, m := binary.Uvarint(buf[i:])
	i += m
	name := string(buf[i : i+int(nameLen)])
	i += int(nameLen)
	return Hotspot{ID: id, Lat: lat, Lng: lng, Name: name, TotalCount: int(total)}, i
}

// Info looks up a single hotspot by its ID ("lat,lng").
func (s *HotspotStore) Info(id string) (Hotspot, bool) {
	var h Hotspot
	var ok bool
	s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket([]byte(hotspotByIDBucket)).Get([]byte(id))
		if v == nil {
			return nil
		}
		h, ok = decodeHotspotByID(id, v), true
		return nil
	})
	return h, ok
}

// Len returns the total number of hotspots in the store.
func (s *HotspotStore) Len() int {
	return s.count
}

// Nearby returns every hotspot within distKm of (lat, lng), most-active
// first, by fetching only the grid cells the search radius could reach —
// one bbolt Get per candidate cell — rather than scanning every hotspot
// worldwide.
func (s *HotspotStore) Nearby(lat, lng, distKm float64) []Hotspot {
	// ~111km per degree of latitude; generous enough for longitude too at
	// the latitudes this project's test hotspots sit at. +1 cell of margin
	// for points near a cell edge.
	cellRadius := int(math.Ceil(distKm/(gridDegrees*111.0))) + 1
	center := cellFor(lat, lng)

	var out []Hotspot
	s.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(hotspotByCellBucket))
		for dLat := -cellRadius; dLat <= cellRadius; dLat++ {
			for dLng := -cellRadius; dLng <= cellRadius; dLng++ {
				cell := gridCell{center.latCell + dLat, center.lngCell + dLng}
				v := b.Get(cellKey(cell))
				for i := 0; i < len(v); {
					h, n := decodeHotspotEntry(v[i:])
					if haversineKm(lat, lng, h.Lat, h.Lng) <= distKm {
						out = append(out, h)
					}
					i += n
				}
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].TotalCount > out[j].TotalCount
	})
	return out
}

// earthRadiusKm is the mean radius used for the haversine distance below —
// plenty accurate for "hotspots within N km", not a geodesy tool.
const earthRadiusKm = 6371.0

// haversineKm returns the great-circle distance between two lat/lng points in
// kilometers.
func haversineKm(lat1, lng1, lat2, lng2 float64) float64 {
	rad := math.Pi / 180
	dLat := (lat2 - lat1) * rad
	dLng := (lng2 - lng1) * rad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(a))
}
