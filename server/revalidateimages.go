package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// revalidateSpeciesImages brings sciName's already-cached images in line
// with the current sourcing policy (see images.go/wikimedia.go): Wikimedia
// may contribute at most the current Wikipedia infobox photo, never a
// Commons-category-walked one. The image cache is fetch-once (see
// cache.go's topUpImages), so a species fully resolved under the old
// category-walk behavior keeps whatever it originally got forever unless
// something actively re-checks it — this is that re-check, meant to be run
// as a one-off offline pass (see runRevalidateImages) rather than on every
// request.
//
// Existing iNaturalist images are always kept as-is: inaturalistImages
// already ties every photo to a specific research-grade,
// community-identified observation of the named taxon, a guarantee a
// Commons category tag never had, so they're not in scope for this cleanup.
//
// Returns changed=true if anything was actually replaced or dropped, so
// callers can report how much a pass found without diffing the cache
// themselves.
func revalidateSpeciesImages(cache *ImageCache, sciName string) (changed bool, err error) {
	existing := cache.readMetadata(sciName)
	if len(existing) == 0 {
		return false, nil
	}

	var kept []*ImageInfo
	for _, info := range existing {
		if !strings.Contains(info.SourceURL, "commons.wikimedia.org") {
			kept = append(kept, info) // iNaturalist -- not in scope, keep as-is
		}
		// Any Commons entry is dropped here regardless of whether it still
		// passes commonsFileInfo's checks -- commonsImages below re-adds it
		// if (and only if) it's still the current infobox pick.
	}
	freshCommons := commonsImages(sciName, maxImageWidth, maxImagesPerSpecies)
	final := mergeImageSources(freshCommons, kept)
	if len(final) < maxImagesPerSpecies {
		final = mergeImageSources(final, inaturalistImages(sciName, maxImagesPerSpecies-len(final)))
	}

	if sameSourceURLs(existing, final) {
		return false, nil
	}

	// Slots are positional (image-N.jpg), so dropping or reordering one
	// means every slot has to be rewritten rather than just the changed
	// ones -- simplest correct way to reshuffle is to clear them all and
	// re-save final's images from scratch, even the ones that were already
	// fine.
	for i := range existing {
		os.Remove(cache.imagePath(sciName, i))
	}
	var saved []*ImageInfo
	for _, info := range final {
		if len(saved) >= maxImagesPerSpecies {
			break
		}
		if err := cache.downloadAndSave(sciName, info, len(saved)); err != nil {
			log.Printf("revalidate %s: download %s: %v", sciName, info.SourceURL, err)
			continue
		}
		saved = append(saved, info)
	}
	if err := cache.writeMetadata(sciName, saved); err != nil {
		return true, err
	}
	return true, nil
}

// sameSourceURLs reports whether a and b are the same images in the same
// slot order, so a revalidation pass that would end up writing back exactly
// what's already cached can skip the rewrite (and, for the caller, not be
// counted as something actually fixed).
func sameSourceURLs(a, b []*ImageInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SourceURL != b[i].SourceURL {
			return false
		}
	}
	return true
}

// runRevalidateImages is the `go run . revalidateimages` CLI mode: walks
// every species in the embedded taxonomy that already has cached images and
// re-checks them with revalidateSpeciesImages, replacing or dropping any
// Commons image that isn't the species' current Wikipedia infobox photo.
// Meant to be run once after a sourcing policy or filter change (like
// wikimedia.go dropping its Commons-category walk, or excludeCategoryRe's
// junk filter) to bring whatever was already cached under the old rules in
// line -- ordinary fetches (EnsureFetchedAsync et al) never re-check a
// species once it's fully resolved.
func runRevalidateImages() error {
	rps := rpsFromEnv("REVALIDATEIMAGES_RPS", 1)
	wikimediaLimiter = newRateLimiter(rps)
	inaturalistLimiter = newRateLimiter(rps)
	log.Printf("revalidateimages: rate limit %.1f req/s per source (override with REVALIDATEIMAGES_RPS)", rps)

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

	var cached []string
	for sciName := range taxonomy.codeBySciName {
		if fileExists(cache.metaPath(sciName)) {
			cached = append(cached, sciName)
		}
	}
	log.Printf("revalidateimages: %d cached species to re-check", len(cached))

	start := time.Now()
	fixed := 0
	for i, sciName := range cached {
		did, err := revalidateSpeciesImages(cache, sciName)
		if err != nil {
			log.Printf("revalidateimages: %s: %v", sciName, err)
			continue
		}
		if did {
			fixed++
			log.Printf("revalidateimages: %s: replaced/dropped a bad image", sciName)
		}
		if (i+1)%100 == 0 || i+1 == len(cached) {
			log.Printf("revalidateimages: %d/%d checked, %d fixed so far (%s elapsed)",
				i+1, len(cached), fixed, time.Since(start).Round(time.Second))
		}
	}
	log.Printf("revalidateimages: done — %d/%d species had something fixed, in %s", fixed, len(cached), time.Since(start).Round(time.Second))
	return nil
}
