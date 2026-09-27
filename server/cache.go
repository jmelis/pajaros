package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	// metaMu serializes metadata read-modify-write so a concurrent fetch and
	// name record for the same species can't drop a locale's common name.
	metaMu sync.Mutex
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
//
// lang and comName, when non-empty, are the species' common name in that
// locale; they are recorded in the cache metadata (even for an already-cached
// image) so the fallback distractor pool can show proper common names.
func (c *ImageCache) EnsureFetchedAsync(sciName, lang, comName string) {
	if sciName == "" {
		return
	}
	if fileExists(c.imagePath(sciName)) || fileExists(c.missingMarkerPath(sciName)) {
		c.recordName(sciName, lang, comName)
		return
	}
	if _, alreadyFetching := c.inFlight.LoadOrStore(sciName, struct{}{}); alreadyFetching {
		return
	}
	go func() {
		defer c.inFlight.Delete(sciName)
		c.sem <- struct{}{} // blocks here, in the background goroutine, not the caller
		defer func() { <-c.sem }()
		if err := c.EnsureFetched(sciName, lang, comName); err != nil {
			log.Printf("EnsureFetched(%s): %v", sciName, err)
		}
	}()
}

// EnsureFetched makes sure sciName's principal image (or a "no free image
// found" marker) is on disk, fetching it from Wikimedia if this is the
// first time we've seen this species. Safe to call repeatedly/concurrently
// per species — cheap no-op once cached.
//
// lang/comName are recorded alongside the image metadata (see recordName).
func (c *ImageCache) EnsureFetched(sciName, lang, comName string) error {
	if fileExists(c.imagePath(sciName)) || fileExists(c.missingMarkerPath(sciName)) {
		c.recordName(sciName, lang, comName)
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
	info.SciName = sciName
	if lang != "" && comName != "" {
		info.Names = map[string]string{lang: comName}
	}
	return c.writeMetadata(sciName, info)
}

// recordName merges one locale's common name into sciName's cache metadata,
// creating the metadata file if needed. It is a cheap no-op once that
// locale/name pair is already recorded. Kept separate from image fetching so
// the name can still be captured when the image was cached by an earlier run.
func (c *ImageCache) recordName(sciName, lang, comName string) {
	if lang == "" || comName == "" {
		return
	}
	info := c.readMetadata(sciName)
	if info.Names == nil {
		info.Names = map[string]string{}
	}
	if info.Names[lang] == comName {
		return
	}
	info.Names[lang] = comName
	if info.SciName == "" {
		info.SciName = sciName
	}
	if err := c.writeMetadata(sciName, info); err != nil {
		log.Printf("record name %s: %v", sciName, err)
	}
}

// readMetadata loads sciName's cache metadata, returning an empty ImageInfo
// when there is none (or it can't be parsed). Callers that need to modify it
// must hold metaMu for the read-modify-write to be atomic.
func (c *ImageCache) readMetadata(sciName string) *ImageInfo {
	data, err := os.ReadFile(c.metaPath(sciName))
	if err != nil {
		return &ImageInfo{}
	}
	var info ImageInfo
	if json.Unmarshal(data, &info) != nil {
		return &ImageInfo{}
	}
	return &info
}

// writeMetadata persists info for sciName, merging in any common names (and a
// scientific name) already on disk rather than overwriting them, so fetching
// and name-recording for the same species can't lose each other's data.
func (c *ImageCache) writeMetadata(sciName string, info *ImageInfo) error {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()

	existing := c.readMetadata(sciName)
	if info.SciName == "" {
		info.SciName = existing.SciName
	}
	if info.SciName == "" {
		info.SciName = sciName
	}
	if info.Names == nil {
		info.Names = map[string]string{}
	}
	for lang, name := range existing.Names {
		if _, ok := info.Names[lang]; !ok {
			info.Names[lang] = name
		}
	}
	metaBytes, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(c.metaPath(sciName), metaBytes)
}

// CacheSpecies is one species with cached image metadata. It exists only to
// seed the last-resort distractor pool for a hotspot too small to fill a
// multiple-choice question on its own (see quiz.go's chooseDistractors).
type CacheSpecies struct {
	Code    string // slug, the stable key ImageURLFor uses
	SciName string
	ComName string
}

// CachedSpecies lists the species with a cached image metadata file, sorted by
// name. Identity is the slugified scientific name (the same key ImageURLFor
// uses) because that is all the on-disk cache preserves for entries written
// before ImageInfo carried SciName; newer entries report the real scientific
// name.
//
// Only species with a recorded common name are returned, resolved in lang
// (falling back to English, then any locale). Entries whose metadata predates
// common-name recording are skipped rather than shown as a scientific name or
// slug, since the fallback pool feeds kid-facing multiple-choice options.
func (c *ImageCache) CachedSpecies(lang string) []CacheSpecies {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil
	}
	out := []CacheSpecies{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.dir, e.Name()))
		if err != nil {
			continue
		}
		var info ImageInfo
		if json.Unmarshal(data, &info) != nil {
			continue
		}
		comName := pickCommonName(info.Names, lang)
		if comName == "" {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".json")
		sci := info.SciName
		if sci == "" {
			sci = slug
		}
		out = append(out, CacheSpecies{Code: slug, SciName: sci, ComName: comName})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ComName < out[j].ComName })
	return out
}

// pickCommonName resolves a species' display name for lang: the requested
// locale first, then English, then any recorded locale. Returns "" when no
// common name is known.
func pickCommonName(names map[string]string, lang string) string {
	if name := names[lang]; name != "" {
		return name
	}
	if name := names["en"]; name != "" {
		return name
	}
	for _, name := range names {
		if name != "" {
			return name
		}
	}
	return ""
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
