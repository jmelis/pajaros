package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const ebirdAPIBase = "https://api.ebird.org/v2"

type EbirdClient struct {
	apiKey string
}

func NewEbirdClient(apiKey string) *EbirdClient {
	return &EbirdClient{apiKey: apiKey}
}

func (c *EbirdClient) get(path string, query url.Values, out any) error {
	u := ebirdAPIBase + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-eBirdApiToken", c.apiKey)
	req.Header.Set("User-Agent", pajarosUA)

	resp, err := doThrottled(ebirdLimiter, req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("eBird API %s: unexpected status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type Hotspot struct {
	LocID                string  `json:"locId"`
	LocName              string  `json:"locName"`
	CountryCode          string  `json:"countryCode"`
	Lat                  float64 `json:"lat"`
	Lng                  float64 `json:"lng"`
	LatestObsDt          string  `json:"latestObsDt,omitempty"`
	NumSpeciesAllTime    int     `json:"numSpeciesAllTime,omitempty"`
	NumChecklistsAllTime int     `json:"numChecklistsAllTime,omitempty"`
}

// NearbyHotspots returns hotspots within distKm of (lat, lng), most-active first.
func (c *EbirdClient) NearbyHotspots(lat, lng, distKm float64) ([]Hotspot, error) {
	var hotspots []Hotspot
	err := c.get("/ref/hotspot/geo", url.Values{
		"lat":  {fmt.Sprintf("%g", lat)},
		"lng":  {fmt.Sprintf("%g", lng)},
		"dist": {fmt.Sprintf("%g", distKm)},
		"fmt":  {"json"},
	}, &hotspots)
	if err != nil {
		return nil, err
	}
	sort.Slice(hotspots, func(i, j int) bool {
		return hotspots[i].NumChecklistsAllTime > hotspots[j].NumChecklistsAllTime
	})
	return hotspots, nil
}

// HotspotInfo resolves a single hotspot by its eBird location id — used to
// support deep-linking directly to a hotspot (?locId=...) without first
// doing a nearby-hotspots search.
func (c *EbirdClient) HotspotInfo(locID string) (*Hotspot, error) {
	var h Hotspot
	err := c.get(fmt.Sprintf("/ref/hotspot/info/%s", locID), nil, &h)
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// SpeciesList returns the all-time species codes ever recorded at a hotspot,
// in eBird's own taxonomic order (verified: it's exactly ascending
// taxonOrder — no frequency/commonness signal at all). Species are grouped
// for display by family instead — see Taxon.FamilyComName and main.go.
func (c *EbirdClient) SpeciesList(locID string) ([]string, error) {
	var codes []string
	err := c.get(fmt.Sprintf("/product/spplist/%s", locID), url.Values{"fmt": {"json"}}, &codes)
	return codes, err
}

type Taxon struct {
	SciName       string  `json:"sciName"`
	ComName       string  `json:"comName"`
	SpeciesCode   string  `json:"speciesCode"`
	Order         string  `json:"order"`
	FamilyCode    string  `json:"familyCode"`
	FamilyComName string  `json:"familyComName"`
	TaxonOrder    float64 `json:"taxonOrder"`
}

// Taxonomy resolves species codes to scientific + localized common names in
// one batch call, keyed by species code. Note: `locale` only localizes
// ComName — verified empirically that FamilyComName always comes back in
// English regardless of locale. See families.go for how family names are
// translated instead.
func (c *EbirdClient) Taxonomy(speciesCodes []string, locale string) (map[string]Taxon, error) {
	if len(speciesCodes) == 0 {
		return map[string]Taxon{}, nil
	}
	var taxa []Taxon
	err := c.get("/ref/taxonomy/ebird", url.Values{
		"species": {strings.Join(speciesCodes, ",")},
		"locale":  {locale},
		"fmt":     {"json"},
	}, &taxa)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Taxon, len(taxa))
	for _, t := range taxa {
		out[t.SpeciesCode] = t
	}
	return out, nil
}
