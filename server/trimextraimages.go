package main

import (
	"fmt"
	"log"
	"os"
	"time"
)

// runTrimExtraImages is the `go run . trimextraimages` CLI mode: for every
// cached species with more than one image, drops every image but the first
// and clears its `.topped` marker. That's the entire operation -- pure local
// disk I/O, no network calls, so it finishes in well under a second even for
// thousands of species (same shape as migrateOldCacheLayout).
//
// The point is to make already-cached species pick up a changed sourcing
// policy (see wikimedia.go/images.go) without a bespoke re-fetch tool of its
// own: clearing the topped marker hands each species straight back to the
// same lazy top-up path every other under-filled species already goes
// through -- EnsureFetchedAsync/topUpImages, live, the next time that
// species is actually viewed -- so slots 2-4 get refilled under whatever
// ResolveImages' current rules are, at the same pace and priority as normal
// traffic. Follow this with `go run . migrateimages` (or just wait for
// organic traffic) if you want them refilled right away instead.
//
// The first image is never touched or re-validated: ensureFirstImage always
// resolves it via the same ResolveImages(sciName, maxImageWidth, 1) call a
// species' first-ever fetch used, which tries the Wikipedia infobox photo
// before anything else -- so it's already the best single pick available,
// in both the old and new policy, for every species that has one.
func runTrimExtraImages() error {
	cacheDir := os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "./cache"
	}
	cache, err := NewImageCache(cacheDir)
	if err != nil {
		return fmt.Errorf("cache dir %q: %w", cacheDir, err)
	}

	taxonomy, err := loadTaxonomyStore()
	if err != nil {
		return fmt.Errorf("taxonomy: %w", err)
	}

	start := time.Now()
	total, trimmed := 0, 0
	for sciName := range taxonomy.codeBySciName {
		images := cache.readMetadata(sciName)
		if len(images) == 0 {
			continue
		}
		total++
		if len(images) == 1 {
			continue
		}
		for i := 1; i < len(images); i++ {
			if err := os.Remove(cache.imagePath(sciName, i)); err != nil {
				log.Printf("trimextraimages: %s: remove slot %d: %v", sciName, i, err)
			}
		}
		if err := cache.writeMetadata(sciName, images[:1]); err != nil {
			log.Printf("trimextraimages: %s: write metadata: %v", sciName, err)
			continue
		}
		if err := os.Remove(cache.toppedMarkerPath(sciName)); err != nil && !os.IsNotExist(err) {
			log.Printf("trimextraimages: %s: remove topped marker: %v", sciName, err)
		}
		trimmed++
	}
	log.Printf("trimextraimages: done — %d/%d cached species trimmed to their first image, in %s",
		trimmed, total, time.Since(start).Round(time.Millisecond))
	return nil
}
