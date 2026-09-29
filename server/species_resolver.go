package main

import "fmt"

// hotspotSpeciesSource resolves the species observed at a hotspot, in
// taxonomic order, plus their taxonomy and (separately) their popularity —
// all from the offline GBIF-derived bbolt data (see ARCHITECTURE.md), no
// live eBird or GBIF call involved. It's an interface (rather than a
// concrete *SpeciesResolver field on Server) purely for testability: tests
// want to hand the server a fixed set of made-up species codes without
// going through real hotspot/bbolt/taxonomy data at all.
type hotspotSpeciesSource interface {
	Species(locID, lang string) (codes []string, taxa map[string]Taxon, err error)
	// PopularityCounts returns each species' observation count at a
	// hotspot, keyed by scientific name (matching how callers already look
	// species up elsewhere — see main.go's "popularity" mode, also what
	// Learn's card deck orders by).
	PopularityCounts(locID string) (map[string]int, error)
}

// SpeciesResolver is the real, production hotspotSpeciesSource: it reads a
// hotspot's species list straight from the bbolt store (species_store.go),
// keyed directly by hotspot ID — no coordinate lookup or live API call
// needed, since a Hotspot's ID *is* its bbolt key.
type SpeciesResolver struct {
	taxonomy *TaxonomyStore
	species  *SpeciesStore
}

func NewSpeciesResolver(taxonomy *TaxonomyStore, species *SpeciesStore) *SpeciesResolver {
	return &SpeciesResolver{taxonomy: taxonomy, species: species}
}

func (r *SpeciesResolver) Species(locID, lang string) ([]string, map[string]Taxon, error) {
	entries, err := r.species.Lookup(locID)
	if err != nil {
		return nil, nil, err
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("unknown hotspot %q", locID)
	}
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}
	r.taxonomy.SortByTaxonOrder(codes)
	return codes, r.taxonomy.Lookup(codes, lang), nil
}

func (r *SpeciesResolver) PopularityCounts(locID string) (map[string]int, error) {
	entries, err := r.species.Lookup(locID)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.SciName] = e.Count
	}
	return counts, nil
}
