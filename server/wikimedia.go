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

// Resolves a species' principal card image from Wikimedia Commons: the
// Wikipedia infobox photo, filtered by license and by Commons category
// (excluding maps/illustrations/statues etc.), and returns it if it passes.

var allowedLicenseRe = regexp.MustCompile(`(?i)^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)`)

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

// commonsImages returns the species' Wikipedia infobox photo from Wikimedia
// Commons, if it has one and it passes commonsFileInfo's license/category
// checks -- at most one image, never more.
//
// This used to also walk the species' whole Commons category for more
// candidates, but that category is a free-text tag anyone can put on any
// file, with nothing enforcing that it actually depicts the species: real
// species categories were found carrying an unrelated bird's photos (Commons
// contributors miscategorize; see excludeCategoryRe's doc comment for the
// distribution-map/statue cases that filter does catch) with no filename or
// category signal distinguishing the good files from the bad ones. The
// infobox pick is Wikipedia-editor-curated, not just self-tagged, so it's
// trusted as the one Commons image; iNaturalist (server/inaturalist.go),
// where a photo is tied to a specific community-identified observation
// rather than a free-text tag, supplies the rest.
func commonsImages(sciName string, width, max int) []*ImageInfo {
	if max <= 0 {
		return nil
	}
	infobox, err := wikipediaInfoboxFile(sciName)
	if err != nil || infobox == "" {
		return nil
	}
	info, err := commonsFileInfo(infobox, width)
	if err != nil || info == nil {
		return nil
	}
	return []*ImageInfo{info}
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
