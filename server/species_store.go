package main

import (
	"sort"

	"github.com/jmelis/pajaros/server/internal/seasonal"
)

// SpeciesStore answers "which species, how often" for an area key. The pooled
// counts come from entries (an AreaResolver in production), which sums the
// seasonal_cells.bolt data inside the area; taxonByID maps the stable species
// IDs in that data to taxa (position i = ID i, see TaxonomyStore.StableIDOrder).
type SpeciesStore struct {
	entries   func(key string) ([]seasonal.Entry, error)
	taxonByID []Taxon
}

func newSpeciesStore(areas *AreaResolver, taxonomy *TaxonomyStore) *SpeciesStore {
	return &SpeciesStore{entries: areas.Entries, taxonByID: taxonomy.StableIDOrder()}
}

// SpeciesCount is one species' observation count in an area.
type SpeciesCount struct {
	Code    string
	SciName string
	Count   int
}

// Lookup returns an area's species with their record counts, sorted by
// count descending (ties by species ID). month is 1..12 for the records of
// that calendar month only (years pooled; species never recorded in it are
// absent), or 0 for the whole year. An area with no records in the
// month yields an empty list; an unknown place yields errUnknownArea.
func (s *SpeciesStore) Lookup(key string, month int) ([]SpeciesCount, error) {
	defer bboltTimer("species", "lookup")()
	entries, err := s.entries(key)
	if err != nil {
		return nil, err
	}
	out := make([]SpeciesCount, 0, len(entries))
	for _, e := range entries {
		n := e.Months.Total()
		if month != 0 {
			n = int(e.Months[month-1])
		}
		if n == 0 || int(e.ID) >= len(s.taxonByID) {
			continue
		}
		t := s.taxonByID[e.ID]
		out = append(out, SpeciesCount{Code: t.SpeciesCode, SciName: t.SciName, Count: n})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out, nil
}

// Seasons classifies an area's species as year-round, seasonal or
// occasional (see classifySeasons), keyed by scientific name. An area with
// too little data yields an empty map; an unknown place yields errUnknownArea.
func (s *SpeciesStore) Seasons(key string) (map[string]SpeciesSeason, error) {
	defer bboltTimer("species", "seasons")()
	entries, err := s.entries(key)
	if err != nil {
		return nil, err
	}
	out := map[string]SpeciesSeason{}
	for id, season := range classifySeasons(entries) {
		if int(id) >= len(s.taxonByID) {
			continue
		}
		season.SciName = s.taxonByID[id].SciName
		out[season.SciName] = season
	}
	return out, nil
}
