package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// imageFetchConcurrency caps how many species can be mid-fetch from
// Wikimedia at once. This is separate from wikimediaLimiter's requests/sec
// cap — this bounds pipelining (in-flight goroutines), the limiter bounds
// actual outbound request rate.
const imageFetchConcurrency = 8

// ImageCache is the local-disk species image cache: fetched-on-demand,
// keyed by scientific name (species are shared across hotspots, so this
// pays off fast). No PVC/sidecar yet — this is the "just the Go binary" cut.
type ImageCache struct {
	dir      string
	sem      chan struct{}
	inFlight sync.Map // sciName -> struct{}, so concurrent requests don't double-fetch
}

func NewImageCache(dir string) (*ImageCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &ImageCache{dir: dir, sem: make(chan struct{}, imageFetchConcurrency)}, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(sciName string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(sciName), "-")
	return strings.Trim(s, "-")
}

func (c *ImageCache) imagePath(sciName string) string {
	return filepath.Join(c.dir, slugify(sciName)+".jpg")
}

func (c *ImageCache) metaPath(sciName string) string {
	return filepath.Join(c.dir, slugify(sciName)+".json")
}

func (c *ImageCache) missingMarkerPath(sciName string) string {
	return filepath.Join(c.dir, slugify(sciName)+".missing")
}

// ImageURLFor is the deterministic /images/ URL for a species, regardless of
// whether it's cached yet. The frontend renders a placeholder immediately
// and retries this URL until it 200s (see static/index.html) — so callers
// don't need to block a request on the fetch completing.
func (c *ImageCache) ImageURLFor(sciName string) string {
	return "/images/" + slugify(sciName) + ".jpg"
}

// IsKnownMissing reports whether sciName has already been resolved to "no
// free image found" — as opposed to simply not having been fetched yet. The
// frontend uses this to show a distinct "no photo" placeholder instead of
// polling a URL that will never 200.
func (c *ImageCache) IsKnownMissing(sciName string) bool {
	return fileExists(c.missingMarkerPath(sciName))
}

// EnsureFetchedAsync kicks off a background fetch for sciName if it isn't
// already cached (or already being fetched) and returns immediately. Safe to
// call once per species per request — concurrency is capped process-wide and
// duplicate concurrent fetches for the same species are deduped.
func (c *ImageCache) EnsureFetchedAsync(sciName string) {
	if sciName == "" || fileExists(c.imagePath(sciName)) || fileExists(c.missingMarkerPath(sciName)) {
		return
	}
	if _, alreadyFetching := c.inFlight.LoadOrStore(sciName, struct{}{}); alreadyFetching {
		return
	}
	go func() {
		defer c.inFlight.Delete(sciName)
		c.sem <- struct{}{} // blocks here, in the background goroutine, not the caller
		defer func() { <-c.sem }()
		if err := c.EnsureFetched(sciName); err != nil {
			log.Printf("EnsureFetched(%s): %v", sciName, err)
		}
	}()
}

// EnsureFetched makes sure sciName's principal image (or a "no free image
// found" marker) is on disk, fetching it from Wikimedia if this is the
// first time we've seen this species. Safe to call repeatedly/concurrently
// per species — cheap no-op once cached.
func (c *ImageCache) EnsureFetched(sciName string) error {
	if fileExists(c.imagePath(sciName)) || fileExists(c.missingMarkerPath(sciName)) {
		return nil
	}

	info, err := ResolvePrincipalImage(sciName, maxImageWidth)
	if err != nil {
		return err
	}
	if info == nil {
		return os.WriteFile(c.missingMarkerPath(sciName), []byte{}, 0o644)
	}

	imgBytes, err := DownloadImage(info.DownloadURL)
	if err != nil {
		return err
	}
	imgBytes, err = capImageWidth(imgBytes, maxImageWidth)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(c.imagePath(sciName), imgBytes); err != nil {
		return err
	}
	metaBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.metaPath(sciName), metaBytes)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeFileAtomic writes to a temp file in the same directory and renames it
// into place, so a concurrent reader (or, later, an inotify-watching sidecar)
// never observes a partially-written file.
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
