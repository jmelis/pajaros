package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// imagePolicyFile records, under CACHE_DIR, which image sourcing policy the
// cached photos were fetched under. migrateImagePolicy compares it to
// imagePolicyVersion so a policy change is applied exactly once per cache,
// however many times the server restarts afterwards.
const imagePolicyFile = ".image-policy"

// migrationLogName is the append-only operations log migrateImagePolicy
// writes under CACHE_DIR, alongside the normal log.Printf output, so a trim
// stays auditable after pod logs have rotated away.
const migrationLogName = "migration.log"

// imagePolicyVersion names the current sourcing policy (Wikipedia infobox +
// Commons quality images only — see wikimedia.go). Bump it to make the next
// startup re-trim the cache under a new policy.
const imagePolicyVersion = "commons-curated-1"

// migrateImagePolicy brings an existing cache in line with the current
// sourcing policy, once. For every cached species it keeps the first image
// (the title photo: the infobox photo where the species has one) and drops
// the rest, then clears the `.topped` and `.missing` markers. That hands each
// species back to the normal lazy path — EnsureFetchedAsync queues a top-up
// for species with a first image and a first-image fetch for species with
// none, both in the background and on first view — which refills slots under
// the current rules.
//
// Pure local disk I/O, no network calls. The marker is written at the end,
// so a crash midway re-runs the trim on the next start; nothing has been
// refilled by then because the server only starts serving after this returns.
// A fresh cache (nothing to trim) just records the policy.
func migrateImagePolicy(cache *ImageCache, dir string) error {
	marker := filepath.Join(dir, imagePolicyFile)
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == imagePolicyVersion {
		return nil
	}

	slugs, err := cachedSlugs(dir)
	if err != nil {
		return err
	}
	if len(slugs) > 0 {
		logFile, err := os.OpenFile(filepath.Join(dir, migrationLogName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("open %s: %w", migrationLogName, err)
		}
		defer logFile.Close()
		logOp := func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			log.Print(line)
			fmt.Fprintf(logFile, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
		}

		start := time.Now()
		logOp("image policy migration to %s: %d cached species", imagePolicyVersion, len(slugs))
		trimmed, reopened := 0, 0
		for _, slug := range slugs {
			t, r := trimSpeciesToTitleImage(cache, slug, logOp)
			if t {
				trimmed++
			}
			if r {
				reopened++
			}
		}
		logOp("image policy migration: done in %s — %d species trimmed to their title image, %d 'no image' markers cleared",
			time.Since(start).Round(time.Millisecond), trimmed, reopened)
	}
	return writeFileAtomic(marker, []byte(imagePolicyVersion+"\n"))
}

// trimSpeciesToTitleImage keeps slug's first image, drops the others, and
// clears its top-up and "no image" markers. trimmed reports whether any image
// beyond the first was removed; reopened whether a "no image" marker was.
func trimSpeciesToTitleImage(cache *ImageCache, slug string, logOp func(string, ...any)) (trimmed, reopened bool) {
	images := cache.readMetadata(slug)
	if len(images) > 0 {
		last := max(len(images), maxImagesPerSpecies)
		for i := 1; i < last; i++ {
			err := os.Remove(cache.imagePath(slug, i))
			if err == nil {
				trimmed = true
			} else if !os.IsNotExist(err) {
				logOp("image policy migration: %s: remove slot %d: %v", slug, i, err)
			}
		}
		if len(images) > 1 {
			trimmed = true
			if err := cache.writeMetadata(slug, images[:1]); err != nil {
				logOp("image policy migration: %s: write metadata: %v", slug, err)
			}
		}
		if err := os.Remove(cache.toppedMarkerPath(slug)); err != nil && !os.IsNotExist(err) {
			logOp("image policy migration: %s: remove topped marker: %v", slug, err)
		}
	}
	if err := os.Remove(cache.missingMarkerPath(slug)); err == nil {
		reopened = true
	} else if !os.IsNotExist(err) {
		logOp("image policy migration: %s: remove missing marker: %v", slug, err)
	}
	return trimmed, reopened
}

// cachedSlugs lists every species slug that has a metadata or "no image"
// marker file in one of the cache's bucket directories.
func cachedSlugs(dir string) ([]string, error) {
	var slugs []string
	for i := range cacheBucketCount {
		entries, err := os.ReadDir(filepath.Join(dir, bucketName(i)))
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, e := range entries {
			name := e.Name()
			slug := strings.TrimSuffix(strings.TrimSuffix(name, ".json"), ".missing")
			if slug == name || seen[slug] {
				continue
			}
			seen[slug] = true
			slugs = append(slugs, slug)
		}
	}
	return slugs, nil
}
