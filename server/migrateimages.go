package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// migrationLogName is the append-only operations log migrateOldCacheLayout
// writes under CACHE_DIR, alongside the normal log.Printf output — so a
// migration stays auditable (and its renames reversible) even after pod
// logs have rotated away or the pod that ran it is long gone.
const migrationLogName = "migration.log"

// migrateOldCacheLayout moves every species still in the old flat cache
// layout (one <slug>.jpg / <slug>.json / <slug>.missing directly under dir,
// one image per species) into its bucket (cache.go's bucketed,
// multi-image-per-species layout) — pure local disk I/O (renames and small
// file writes), no network calls, so even a few hundred species finishes in
// a fraction of a second. Each migrated species ends up exactly as if
// ensureFirstImage had just fetched its first image: the existing
// background top-up queue (EnsureFetchedAsync) picks up the rest
// automatically the next time that species is actually viewed.
//
// Run automatically on every server startup (see main.go) — safe to: a
// cache with nothing left in the old layout returns immediately, and a
// species already migrated (or confirmed to have no image) by an earlier
// run is skipped. Also used by the migrateimages CLI mode below, which
// additionally tops every migrated species up right away instead of
// waiting on live traffic.
//
// Returns the scientific names of every species actually migrated. Every
// operation is logged via both log.Printf (kubectl logs et al) and an
// append-only migration.log file under dir, timestamped, so a run stays
// auditable — and its renames reversible — long after the log lines
// themselves have rotated away.
func migrateOldCacheLayout(cache *ImageCache, dir string) ([]string, error) {
	slugs, err := oldFlatSlugs(dir)
	if err != nil {
		return nil, err
	}
	if len(slugs) == 0 {
		return nil, nil
	}

	logFile, err := os.OpenFile(filepath.Join(dir, migrationLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", migrationLogName, err)
	}
	defer logFile.Close()
	logOp := func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		log.Print(line)
		fmt.Fprintf(logFile, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
	}

	start := time.Now()
	logOp("cache layout migration: %d species in the old flat layout", len(slugs))
	var migrated []string
	for _, slug := range slugs {
		sciName, did, err := migrateSlugStructure(cache, dir, slug, logOp)
		if err != nil {
			logOp("cache layout migration: %s: %v", slug, err)
			continue
		}
		if did {
			migrated = append(migrated, sciName)
		}
	}
	logOp("cache layout migration: done, %d/%d species moved in %s",
		len(migrated), len(slugs), time.Since(start).Round(time.Millisecond))
	return migrated, nil
}

// oldFlatSlugs lists every species slug still stored directly under dir in
// the old flat layout — a top-level <slug>.jpg, <slug>.json, or
// <slug>.missing. NewImageCache's bucket subdirectories (2-character hex
// names, no extension) are directories, not files, so they're naturally
// excluded.
func oldFlatSlugs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var slugs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		slug := name
		for _, suffix := range []string{".jpg", ".json", ".missing"} {
			slug = strings.TrimSuffix(slug, suffix)
		}
		if slug == name || seen[slug] { // no recognized suffix, or already recorded
			continue
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	return slugs, nil
}

// oldImageInfo is the old flat cache's single-image metadata shape (no json
// tags on the original ImageInfo, so encoding/json used the Go field names
// verbatim as keys). Read here purely to recover the exact scientific name
// and attribution of an already-downloaded image during migration; the
// current ImageInfo (wikimedia.go) no longer carries SciName/Names.
type oldImageInfo struct {
	SciName     string `json:"SciName"`
	Title       string `json:"Title"`
	DownloadURL string `json:"DownloadURL"`
	Author      string `json:"Author"`
	License     string `json:"License"`
	LicenseURL  string `json:"LicenseURL"`
	SourceURL   string `json:"SourceURL"`
}

// recoverOldMetadata reads slug's old-format metadata JSON at oldJSONPath
// (if present) and returns the species' scientific name plus its
// carried-over image attribution (never nil, so callers can always persist
// it as-is). Falls back to deslugifyGuess for the scientific name when the
// metadata is missing or unreadable.
func recoverOldMetadata(oldJSONPath, slug string) (sciName string, carried *ImageInfo) {
	if data, err := os.ReadFile(oldJSONPath); err == nil {
		var old oldImageInfo
		if json.Unmarshal(data, &old) == nil {
			sciName = old.SciName
			if old.SourceURL != "" {
				carried = &ImageInfo{
					Title: old.Title, DownloadURL: old.DownloadURL, Author: old.Author,
					License: old.License, LicenseURL: old.LicenseURL, SourceURL: old.SourceURL,
				}
			}
		}
	}
	if sciName == "" {
		// No (or unreadable) metadata — happens only for a species that
		// resolved to "no image" on its very first-ever fetch and was never
		// looked up again (a companion .json is written on every later
		// lookup, even a miss — see EnsureFetchedAsync's history). Best
		// effort: reverse the common "Genus species" binomial shape.
		sciName = deslugifyGuess(slug)
	}
	if carried == nil {
		carried = &ImageInfo{}
	}
	return sciName, carried
}

// migrateSlugStructure moves one species' already-downloaded image (if any)
// from the old flat layout into its bucket, without re-downloading it or
// contacting either image source — purely local disk I/O. A species that
// only ever had a ".missing" marker has nothing local to carry over: rather
// than block this fast, network-free pass on a fresh source lookup, its
// stale marker is just dropped, so it resolves again from scratch (now with
// iNaturalist as a second source) the next time it's actually viewed, same
// as any other never-fetched species.
//
// Returns did=false only when there were no old flat files for this slug at
// all (shouldn't happen — callers only pass slugs oldFlatSlugs found);
// did=true whenever this call touched the filesystem, whether that was
// moving an image or just clearing out files a bucket entry (written some
// other way, e.g. by live traffic, before this slug's turn) had already
// made redundant.
func migrateSlugStructure(cache *ImageCache, oldDir, slug string, logOp func(string, ...any)) (sciName string, did bool, err error) {
	oldJPGPath := filepath.Join(oldDir, slug+".jpg")
	oldJSONPath := filepath.Join(oldDir, slug+".json")
	oldMissingPath := filepath.Join(oldDir, slug+".missing")

	var carried *ImageInfo
	sciName, carried = recoverOldMetadata(oldJSONPath, slug)

	if fileExists(oldJPGPath) {
		if fileExists(cache.imagePath(sciName, 0)) || fileExists(cache.missingMarkerPath(sciName)) {
			// The bucket already has this species — fetched some other way
			// (e.g. live traffic) before its turn in this pass. The old flat
			// image is now redundant; drop it rather than leave it orphaned
			// forever (nothing else will ever revisit an "already migrated"
			// slug to clean it up).
			if err := os.Remove(oldJPGPath); err != nil {
				return sciName, false, err
			}
			logOp("REMOVE %s (already present in bucket)", oldJPGPath)
			did = true
		} else {
			newPath := cache.imagePath(sciName, 0)
			if err := os.Rename(oldJPGPath, newPath); err != nil {
				return sciName, false, err
			}
			logOp("RENAME %s -> %s", oldJPGPath, newPath)
			if err := cache.writeMetadata(sciName, []*ImageInfo{carried}); err != nil {
				return sciName, false, err
			}
			did = true
		}
	}

	if fileExists(oldJSONPath) {
		os.Remove(oldJSONPath)
		logOp("REMOVE %s", oldJSONPath)
		did = true
	}
	if fileExists(oldMissingPath) {
		os.Remove(oldMissingPath)
		logOp("REMOVE %s", oldMissingPath)
		did = true
	}
	return sciName, did, nil
}

// deslugifyGuess reverses slugify's lossy transform well enough for the
// overwhelming common case (a two-word "Genus species" binomial with no
// hyphens of its own): split on "-", capitalize the first word. See
// recoverOldMetadata's one caller for when this fallback is actually needed.
func deslugifyGuess(slug string) string {
	words := strings.Split(slug, "-")
	if len(words) > 0 && words[0] != "" {
		words[0] = strings.ToUpper(words[0][:1]) + words[0][1:]
	}
	return strings.Join(words, " ")
}

// runMigrateImages is the `go run . migrateimages` CLI mode: like the
// automatic startup migration (see migrateOldCacheLayout, main.go), but
// additionally tops every migrated species up toward maxImagesPerSpecies
// right away — from both image sources, rate-limited — instead of waiting
// for each one to actually be viewed by live traffic. Useful when you'd
// rather pay that cost once, offline, than have it trickle in gradually.
//
// This is a separate mode of the server binary (not cmd/gensnapshot), same
// reasoning as warmcache.go: ImageCache, the rate limiters and the image
// resolvers all already live in this package.
func runMigrateImages() error {
	rps := rpsFromEnv("MIGRATEIMAGES_RPS", 1)
	wikimediaLimiter = newRateLimiter(rps)
	inaturalistLimiter = newRateLimiter(rps)
	log.Printf("migrateimages: rate limit %.1f req/s per source (override with MIGRATEIMAGES_RPS)", rps)

	cacheDir := os.Getenv("CACHE_DIR")
	if cacheDir == "" {
		cacheDir = "./cache"
	}
	cache, err := NewImageCache(cacheDir) // also pre-creates every bucket dir
	if err != nil {
		return fmt.Errorf("cache dir %q: %w", cacheDir, err)
	}

	sciNames, err := migrateOldCacheLayout(cache, cacheDir)
	if err != nil {
		return fmt.Errorf("migrate cache layout: %w", err)
	}
	if len(sciNames) == 0 {
		log.Printf("migrateimages: nothing left in the old flat layout")
		return nil
	}
	log.Printf("migrateimages: %d species moved into the bucketed layout; topping each up now", len(sciNames))

	start := time.Now()
	for i, sciName := range sciNames {
		if err := cache.topUpImages(sciName); err != nil {
			log.Printf("migrateimages: topUpImages(%s): %v", sciName, err)
		}
		if (i+1)%25 == 0 || i+1 == len(sciNames) {
			log.Printf("migrateimages: %d/%d topped up — %s elapsed", i+1, len(sciNames), time.Since(start).Round(time.Second))
		}
	}
	log.Printf("migrateimages: done in %s", time.Since(start).Round(time.Second))
	return nil
}
