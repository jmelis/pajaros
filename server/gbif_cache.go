package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// gbifGridDegrees snaps nearby hotspots onto a shared cache cell — a ~10km
// box computed for one hotspot is a reasonable stand-in for any other
// hotspot in roughly the same 0.1°-square area, so they share one GBIF
// fetch instead of each paying for their own. Approximate by design (see
// gbif.go's boundingBox comment) — not a precise equal-area grid.
const gbifGridDegrees = 0.1

const gbifCacheTTL = 30 * 24 * time.Hour

type GBIFCache struct {
	dir string
}

func NewGBIFCache(cacheDir string) (*GBIFCache, error) {
	dir := filepath.Join(cacheDir, "gbif")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &GBIFCache{dir: dir}, nil
}

type gbifCacheEntry struct {
	FetchedAt time.Time      `json:"fetchedAt"`
	Counts    map[string]int `json:"counts"`
}

func (c *GBIFCache) path(lat, lng float64) string {
	latCell := math.Floor(lat/gbifGridDegrees) * gbifGridDegrees
	lngCell := math.Floor(lng/gbifGridDegrees) * gbifGridDegrees
	return filepath.Join(c.dir, fmt.Sprintf("%.1f_%.1f.json", latCell, lngCell))
}

// PopularityCounts returns nearby occurrence counts for (lat, lng),
// transparently caching on disk for gbifCacheTTL so repeated lookups near
// the same area don't re-hit GBIF.
func (c *GBIFCache) PopularityCounts(lat, lng float64) (map[string]int, error) {
	path := c.path(lat, lng)

	if data, err := os.ReadFile(path); err == nil {
		var entry gbifCacheEntry
		if json.Unmarshal(data, &entry) == nil && time.Since(entry.FetchedAt) < gbifCacheTTL {
			return entry.Counts, nil
		}
	}

	counts, err := PopularityCounts(lat, lng)
	if err != nil {
		return nil, err
	}

	entry := gbifCacheEntry{FetchedAt: time.Now(), Counts: counts}
	if data, err := json.Marshal(entry); err == nil {
		_ = writeFileAtomic(path, data)
	}
	return counts, nil
}
