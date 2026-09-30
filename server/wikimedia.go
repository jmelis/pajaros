package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Resolves a species' card photos from two curated Wikimedia sources only:
// the Wikipedia infobox image and the members of Commons'
// "Category:Quality images of <taxon>". Both are human-reviewed, which is why
// they're trusted; uncurated category files and other providers produced too
// many poor photos. Every candidate is additionally filtered by license,
// size, aspect ratio, filename and Commons categories (see commonsFileInfo).
// Returning fewer photos -- even none -- is preferred over a poor one.

var allowedLicenseRe = regexp.MustCompile(`(?i)^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)`)
var restrictedLicenseRe = regexp.MustCompile(`(?i)\b(nc|nd)\b`)

// licenseAllowed reports whether a Commons license short name is one we can
// redistribute: CC0, public domain, CC BY or CC BY-SA, never the NonCommercial
// or NoDerivatives variants.
func licenseAllowed(license string) bool {
	return license != "" && allowedLicenseRe.MatchString(license) && !restrictedLicenseRe.MatchString(license)
}

var excludeFilenameRe = regexp.MustCompile(`(?i)\b(maps?|range|distribution|eggs?|nests?|skeletons?|anatomy|illustrations?|drawings?|paintings?|sounds?|spectrograms?|calls?|songs?|vocali\w*|logos?|stamps?|coins?|taxonomy|cladograms?|chicks?|juv|juveniles?|immatures?|nestlings?|fledglings?)\b`)

// excludeCategoryRe flags files that a filename check can't catch, because
// the filename carries no hint of the content. Commons' own categories give
// away what filenames don't: non-photos (maps, statues, stamps), non-wild or
// non-adult subjects (captive, juveniles, eggs, nests), and composition
// problems (flocks, "with other species", incidental appearances).
var excludeCategoryRe = regexp.MustCompile(`(?i)\b(with other species|incidental|flocks?|pairs?|chicks?|families|juveniles?|immatures?|eggs?|nests?|nesting|captive|captivity|museum|specimens?|taxidermy|illustrations?|drawings?|paintings?|anatomy|skeletons?|audio|videos?|hybrids?|leucistic|albinos?|melanistic|domestic(us|a)?|feral|dead|carcass(es)?|roadkill|injured|rehabilitation|zoos?|aviar(y|ies)|cages?|distribution|maps?|statues?|sculptures?|monuments?|stamps?|coins?|logos?|taxonomy|cladograms?|spectrograms?|sounds?|recordings?|feathers?|footprints?|coats? of arms|heraldry|symbols?|flags?|in art|art)\b`)

const (
	minImageWidth  = 1000
	minImageHeight = 650
	minAspect      = 0.7
	maxAspect      = 2.3
	maxPerAuthor   = 2
)

// birdSciNames is the set of every species scientific name in the embedded
// taxonomy, used to spot a file that's also categorized under another bird.
var birdSciNames = sync.OnceValue(func() map[string]bool {
	store, err := loadTaxonomyStore()
	if err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(store.codeBySciName))
	for name := range store.codeBySciName {
		out[name] = true
	}
	return out
})

var categoryQualifierRe = regexp.MustCompile(`\s*\(.*\)$`)

// ImageInfo is one candidate (or already-cached) image for a species,
// however many of them the cache keeps — see cache.go's per-species image
// slots and maxImagesPerSpecies in images.go.
type ImageInfo struct {
	Title       string
	DownloadURL string
	Author      string
	License     string
	LicenseURL  string
	SourceURL   string
}

