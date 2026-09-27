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

// Port of scripts/fetch_images.py's principal-image resolution, trimmed to
// only what's needed for the card image (no extra photos, no metadata.json
// bookkeeping — that lives in the static-site pipeline, not here).

var allowedLicenseRe = regexp.MustCompile(`(?i)^(cc0|cc[- ]by(-sa)?[- ]?[\d.]*|public domain|pd)`)
var excludeFilenameRe = regexp.MustCompile(`(?i)(map|range|distribution|egg|nest|skeleton|anatomy|illustration|drawing|painting|sound|spectrogram|call\b|song\b|vocali|logo|stamp|coin|taxonomy|cladogram)`)

type ImageInfo struct {
	// SciName is the species this image belongs to, carried in the cache
	// metadata so the image-cache fallback distractor pool (see cache.go's
	// CachedSpecies) can show a real name instead of a slug.
	SciName string
	// Names maps an eBird locale code ("es", "fr", "en") to this species'
	// common name in that locale. The image cache is keyed by scientific name
	// and has no other species data, so the fallback distractor pool relies on
	// these to show proper, localized common names (see CachedSpecies).
	Names       map[string]string
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
	req.Header.Set("User-Agent", pajarosUA)

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
				Missing   *string `json:"missing"`
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
		"prop":       {"imageinfo"},
		"iiprop":     {"url|extmetadata|size"},
		"iiurlwidth": {fmt.Sprintf("%d", width)},
		"format":     {"json"},
	}, &data)
	if err != nil {
		return nil, err
	}
	for _, p := range data.Query.Pages {
		if p.Missing != nil || len(p.ImageInfo) == 0 {
			continue
		}
		info := p.ImageInfo[0]
		license := info.ExtMeta.LicenseShortName.Value
		if license == "" || !allowedLicenseRe.MatchString(license) {
			return nil, nil // not a license we redistribute under
		}
		author := stripHTML(info.ExtMeta.Artist.Value)
		if author == "" {
			author = "Desconocido"
		}
		if len(author) > 100 || strings.ContainsAny(author, "@") || strings.Contains(author, "http://") || strings.Contains(author, "https://") {
			lead := strings.FieldsFunc(author, func(r rune) bool { return r == '.' || r == '(' })
			if len(lead) > 0 && len(strings.TrimSpace(lead[0])) <= 100 {
				author = strings.TrimSpace(lead[0])
			} else {
				author = "Desconocido"
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

// categoryFileTitles lists candidate photo filenames in a species' Commons category.
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

// ResolvePrincipalImage finds a freely-licensed principal photo for a
// species: prefer the Wikipedia infobox image, else the first suitable photo
// in its Commons category. Returns nil (not an error) if none qualifies.
func ResolvePrincipalImage(sciName string, width int) (*ImageInfo, error) {
	if infobox, err := wikipediaInfoboxFile(sciName); err == nil && infobox != "" {
		if info, err := commonsFileInfo(infobox, width); err == nil && info != nil {
			return info, nil
		}
	}
	titles, err := categoryFileTitles(sciName)
	if err != nil {
		return nil, err
	}
	for _, title := range titles {
		info, err := commonsFileInfo(title, width)
		if err != nil {
			return nil, err
		}
		if info != nil {
			return info, nil
		}
	}
	return nil, nil
}

// DownloadImage fetches the image bytes from a Wikimedia URL.
func DownloadImage(downloadURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", pajarosUA)
	resp, err := doThrottled(wikimediaLimiter, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download failed: %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
