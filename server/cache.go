package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// imageFetchConcurrency caps how many species can be mid-fetch for their
// *first* image at once — the hot path, triggered directly by a live
// Browse/Learn request. This is separate from wikimediaLimiter's
// requests/sec cap — this bounds pipelining (in-flight goroutines), the
// limiter bounds actual outbound request rate.
const imageFetchConcurrency = 8

// topUpConcurrency caps how many species can be mid-fetch for their
// *remaining* images (2..maxImagesPerSpecies) at once — deliberately smaller
// than imageFetchConcurrency and on its own semaphore, so a hotspot full of
// top-up work (each species needs up to 3 more images) can never crowd out
// a *different* species' first-image fetch. A first image is cheap — one
// source lookup, one download; a full top-up can mean several source
// lookups and downloads per species, plainly not something a live request
// should wait anywhere near — this is strictly best-effort background work.
const topUpConcurrency = 3

// cacheBucketCount shards the on-disk cache into this many subdirectories
// (see bucketFor) so a single directory never has to hold every species'
// files directly — at full eBird taxonomy scale (~11K species, up to
// maxImagesPerSpecies images each) that would be tens of thousands of files
// in one place. 256 (a full byte of hash) keeps each bucket to a manageable
// few hundred files.
const cacheBucketCount = 256

// ImageCache is the local-disk species image cache: fetched-on-demand,
// keyed by scientific name (species are shared across hotspots, so this
// pays off fast). No PVC/sidecar yet — this is the "just the Go binary" cut.
type ImageCache struct {
	dir           string
	sem           chan struct{} // bounds first-image fetches (imageFetchConcurrency)
	topUpSem      chan struct{} // bounds top-up fetches (topUpConcurrency)
	inFlight      sync.Map      // sciName -> struct{}, dedupes concurrent first-image fetches
	topUpInFlight sync.Map      // sciName -> struct{}, dedupes concurrent top-up fetches
}

