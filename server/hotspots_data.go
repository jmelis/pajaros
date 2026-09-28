package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
)

// gridDegrees buckets hotspots into ~1-degree cells so Nearby only scans
// points near the query instead of the whole worldwide set (on the order of
// 20 million points at current scale — a linear scan per request would be
// far too slow for a live map/search endpoint).
const gridDegrees = 1.0

// HotspotStore answers hotspot lookups from hotspots_index.json.gz (built by
// cmd/gensnapshot — see docs/GBIF_DATA_PIPELINE.md), loaded once at startup
// from a plain file path. Unlike the taxonomy tables, this file is large and
// rebuilt yearly, so it's not go:embed'd or committed to git — it's scp'd to
// the serving host and read from HOTSPOTS_DATA_DIR.
type HotspotStore struct {
	byID map[string]Hotspot
	grid map[gridCell][]Hotspot
}

type gridCell struct{ latCell, lngCell int }

func cellFor(lat, lng float64) gridCell {
	return gridCell{int(math.Floor(lat / gridDegrees)), int(math.Floor(lng / gridDegrees))}
}

// NewHotspotStore indexes list by ID and by spatial grid cell. Used directly
// by tests with a handful of fixture hotspots, and by loadHotspotStore with
// the full worldwide index.
func NewHotspotStore(list []Hotspot) *HotspotStore {
	byID := make(map[string]Hotspot, len(list))
	grid := make(map[gridCell][]Hotspot)
	for _, h := range list {
		byID[h.ID] = h
		cell := cellFor(h.Lat, h.Lng)
		grid[cell] = append(grid[cell], h)
	}
	return &HotspotStore{byID: byID, grid: grid}
}

// hotspotIndexEntry is the on-disk shape cmd/gensnapshot writes to
// hotspots_index.json.gz (see its hotspotOut type) — kept distinct from
// Hotspot's own JSON tags, which are chosen for the HTTP API's wire format
// (locId/locName, for frontend compatibility) rather than the file format.
// Decoding straight into Hotspot here would silently zero every field: Go's
// json.Unmarshal doesn't error on an unmatched tag, it just leaves the field
// at its zero value, which is exactly what happened before this type existed
// (every hotspot's ID decoded as "", collapsing 20M+ entries into one).
type hotspotIndexEntry struct {
	ID         string  `json:"id"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Name       string  `json:"name"`
	TotalCount int     `json:"totalCount"`
}

// loadHotspotStore decompresses and parses the hotspot index at path.
func loadHotspotStore(path string) (*HotspotStore, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open hotspot index %q: %w", path, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	var entries []hotspotIndexEntry
	if err := json.NewDecoder(gz).Decode(&entries); err != nil {
		return nil, err
	}
	list := make([]Hotspot, len(entries))
	for i, e := range entries {
		list[i] = Hotspot{ID: e.ID, Name: e.Name, Lat: e.Lat, Lng: e.Lng, TotalCount: e.TotalCount}
	}
	return NewHotspotStore(list), nil
}

// Info looks up a single hotspot by its ID ("lat,lng").
func (s *HotspotStore) Info(id string) (Hotspot, bool) {
	h, ok := s.byID[id]
	return h, ok
}

// Len returns the total number of hotspots loaded.
func (s *HotspotStore) Len() int {
	return len(s.byID)
}

// Nearby returns every hotspot within distKm of (lat, lng), most-active
// first, by scanning only the grid cells the search radius could reach
// rather than every hotspot worldwide.
func (s *HotspotStore) Nearby(lat, lng, distKm float64) []Hotspot {
	// ~111km per degree of latitude; generous enough for longitude too at
	// the latitudes this project's test hotspots sit at. +1 cell of margin
	// for points near a cell edge.
	cellRadius := int(math.Ceil(distKm/(gridDegrees*111.0))) + 1
	center := cellFor(lat, lng)

	var out []Hotspot
	for dLat := -cellRadius; dLat <= cellRadius; dLat++ {
		for dLng := -cellRadius; dLng <= cellRadius; dLng++ {
			cell := gridCell{center.latCell + dLat, center.lngCell + dLng}
			for _, h := range s.grid[cell] {
				if haversineKm(lat, lng, h.Lat, h.Lng) <= distKm {
					out = append(out, h)
				}
			}
		}
	}
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
