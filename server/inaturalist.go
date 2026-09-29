package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// inaturalistImages tops up a species' image set from iNaturalist
// observations when Wikimedia Commons doesn't have enough on its own (see
// images.go's ResolveImages). Restricted to research-grade observations
// (community-confirmed ID — matters more here than on Wikimedia, since
// anyone can upload an observation) ordered by community votes, and to the
// same redistributable licenses wikimedia.go's allowedLicenseRe accepts, so
// one license gate governs both sources. At most one photo per observation,
// so a handful of photos of the same individual bird from one photographer's
// observation can't crowd out the rest of a species' image set.
func inaturalistImages(sciName string, max int) []*ImageInfo {
	if max <= 0 {
		return nil
	}
	var data struct {
		Results []struct {
			Photos []struct {
				ID          int    `json:"id"`
				URL         string `json:"url"`
				Attribution string `json:"attribution"`
				LicenseCode string `json:"license_code"`
			} `json:"photos"`
		} `json:"results"`
	}
	err := inaturalistGetJSON("https://api.inaturalist.org/v1/observations", url.Values{
		"taxon_name":    {sciName},
		"photos":        {"true"},
		"quality_grade": {"research"},
		"order_by":      {"votes"},
		"photo_license": {"cc0,cc-by,pd"},
		"per_page":      {fmt.Sprintf("%d", max*6)}, // more observations than needed: skip disqualified photos without a second request
	}, &data)
	if err != nil {
		return nil
	}

	var out []*ImageInfo
	for _, obs := range data.Results {
		if len(out) >= max {
			break
		}
		if len(obs.Photos) == 0 {
			continue
		}
		photo := obs.Photos[0]
		if !allowedLicenseRe.MatchString(photo.LicenseCode) {
			continue
		}
		license, licenseURL := inaturalistLicense(photo.LicenseCode)
		out = append(out, &ImageInfo{
			Title:       fmt.Sprintf("iNaturalist photo %d", photo.ID),
			DownloadURL: strings.Replace(photo.URL, "/square.", "/medium.", 1),
			Author:      inaturalistAuthor(photo.Attribution),
			License:     license,
			LicenseURL:  licenseURL,
			SourceURL:   fmt.Sprintf("https://www.inaturalist.org/photos/%d", photo.ID),
		})
	}
	return out
}

func inaturalistGetJSON(apiURL string, query url.Values, out any) error {
	req, err := http.NewRequest(http.MethodGet, apiURL+"?"+query.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", birdquizUA)

	resp, err := doThrottled(inaturalistLimiter, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inaturalist request failed: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// inaturalistAttributionAuthorRe pulls the name out of iNaturalist's
// attribution string, e.g. "(c) Mila Turov, some rights reserved (CC BY)" ->
// "Mila Turov". Falls back to the full string when it doesn't match.
var inaturalistAttributionAuthorRe = regexp.MustCompile(`^\(c\)\s*(.+?),`)

func inaturalistAuthor(attribution string) string {
	if m := inaturalistAttributionAuthorRe.FindStringSubmatch(attribution); m != nil {
		return m[1]
	}
	if attribution == "" {
		return "Unknown"
	}
	return attribution
}

// inaturalistLicense maps iNaturalist's short license codes to a display
// name and canonical URL, matching the (name, URL) shape wikimedia.go's
// commonsFileInfo returns. The names are chosen to still match
// allowedLicenseRe, so one license gate covers images from both sources.
func inaturalistLicense(code string) (name, licenseURL string) {
	switch code {
	case "cc0":
		return "CC0", "https://creativecommons.org/publicdomain/zero/1.0/"
	case "pd":
		return "Public Domain", "https://creativecommons.org/publicdomain/mark/1.0/"
	default: // "cc-by" and any other cc-by variant photo_license could return
		return "CC BY", "https://creativecommons.org/licenses/by/4.0/"
	}
}