func NewImageCache(dir string) (*ImageCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	// Pre-create every bucket once up front, rather than a MkdirAll per
	// write: cheap (256 calls, once, at startup) and means every later
	// write is a plain file create with no directory-existence check.
	for i := range cacheBucketCount {
		if err := os.MkdirAll(filepath.Join(dir, bucketName(i)), 0o755); err != nil {
			return nil, err
		}
	}
	return &ImageCache{
		dir:      dir,
		sem:      make(chan struct{}, imageFetchConcurrency),
		topUpSem: make(chan struct{}, topUpConcurrency),
	}, nil
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(sciName string) string {
	s := slugRe.ReplaceAllString(strings.ToLower(sciName), "-")
	return strings.Trim(s, "-")
}

func bucketName(i int) string { return fmt.Sprintf("%02x", i) }

// bucketFor deterministically assigns slug to one of cacheBucketCount
// subdirectories, by hash rather than by slug prefix — genus names cluster
// alphabetically (lots of species share a first letter or two), which would
// load a few prefix-based buckets far more than others.
func bucketFor(slug string) string {
	h := fnv.New32a()
	h.Write([]byte(slug))
	return bucketName(int(h.Sum32() & (cacheBucketCount - 1)))
}

func (c *ImageCache) speciesDir(sciName string) string {
	slug := slugify(sciName)
	return filepath.Join(c.dir, bucketFor(slug))
}

func (c *ImageCache) imagePath(sciName string, i int) string {
	return filepath.Join(c.speciesDir(sciName), fmt.Sprintf("%s-%d.jpg", slugify(sciName), i))
}

func (c *ImageCache) metaPath(sciName string) string {
	return filepath.Join(c.speciesDir(sciName), slugify(sciName)+".json")
}

func (c *ImageCache) missingMarkerPath(sciName string) string {
	return filepath.Join(c.speciesDir(sciName), slugify(sciName)+".missing")
}

// toppedMarkerPath is an empty marker written once a species' top-up pass
// (fetching past its first image, up to maxImagesPerSpecies) has been
// attempted — regardless of how many more images it actually found. Without
// it, a species that naturally has fewer than maxImagesPerSpecies (nothing
// more to find) would look identical to one that's simply never had a
// top-up attempted, and every later request would re-queue it.
func (c *ImageCache) toppedMarkerPath(sciName string) string {
	return filepath.Join(c.speciesDir(sciName), slugify(sciName)+".topped")
}

// ImageURLFor is the deterministic /images/ URL for a species' first image,
// regardless of whether it's cached yet — see ImageURLsFor.
func (c *ImageCache) ImageURLFor(sciName string) string {
	return c.ImageURLsFor(sciName)[0]
}

// ImageURLsFor returns maxImagesPerSpecies deterministic /images/ URLs for a
// species (one per image slot), regardless of how many are actually cached
// yet. The frontend renders placeholders immediately and retries each URL
// until it 200s (see static/app.js) — so callers don't need to block a
// request on the fetch completing, and a slot that never gets a real image
// (fewer than maxImagesPerSpecies were found) just 404s forever, which the
// frontend treats as "no such slide" after a bounded retry.
func (c *ImageCache) ImageURLsFor(sciName string) []string {
	slug := slugify(sciName)
	bucket := bucketFor(slug)
	urls := make([]string, maxImagesPerSpecies)
	for i := range urls {
		urls[i] = fmt.Sprintf("/images/%s/%s-%d.jpg", bucket, slug, i)
	}
	return urls
}

// IsKnownMissing reports whether sciName has already been resolved to "no
// free image found" — as opposed to simply not having been fetched yet. The
// frontend uses this to show a distinct "no photo" placeholder instead of
// polling a URL that will never 200.
func (c *ImageCache) IsKnownMissing(sciName string) bool {
	return fileExists(c.missingMarkerPath(sciName))
}

// EnsureFetchedAsync kicks off whatever background work sciName still needs
// and returns immediately — never blocks a live request. It's a two-tier
// hot path: a species with no image yet gets just its *first* image fetched
// on the higher-priority queue (imageFetchConcurrency), so a hotspot full of
// unseen species starts showing something as fast as possible; a species
// that already has its first image but hasn't been topped up gets queued on
// the separate, smaller, lower-priority queue (topUpConcurrency) for the
// rest (up to maxImagesPerSpecies). Safe to call repeatedly per species —
// each tier is deduped and a species that's fully resolved (or confirmed to
// have no image at all) is a cheap stat-only no-op.
func (c *ImageCache) EnsureFetchedAsync(sciName string) {
	if sciName == "" {
		return
	}
	if fileExists(c.missingMarkerPath(sciName)) || fileExists(c.toppedMarkerPath(sciName)) {
		return
	}
	if fileExists(c.imagePath(sciName, 0)) {
		c.enqueueTopUp(sciName)
		return
	}
	c.enqueueFirstImage(sciName)
}

// enqueueFirstImage fetches just sciName's first image on the high-priority
// queue, then — once it's on disk — hands the species off to the top-up
// queue for the rest. Deduped and non-blocking.
func (c *ImageCache) enqueueFirstImage(sciName string) {
	if _, already := c.inFlight.LoadOrStore(sciName, struct{}{}); already {
		return
	}
	go func() {
		defer c.inFlight.Delete(sciName)
		c.sem <- struct{}{} // blocks here, in the background goroutine, not the caller
		imageFetchInflight.WithLabelValues("first").Inc()
		defer func() { <-c.sem; imageFetchInflight.WithLabelValues("first").Dec() }()
		if err := c.ensureFirstImage(sciName); err != nil {
			log.Printf("ensureFirstImage(%s): %v", sciName, err)
			return
		}
		c.enqueueTopUp(sciName)
	}()
}

// enqueueTopUp fetches the rest of sciName's images (past the first, up to
// maxImagesPerSpecies) on the low-priority queue. Deduped and non-blocking.
func (c *ImageCache) enqueueTopUp(sciName string) {
	if _, already := c.topUpInFlight.LoadOrStore(sciName, struct{}{}); already {
		return
	}
	go func() {
		defer c.topUpInFlight.Delete(sciName)
		c.topUpSem <- struct{}{}
		imageFetchInflight.WithLabelValues("topup").Inc()
		defer func() { <-c.topUpSem; imageFetchInflight.WithLabelValues("topup").Dec() }()
		if err := c.topUpImages(sciName); err != nil {
			log.Printf("topUpImages(%s): %v", sciName, err)
		}
	}()
}

// EnsureFetched synchronously resolves sciName all the way — first image
// then top-up — and only returns once both are done (or confirmed there's
// nothing to find). Unlike EnsureFetchedAsync, this blocks the caller: it's
// for offline batch tools (warmcache, migrateimages) that want a species
// fully resolved before moving to the next one, not live request serving.
func (c *ImageCache) EnsureFetched(sciName string) error {
	if err := c.ensureFirstImage(sciName); err != nil {
		return err
	}
	if fileExists(c.missingMarkerPath(sciName)) {
		return nil
	}
	return c.topUpImages(sciName)
}

// ensureFirstImage makes sure sciName has an image in slot 0, or a "no free
// image found" marker, fetching from Wikimedia if this is the
// first time we've seen this species. Cheap no-op once resolved either way.
func (c *ImageCache) ensureFirstImage(sciName string) error {
	if fileExists(c.imagePath(sciName, 0)) || fileExists(c.missingMarkerPath(sciName)) {
		return nil
	}
	start := time.Now()
	images := ResolveImages(sciName, maxImageWidth, 1)
	if len(images) == 0 {
		imageCacheMissingTotal.Inc()
		imageFetchTotal.WithLabelValues("none", "first", "not_found").Inc()
		imageFetchDuration.WithLabelValues("none", "first").Observe(time.Since(start).Seconds())
		return os.WriteFile(c.missingMarkerPath(sciName), []byte{}, 0o644)
	}
	const source = "wikimedia"
	if err := c.downloadAndSave(sciName, images[0], 0); err != nil {
		imageFetchTotal.WithLabelValues(source, "first", "error").Inc()
		imageFetchDuration.WithLabelValues(source, "first").Observe(time.Since(start).Seconds())
		// Nothing persisted — a later call retries from scratch rather than
		// wrongly recording this species as having no image at all.
		return err
	}
	imageFetchTotal.WithLabelValues(source, "first", "success").Inc()
	imageFetchDuration.WithLabelValues(source, "first").Observe(time.Since(start).Seconds())
	return c.writeMetadata(sciName, images)
}

// topUpImages fetches sciName's remaining images, past whatever's already in
// its metadata, up to maxImagesPerSpecies, then writes the "topped" marker
// regardless of outcome — a species that genuinely has fewer than
// maxImagesPerSpecies available shouldn't be re-queued on every later
// request. Safe to call repeatedly/concurrently — cheap no-op once topped.
func (c *ImageCache) topUpImages(sciName string) error {
	defer func() {
		if err := os.WriteFile(c.toppedMarkerPath(sciName), []byte{}, 0o644); err != nil {
			log.Printf("write topped marker %s: %v", sciName, err)
		}
	}()

	existing := c.readMetadata(sciName)
	if len(existing) == 0 || len(existing) >= maxImagesPerSpecies {
		return nil
	}

	fresh := ResolveImages(sciName, maxImageWidth, maxImagesPerSpecies)
	seen := make(map[string]bool, len(existing))
	for _, info := range existing {
		seen[info.SourceURL] = true
	}
	final := append([]*ImageInfo{}, existing...)
	for _, info := range fresh {
		if seen[info.SourceURL] {
			continue
		}
		if len(final) >= maxImagesPerSpecies {
			break
		}
		start := time.Now()
		const source = "wikimedia"
		if err := c.downloadAndSave(sciName, info, len(final)); err != nil {
			imageFetchTotal.WithLabelValues(source, "topup", "error").Inc()
			imageFetchDuration.WithLabelValues(source, "topup").Observe(time.Since(start).Seconds())
			log.Printf("download image %s (%s): %v", sciName, info.SourceURL, err)
			continue
		}
		imageFetchTotal.WithLabelValues(source, "topup", "success").Inc()
		imageFetchDuration.WithLabelValues(source, "topup").Observe(time.Since(start).Seconds())
		final = append(final, info)
	}
	if len(final) == len(existing) {
		return nil // nothing new actually landed; metadata is already current
	}
	return c.writeMetadata(sciName, final)
}

// downloadAndSave downloads info's image (throttled through
// wikimediaLimiter), caps it to maxImageWidth, and writes it to
// sciName's slot-th image slot.
func (c *ImageCache) downloadAndSave(sciName string, info *ImageInfo, slot int) error {
	imgBytes, err := downloadImage(info.DownloadURL)
	if err != nil {
		return err
	}
	imgBytes, err = capImageWidth(imgBytes, maxImageWidth)
	if err != nil {
		return err
	}
	return writeFileAtomic(c.imagePath(sciName, slot), imgBytes)
}

// speciesImages is the on-disk shape of a species' shared metadata file: one
// entry per cached image slot, in slot order.
type speciesImages struct {
	Images []*ImageInfo `json:"images"`
}

func (c *ImageCache) writeMetadata(sciName string, images []*ImageInfo) error {
	data, err := json.MarshalIndent(speciesImages{Images: images}, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.metaPath(sciName), data)
}

// readMetadata returns sciName's currently-cached images, in slot order, or
// nil if there's no metadata yet (or it can't be parsed).
func (c *ImageCache) readMetadata(sciName string) []*ImageInfo {
	data, err := os.ReadFile(c.metaPath(sciName))
	if err != nil {
		return nil
	}
	var m speciesImages
	if json.Unmarshal(data, &m) != nil {
		return nil
	}
	return m.Images
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
