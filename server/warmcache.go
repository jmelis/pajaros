package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// runWarmCache pre-populates the image cache with a high-value subset of
// species, rather than the full ~11K-species taxonomy: for each country
// (from places.bolt), the busiest hotspot within maxNearbyDistKm of that
// country's most populous place, and every species recorded there. That's
// an approximation of "the busiest hotspot in the country" — the true
// answer would need a full scan of hotspots.bolt reverse-geocoded against
// places.bolt, which isn't worth the cost here — but it's a close one:
// top birding sites are rarely far from a country's biggest population
// centers.
//
// This is a separate mode of the server binary (not cmd/gensnapshot)
// because ImageCache, the taxonomy loader and the Wikimedia rate limiter
// all already live in this package, wired up exactly as main() needs them —
// see `go run . warmcache`'s dispatch there.
//
// Run this offline against a local CACHE_DIR, same as gensnapshot's data
// files, then rsync the resulting cache/ directory up to the deployed
// server's hostPath (see README.md) — it never needs to run inside the
// cluster.
func runWarmCache() error {
	// WIKIMEDIA_RPS's default (8) favors live per-hotspot lookups (see
	// ratelimit.go) -- wrong pace for a one-off bulk job hitting thousands
	// of species in a row. warmcache gets its own, deliberately polite
	// default instead, independent of whatever WIKIMEDIA_RPS is set to for
	// live traffic (this runs as its own process, so reassigning here never
	// touches the actual serving process's limiter).
	wikimediaLimiter = newRateLimiter(rpsFromEnv("WARMCACHE_RPS", 1))
	log.Printf("warmcache: rate limit %.1f req/s (override with WARMCACHE_RPS)", rpsFromEnv("WARMCACHE_RPS", 1))

	dataDir := os.Getenv("HOTSPOTS_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	cacheDir := os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "./cache"
	}

	hotspotsDB, err := bolt.Open(filepath.Join(dataDir, "hotspots.bolt"), 0o444, &bolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open hotspots.bolt: %w", err)
	}
	defer hotspotsDB.Close()
	hotspots, err := openHotspotStore(hotspotsDB)
	if err != nil {
		return fmt.Errorf("hotspot store: %w", err)
	}

	placesDB, err := bolt.Open(filepath.Join(dataDir, "places.bolt"), 0o444, &bolt.Options{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("open places.bolt: %w", err)
	}
	defer placesDB.Close()
	places, err := openPlaceStore(placesDB)
	if err != nil {
		return fmt.Errorf("place store: %w", err)
	}

	taxonomy, err := loadTaxonomyStore()
	if err != nil {
		return fmt.Errorf("taxonomy: %w", err)
	}
	species := NewSpeciesResolver(taxonomy, openSpeciesStore(hotspotsDB, taxonomy))

	cache, err := NewImageCache(cacheDir)
	if err != nil {
		return fmt.Errorf("cache dir %q: %w", cacheDir, err)
	}

	topPlaces := places.TopPlacePerCountry()
	log.Printf("warmcache: %d countries in places.bolt", len(topPlaces))

	type nameInfo struct{ comName string }
	toWarm := map[string]nameInfo{} // sciName -> its English common name

	hit := 0
	for cc, place := range topPlaces {
		nearby := hotspots.Nearby(place.Lat, place.Lng, maxNearbyDistKm)
		if len(nearby) == 0 {
			continue
		}
		top := nearby[0] // Nearby sorts most-active first
		codes, taxa, err := species.Species(top.ID, defaultLang)
		if err != nil {
			log.Printf("warmcache: %s: species at %q: %v", cc, top.Name, err)
			continue
		}
		hit++
		for _, code := range codes {
			t := taxa[code]
			if t.SciName == "" {
				continue
			}
			toWarm[t.SciName] = nameInfo{comName: t.ComName}
		}
	}
	log.Printf("warmcache: %d/%d countries had a hotspot within %.0fkm; %d distinct species to warm",
		hit, len(topPlaces), maxNearbyDistKm, len(toWarm))

	start := time.Now()
	i := 0
	for sciName, info := range toWarm {
		i++
		if err := cache.EnsureFetched(sciName, defaultLang, info.comName); err != nil {
			log.Printf("warmcache: EnsureFetched(%s): %v", sciName, err)
		}
		if i%50 == 0 || i == len(toWarm) {
			log.Printf("warmcache: %d/%d species (%s elapsed)", i, len(toWarm), time.Since(start).Round(time.Second))
		}
	}
	log.Printf("warmcache: done — %d species in %s", len(toWarm), time.Since(start).Round(time.Second))
	return nil
}