func wikimediaGetJSON(apiURL string, query url.Values, out any) error {
	req, err := http.NewRequest(http.MethodGet, apiURL+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", birdquizUA)

	resp, err := doThrottled(wikimediaLimiter, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wikimedia request failed: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// wikipediaInfoboxFile returns the Commons file name used as the Wikipedia
// infobox image for a species, if any.
func wikipediaInfoboxFile(sciName string) (string, error) {
	var data struct {
		Query struct {
			Pages map[string]struct {
				PageImage string `json:"pageimage"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := wikimediaGetJSON("https://en.wikipedia.org/w/api.php", url.Values{
		"action":    {"query"},
		"titles":    {sciName},
		"redirects": {"1"},
		"prop":      {"pageimages"},
		"piprop":    {"name"},
		"format":    {"json"},
	}, &data)
	if err != nil {
		return "", err
	}
	for _, p := range data.Query.Pages {
		if p.PageImage != "" {
			return strings.ReplaceAll(p.PageImage, "_", " "), nil
		}
	}
	return "", nil
}

// commonsFileInfo fetches license + a thumbnail URL for a Commons file,
// returning nil (not an error) if the file is missing, not freely licensed,
// too small, oddly proportioned, or flagged by its filename or categories.
// ours is the set of Commons category names that denote the target species
// (its scientific name and its Commons category); a file also filed under
// another bird species' category is rejected as a multi-species photo.
// The second return value is the file's pixel area, for ranking candidates.
func commonsFileInfo(fileTitle string, width int, ours map[string]bool) (*ImageInfo, int, error) {
	var data struct {
		Query struct {
			Pages map[string]struct {
				Missing    *string `json:"missing"`
				Categories []struct {
					Title string `json:"title"`
				} `json:"categories"`
				ImageInfo []struct {
					ThumbURL string `json:"thumburl"`
					URL      string `json:"url"`
					Width    int    `json:"width"`
					Height   int    `json:"height"`
					Mime     string `json:"mime"`
					ExtMeta  struct {
						LicenseShortName struct{ Value string } `json:"LicenseShortName"`
						LicenseURL       struct{ Value string } `json:"LicenseUrl"`
						Artist           struct{ Value string } `json:"Artist"`
					} `json:"extmetadata"`
				} `json:"imageinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := wikimediaGetJSON("https://commons.wikimedia.org/w/api.php", url.Values{
		"action":     {"query"},
		"titles":     {"File:" + fileTitle},
		"prop":       {"imageinfo|categories"},
		"iiprop":     {"url|extmetadata|size|mime"},
		"iiurlwidth": {fmt.Sprintf("%d", width)},
		"cllimit":    {"max"},
		"format":     {"json"},
	}, &data)
	if err != nil {
		return nil, 0, err
	}
	if excludeFilenameRe.MatchString(fileTitle) {
		return nil, 0, nil
	}
	birds := birdSciNames()
	for _, p := range data.Query.Pages {
		if p.Missing != nil || len(p.ImageInfo) == 0 {
			continue
		}
		info := p.ImageInfo[0]
		if info.Mime != "image/jpeg" {
			return nil, 0, nil
		}
		if info.Width < minImageWidth || info.Height < minImageHeight {
			return nil, 0, nil
		}
		if aspect := float64(info.Width) / float64(info.Height); aspect < minAspect || aspect > maxAspect {
			return nil, 0, nil
		}
		for _, cat := range p.Categories {
			name := strings.TrimPrefix(cat.Title, "Category:")
			if excludeCategoryRe.MatchString(name) {
				return nil, 0, nil
			}
			if base := categoryQualifierRe.ReplaceAllString(name, ""); birds[base] && !ours[base] {
				return nil, 0, nil
			}
		}
		license := info.ExtMeta.LicenseShortName.Value
		if !licenseAllowed(license) {
			return nil, 0, nil // not a license we redistribute under
		}
		author := stripHTML(info.ExtMeta.Artist.Value)
		if author == "" {
			author = "Unknown"
		}
		if len(author) > 100 || strings.ContainsAny(author, "@") || strings.Contains(author, "http://") || strings.Contains(author, "https://") {
			lead := strings.FieldsFunc(author, func(r rune) bool { return r == '.' || r == '(' })
			if len(lead) > 0 && len(strings.TrimSpace(lead[0])) <= 100 {
				author = strings.TrimSpace(lead[0])
			} else {
				author = "Unknown"
			}
		}
		downloadURL := info.ThumbURL
		if downloadURL == "" {
			downloadURL = info.URL
		}
		licenseURL := info.ExtMeta.LicenseURL.Value
		if licenseURL == "" {
			licenseURL = "https://commons.wikimedia.org/wiki/Commons:Licensing"
		}
		return &ImageInfo{
			Title:       fileTitle,
			DownloadURL: downloadURL,
			Author:      author,
			License:     license,
			LicenseURL:  licenseURL,
			SourceURL:   "https://commons.wikimedia.org/wiki/File:" + url.PathEscape(strings.ReplaceAll(fileTitle, " ", "_")),
		}, info.Width * info.Height, nil
	}
	return nil, 0, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var whitespaceRe = regexp.MustCompile(`\s+`)

func stripHTML(s string) string {
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(htmlTagRe.ReplaceAllString(s, ""), " "))
}

// commonsCategoryFor returns the Commons category name for sciName: the
// category named after the scientific name when it has files, else the
// category Wikidata records for the taxon (P373), which covers names Commons
// files under a taxonomic synonym. Empty if neither exists.
func commonsCategoryFor(sciName string) string {
	if n, err := commonsCategoryFileCount(sciName); err == nil && n > 0 {
		return sciName
	}
	var page struct {
		Query struct {
			Pages map[string]struct {
				PageProps struct {
					WikibaseItem string `json:"wikibase_item"`
				} `json:"pageprops"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := wikimediaGetJSON("https://en.wikipedia.org/w/api.php", url.Values{
		"action":    {"query"},
		"titles":    {sciName},
		"redirects": {"1"},
		"prop":      {"pageprops"},
		"ppprop":    {"wikibase_item"},
		"format":    {"json"},
	}, &page)
	if err != nil {
		return ""
	}
	qid := ""
	for _, p := range page.Query.Pages {
		if p.PageProps.WikibaseItem != "" {
			qid = p.PageProps.WikibaseItem
		}
	}
	if qid == "" {
		return ""
	}
	var claims struct {
		Claims struct {
			P373 []struct {
				Mainsnak struct {
					Datavalue struct {
						Value string `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"P373"`
		} `json:"claims"`
	}
	err = wikimediaGetJSON("https://www.wikidata.org/w/api.php", url.Values{
		"action":   {"wbgetclaims"},
		"entity":   {qid},
		"property": {"P373"},
		"format":   {"json"},
	}, &claims)
	if err != nil || len(claims.Claims.P373) == 0 {
		return ""
	}
	return claims.Claims.P373[0].Mainsnak.Datavalue.Value
}

// commonsCategoryFileCount returns how many files Category:<name> directly
// holds (0 if it doesn't exist).
func commonsCategoryFileCount(name string) (int, error) {
	var data struct {
		Query struct {
			Pages map[string]struct {
				Missing      *string `json:"missing"`
				CategoryInfo struct {
					Files int `json:"files"`
				} `json:"categoryinfo"`
			} `json:"pages"`
		} `json:"query"`
	}
	err := wikimediaGetJSON("https://commons.wikimedia.org/w/api.php", url.Values{
		"action": {"query"},
		"titles": {"Category:" + name},
		"prop":   {"categoryinfo"},
		"format": {"json"},
	}, &data)
	if err != nil {
		return 0, err
	}
	for _, p := range data.Query.Pages {
		if p.Missing != nil {
			return 0, nil
		}
		return p.CategoryInfo.Files, nil
	}
	return 0, nil
}

// qualityImageTitles lists the files in Category:Quality images of <cat>, a
// human-curated set. Returns nil when the category doesn't exist.
func qualityImageTitles(cat string) ([]string, error) {
	var data struct {
		Query struct {
			CategoryMembers []struct {
				Title string `json:"title"`
			} `json:"categorymembers"`
		} `json:"query"`
	}
	err := wikimediaGetJSON("https://commons.wikimedia.org/w/api.php", url.Values{
		"action":  {"query"},
		"list":    {"categorymembers"},
		"cmtitle": {"Category:Quality images of " + cat},
		"cmtype":  {"file"},
		"cmlimit": {"500"},
		"format":  {"json"},
	}, &data)
	if err != nil {
		return nil, err
	}
	var titles []string
	for _, m := range data.Query.CategoryMembers {
		if title := strings.TrimPrefix(m.Title, "File:"); title != m.Title {
			titles = append(titles, title)
		}
	}
	return titles, nil
}

// commonsImages finds up to max photos for a species from two curated
// sources: the Wikipedia infobox image first (usually the best single
// representative photo), then Commons' "Quality images of <taxon>" members,
// largest first with at most maxPerAuthor per photographer. Files failing
// commonsFileInfo's filters are skipped. Fewer than max (or zero) is a
// normal result: a missing photo is better than a poor one.
func commonsImages(sciName string, width, max int) []*ImageInfo {
	var out []*ImageInfo
	seen := map[string]bool{}
	ours := map[string]bool{sciName: true}
	cat := ""
	catResolved := false
	resolveCat := func() {
		if !catResolved {
			catResolved = true
			if cat = commonsCategoryFor(sciName); cat != "" {
				ours[cat] = true
			}
		}
	}

	if infobox, err := wikipediaInfoboxFile(sciName); err == nil && infobox != "" {
		seen[infobox] = true
		resolveCat()
		if info, _, err := commonsFileInfo(infobox, width, ours); err == nil && info != nil {
			out = append(out, info)
		}
	}
	if len(out) >= max {
		return out
	}

	resolveCat()
	if cat == "" {
		return out
	}
	titles, err := qualityImageTitles(cat)
	if err != nil {
		return out
	}
	type candidate struct {
		info *ImageInfo
		area int
	}
	var good []candidate
	need := max - len(out)
	for _, title := range titles {
		if seen[title] {
			continue
		}
		if len(good) >= need*3 {
			break
		}
		seen[title] = true
		info, area, err := commonsFileInfo(title, width, ours)
		if err != nil || info == nil {
			continue
		}
		good = append(good, candidate{info, area})
	}
	sort.SliceStable(good, func(i, j int) bool { return good[i].area > good[j].area })
	perAuthor := map[string]int{}
	for _, c := range good {
		if len(out) >= max {
			break
		}
		if perAuthor[c.info.Author] >= maxPerAuthor {
			continue
		}
		perAuthor[c.info.Author]++
		out = append(out, c.info)
	}
	return out
}

// downloadImage fetches image bytes from downloadURL, throttled through
// wikimediaLimiter.
func downloadImage(downloadURL string) ([]byte, error) {
	limiter := wikimediaLimiter
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", birdquizUA)
	resp, err := doThrottled(limiter, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download failed: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		upstreamResponseBytesTotal.WithLabelValues(limiter.name).Add(float64(len(body)))
	}
	return body, err
}
