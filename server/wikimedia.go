package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Resolves a species' principal card image from Wikimedia Commons: search,
// filter by license, by filename, and by Commons category (excluding
// maps/illustrations/statues etc.), and return the first acceptable hit.

var allowedLicenseRe = regexp.MustCompile(`(?i)^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)`)
var excludeFilenameRe = regexp.MustCompile(`(?i)(map|range|distribution|egg|nest|skeleton|anatomy|illustration|drawing|painting|sound|spectrogram|call\b|song\b|vocali|logo|stamp|coin|taxonomy|cladogram)`)

// excludeCategoryRe flags non-photo Commons files that a filename check
// can't catch, because the filename itself carries no hint of the
// content -- a distribution map or a statue's photo can be named anything.
// Commons' own subject categories give away what filenames don't (e.g.
// "Phalacrocoracidae distribution maps", "Statues of birds in France").
var excludeCategoryRe = regexp.MustCompile(`(?i)\b(maps?|statues?|sculptures?|monuments?|illustrations?|drawings?|paintings?|stamps?|coins?|taxonomy|cladograms?|skeletons?|eggs?|nests?|sounds?|recordings?|spectrograms?|logos?)\b`)

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
// returning nil (not an error) if the file is missing or not freely licensed.
func commonsFileInfo(fileTitle string, width int) (*ImageInfo, error) {
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
		"iiprop":     {"url|extmetadata|size"},
		"iiurlwidth": {fmt.Sprintf("%d", width)},
		"cllimit":    {"50"},
		"format":     {"json"},
	}, &data)
	if err != nil {
		return nil, err
	}
	for _, p := range data.Query.Pages {
		if p.Missing != nil || len(p.ImageInfo) == 0 {
			continue
		}
		for _, cat := range p.Categories {
			if excludeCategoryRe.MatchString(strings.TrimPrefix(cat.Title, "Category:")) {
				return nil, nil // not a photo of the bird -- a map, statue, etc.
			}
		}
		info := p.ImageInfo[0]
		license := info.ExtMeta.LicenseShortName.Value
		if license == "" || !allowedLicenseRe.MatchString(license) {
			return nil, nil // not a license we redistribute under
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
		}, nil
	}
	return nil, nil
}

var htmlTagRe = regexp.MustCompile(`<[^>]*>`)
var whitespaceRe = regexp.MustCompile(`\s+`)

func stripHTML(s string) string {
	return strings.TrimSpace(whitespaceRe.ReplaceAllString(htmlTagRe.ReplaceAllString(s, ""), " "))
}

// categoryFileTitles lists candidate photo filenames in a species' Commons
// category, prefiltered by filename (cheap, avoids a wasted commonsFileInfo
// call for the obvious cases) -- commonsFileInfo's category check catches
// what a filename can't (see excludeCategoryRe).
func categoryFileTitles(sciName string) ([]string, error) {
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
		"cmtitle": {"Category:" + sciName},
		"cmtype":  {"file"},
		"cmlimit": {"50"},
		"format":  {"json"},
	}, &data)
	if err != nil {
		return nil, err
	}
	var titles []string
	fileExtRe := regexp.MustCompile(`(?i)\.(jpe?g)$`)
	for _, m := range data.Query.CategoryMembers {
		title := strings.TrimPrefix(m.Title, "File:")
		if title == m.Title {
			continue
		}
		if fileExtRe.MatchString(title) && !excludeFilenameRe.MatchString(title) {
			titles = append(titles, title)
		}
	}
	return titles, nil
}

// commonsImages finds up to max freely-licensed candidate photos for a
// species from Wikimedia: the Wikipedia infobox image first (usually the
// best single representative photo), then its Commons category's other
// files, in listing order. A single bad title (missing, wrong license,
// flagged by commonsFileInfo's category check, a transient API error) is
// skipped rather than aborting the whole lookup, so one problem file can't
// cost the rest of the species' images.
//
// A Commons category is a free-text tag any contributor can put on any
// file, with nothing enforcing that it actually depicts the species --
// commonsFileInfo's category check (excludeCategoryRe) catches an
// unambiguous non-photo (a distribution map, a statue) that a filename gives
// no hint of, but can't catch a file that's simply, genuinely miscategorized
// under the wrong species entirely. That residual risk is accepted here in
// exchange for photo quality: iNaturalist's research-grade bar is about
// identification consensus, not composition, and using it for every
// species' entire image set (as this app briefly did) trades a rare
// mislabeled photo for consistently more amateur-looking ones.
func commonsImages(sciName string, width, max int) []*ImageInfo {
	var out []*ImageInfo
	seen := map[string]bool{}

	add := func(title string) {
		if len(out) >= max || seen[title] {
			return
		}
		seen[title] = true
		info, err := commonsFileInfo(title, width)
		if err != nil || info == nil {
			return
		}
		out = append(out, info)
	}

	if infobox, err := wikipediaInfoboxFile(sciName); err == nil && infobox != "" {
		add(infobox)
	}
	if len(out) < max {
		if titles, err := categoryFileTitles(sciName); err == nil {
			for _, title := range titles {
				if len(out) >= max {
					break
				}
				add(title)
			}
		}
	}
	return out
}

// downloadImage fetches image bytes from downloadURL, throttled through
// limiter (wikimediaLimiter or inaturalistLimiter — see ratelimit.go).
func downloadImage(limiter *rateLimiter, downloadURL string) ([]byte, error) {
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
	return io.ReadAll(resp.Body)
}
