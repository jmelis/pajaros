package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GBIF's public occurrence-search API, faceted by scientific name, gives a
// real "how often reported nearby" count in a single unauthenticated call —
// see conversation notes for the verified design (tested against real
// hotspots, cross-checked match rate ~91% against eBird taxonomy). This is
// the "popularity" ranking mode; "category" mode (family grouping) needs
// none of this and stays free.

const gbifEODDatasetKey = "4fa7b334-ce0d-4e88-aaae-2e0c138d049e" // eBird Observation Dataset

const (
	gbifInitialHalfWidthKm = 5.0  // ~10x10km box
	gbifWidenedHalfWidthKm = 20.0 // ~40x40km fallback for sparse areas
	gbifSparseThreshold    = 200  // total records below this triggers one widen-retry
	gbifFacetPageSize      = 1000
)

func gbifGetJSON(query url.Values, out any) error {
	u := "https://api.gbif.org/v1/occurrence/search?" + query.Encode()
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", pajarosUA)
	resp, err := doThrottled(gbifLimiter, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GBIF request failed: %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type gbifFacetResponse struct {
	Count  int `json:"count"`
	Facets []struct {
		Field  string `json:"field"`
		Counts []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"counts"`
	} `json:"facets"`
}

// boundingBox computes a rough halfWidthKm-radius box around (lat, lng).
// Fine for the temperate latitudes this project targets — doesn't handle
// polar regions or boxes crossing the ±180° meridian.
func boundingBox(lat, lng, halfWidthKm float64) (south, north, west, east float64) {
	deltaLat := halfWidthKm / 111.32
	deltaLng := halfWidthKm / (111.32 * math.Cos(lat*math.Pi/180))
	return lat - deltaLat, lat + deltaLat, lng - deltaLng, lng + deltaLng
}

// fetchGBIFCounts runs one (possibly paginated) faceted occurrence search
// over the given box and the last 5 complete years, returning raw GBIF
// scientific-name buckets summed by name and the total record count.
func fetchGBIFCounts(south, north, west, east float64) (map[string]int, int, error) {
	endYear := time.Now().Year() - 1
	startYear := endYear - 4

	counts := make(map[string]int)
	total := 0
	offset := 0
	for {
		var resp gbifFacetResponse
		err := gbifGetJSON(url.Values{
			"datasetKey":         {gbifEODDatasetKey},
			"decimalLatitude":    {fmt.Sprintf("%.4f,%.4f", south, north)},
			"decimalLongitude":   {fmt.Sprintf("%.4f,%.4f", west, east)},
			"year":               {fmt.Sprintf("%d,%d", startYear, endYear)},
			"occurrenceStatus":   {"PRESENT"},
			"hasCoordinate":      {"true"},
			"hasGeospatialIssue": {"false"},
			"limit":              {"0"},
			"facet":              {"scientificName"},
			"facetLimit":         {strconv.Itoa(gbifFacetPageSize)},
			"facetOffset":        {strconv.Itoa(offset)},
		}, &resp)
		if err != nil {
			return nil, 0, err
		}
		total = resp.Count
		if len(resp.Facets) == 0 {
			break
		}
		buckets := resp.Facets[0].Counts
		for _, b := range buckets {
			if name, ok := binomialName(b.Name); ok {
				counts[name] += b.Count
			}
		}
		if len(buckets) < gbifFacetPageSize {
			break
		}
		offset += gbifFacetPageSize
	}
	return counts, total, nil
}

// PopularityCounts returns nearby occurrence counts keyed by eBird
// scientific name, for ranking species by "most reported nearby". Widens
// the box once if the initial result is too sparse to be meaningful.
func PopularityCounts(lat, lng float64) (map[string]int, error) {
	south, north, west, east := boundingBox(lat, lng, gbifInitialHalfWidthKm)
	counts, total, err := fetchGBIFCounts(south, north, west, east)
	if err != nil {
		return nil, err
	}
	if total < gbifSparseThreshold {
		south, north, west, east = boundingBox(lat, lng, gbifWidenedHalfWidthKm)
		widened, widenedTotal, err := fetchGBIFCounts(south, north, west, east)
		if err == nil && widenedTotal > total {
			counts = widened // replace, not merge — avoid double-counting overlap
		}
	}
	return counts, nil
}

// sortByPopularity orders items in place using the app's shared popularity
// rule: highest nearby-report count first, ties broken by common name
// ascending, and items with no count data (count returns false) last. The
// species endpoint's "popularity" mode and the quiz session pool both call
// this, so the ordering is defined in exactly one place.
func sortByPopularity[T any](items []T, count func(T) (int, bool), comName func(T) string) {
	sort.SliceStable(items, func(i, j int) bool {
		ci, iok := count(items[i])
		cj, jok := count(items[j])
		vi, vj := -1, -1
		if iok {
			vi = ci
		}
		if jok {
			vj = cj
		}
		if vi != vj {
			return vi > vj
		}
		return comName(items[i]) < comName(items[j])
	})
}

// binomialName extracts a plain "Genus species" binomial from a GBIF
// scientific name that carries an authority/date suffix (e.g. "Columba
// palumbus Linnaeus, 1758" -> "Columba palumbus"), rejecting hybrids,
// sp./cf./aff. entries, and anything that doesn't start with a clean
// binomial. Deliberately conservative — see conversation notes: leaving a
// name unmatched is fine, matching it wrong is not.
var binomialRe = regexp.MustCompile(`^([A-Z][a-z]+)\s+([a-z][a-z-]+)`)
var rejectRe = regexp.MustCompile(`[×/]|\b(?:sp|cf|aff)\.`)

func binomialName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if rejectRe.MatchString(name) {
		return "", false
	}
	loc := binomialRe.FindStringSubmatchIndex(name)
	if loc == nil {
		return "", false
	}
	end := loc[1]
	if end != len(name) && name[end] != ' ' {
		return "", false // e.g. "Parus majorish" — not a clean word boundary
	}
	return name[loc[2]:loc[3]] + " " + name[loc[4]:loc[5]], true
}
