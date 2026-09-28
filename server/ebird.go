package main

// api.ebird.org blocks requests from this project's cloud egress IP
// (verified: same key, same request, works from a residential connection
// and returns 403 from the deployed pod's address — a common anti-scraping
// measure against hosting-provider ranges). The server makes no live calls
// to eBird or GBIF at request time; both Hotspot and Taxon are served from
// offline data built ahead of time — see docs/GBIF_DATA_PIPELINE.md.

// Hotspot is one GBIF-derived point (see docs/GBIF_DATA_PIPELINE.md) — the
// server's unit of "a place with birds," not eBird's curated hotspot list.
// ID is "lat,lng" and doubles as the key into the bbolt species store
// (species_store.go), so no separate ID translation is needed anywhere.
// Name is whatever eBird's own location-naming convention put in GBIF's
// `locality` field for that point — often a real hotspot name, sometimes a
// personal location's description. TotalCount is the summed observation
// count across every species ever recorded there. JSON tags keep the old
// locId/locName names (rather than matching the Go field names) purely to
// minimize the frontend diff — static/app.js reads these field names in
// many places, and only the ID *format* actually changed, not its role.
type Hotspot struct {
	ID         string  `json:"locId"`
	Name       string  `json:"locName"`
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	TotalCount int     `json:"totalCount"`
}

// Taxon is eBird taxonomy data for one species. ComName is the only field
// that varies by locale (verified empirically: familyComName always comes
// back in English regardless of locale) — see families.go for how family
// names are translated instead.
type Taxon struct {
	SciName       string  `json:"sciName"`
	ComName       string  `json:"comName"`
	SpeciesCode   string  `json:"speciesCode"`
	Order         string  `json:"order"`
	FamilyCode    string  `json:"familyCode"`
	FamilyComName string  `json:"familyComName"`
	TaxonOrder    float64 `json:"taxonOrder"`
}
