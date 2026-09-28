package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// EbirdCache wraps EbirdClient with a monthly on-disk cache for the two
// per-hotspot calls a page view triggers (species list + taxonomy, and
// hotspot info). Same FetchedAt+TTL-in-the-JSON pattern as GBIFCache
// (gbif_cache.go) rather than encoding the date in the filename: a refresh
// overwrites the one file in place instead of leaving a trail of dated files
// that would need separate garbage collection.
const ebirdCacheTTL = 30 * 24 * time.Hour

type EbirdCache struct {
	client *EbirdClient
	dir    string
}

func NewEbirdCache(client *EbirdClient, cacheDir string) (*EbirdCache, error) {
	dir := filepath.Join(cacheDir, "ebird")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &EbirdCache{client: client, dir: dir}, nil
}

type hotspotCacheEntry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Hotspot   Hotspot   `json:"hotspot"`
}

// HotspotInfo is EbirdClient.HotspotInfo, cached to disk for ebirdCacheTTL.
// Caller must have already validated locID (see validLocID) — it's used
// verbatim in the cache file path.
func (c *EbirdCache) HotspotInfo(locID string) (*Hotspot, error) {
	path := filepath.Join(c.dir, "hotspot-"+locID+".json")
	if data, err := os.ReadFile(path); err == nil {
		var entry hotspotCacheEntry
		if json.Unmarshal(data, &entry) == nil && time.Since(entry.FetchedAt) < ebirdCacheTTL {
			return &entry.Hotspot, nil
		}
	}

	hotspot, err := c.client.HotspotInfo(locID)
	if err != nil {
		return nil, err
	}
	entry := hotspotCacheEntry{FetchedAt: time.Now(), Hotspot: *hotspot}
	if data, err := json.Marshal(entry); err == nil {
		_ = writeFileAtomic(path, data)
	}
	return hotspot, nil
}

type speciesCacheEntry struct {
	FetchedAt time.Time        `json:"fetchedAt"`
	Codes     []string         `json:"codes"`
	Taxa      map[string]Taxon `json:"taxa"`
}

// Species returns the species codes (taxonomic order) and resolved taxonomy
// for locID in the given language, cached together for ebirdCacheTTL — they
// come from two separate eBird calls but are always used together, so one
// cache entry per (locID, lang) avoids a second file and a second staleness
// check. Caller must have already validated locID and lang (see validLocID
// and validLang) — both are used verbatim in the cache file path.
func (c *EbirdCache) Species(locID, lang string) ([]string, map[string]Taxon, error) {
	path := filepath.Join(c.dir, "species-"+locID+"-"+lang+".json")
	if data, err := os.ReadFile(path); err == nil {
		var entry speciesCacheEntry
		if json.Unmarshal(data, &entry) == nil && time.Since(entry.FetchedAt) < ebirdCacheTTL {
			return entry.Codes, entry.Taxa, nil
		}
	}

	codes, err := c.client.SpeciesList(locID)
	if err != nil {
		return nil, nil, err
	}
	taxa, err := c.client.Taxonomy(codes, lang)
	if err != nil {
		return nil, nil, err
	}

	entry := speciesCacheEntry{FetchedAt: time.Now(), Codes: codes, Taxa: taxa}
	if data, err := json.Marshal(entry); err == nil {
		_ = writeFileAtomic(path, data)
	}
	return codes, taxa, nil
}

type hotspotsRegionCacheEntry struct {
	FetchedAt time.Time `json:"fetchedAt"`
	Hotspots  []Hotspot `json:"hotspots"`
}

// HotspotsInRegion is EbirdClient.HotspotsInRegion, cached to disk for
// ebirdCacheTTL. Caller must have already validated regionCode (see
// validRegionCode) — it's used verbatim in the cache file path.
func (c *EbirdCache) HotspotsInRegion(regionCode string) ([]Hotspot, error) {
	path := filepath.Join(c.dir, "hotspots-region-"+regionCode+".json")
	if data, err := os.ReadFile(path); err == nil {
		var entry hotspotsRegionCacheEntry
		if json.Unmarshal(data, &entry) == nil && time.Since(entry.FetchedAt) < ebirdCacheTTL {
			return entry.Hotspots, nil
		}
	}

	hotspots, err := c.client.HotspotsInRegion(regionCode)
	if err != nil {
		return nil, err
	}
	entry := hotspotsRegionCacheEntry{FetchedAt: time.Now(), Hotspots: hotspots}
	if data, err := json.Marshal(entry); err == nil {
		_ = writeFileAtomic(path, data)
	}
	return hotspots, nil
}
