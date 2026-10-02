package main

import "fmt"

// hotspotSpeciesSource resolves the species observed at a hotspot, in
// taxonomic order, plus their taxonomy and (separately) their popularity —
// for the whole year (month 0) or one calendar month (1..12) —
// all from the offline GBIF-derived bbolt data (see ARCHITECTURE.md), no
// live eBird or GBIF call involved. It's an interface (rather than a
// concrete *SpeciesResolver field on Server) purely for testability: tests
// want to hand the server a fixed set of made-up species codes without
// going through real hotspot/bbolt/taxonomy data at all.
type hotspotSpeciesSource interface {
	Species(locID, lang string, month int) (codes []string, taxa map[string]Taxon, err error)
	// PopularityCounts returns each species' observation count at a
	// hotspot, keyed by scientific name (matching how callers already look
	// species up elsewhere — see main.go's "popularity" mode, also what
	// Learn's card deck orders by).
	PopularityCounts(locID string, month int) (map[string]int, error)
	// Seasonality classifies each species as year-round, seasonal or
	// occasional at a hotspot (all years pooled), keyed by scientific name.
	Seasonality(locID string) (map[string]SpeciesSeason, error)
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

func (r *SpeciesResolver) Species(locID, lang string, month int) ([]string, map[string]Taxon, error) {
	entries, err := r.species.Lookup(locID, month)
	if err != nil {
		return nil, nil, fmt.Errorf("hotspot %q: %w", locID, err)
	}
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}
	r.taxonomy.SortByTaxonOrder(codes)
	return codes, r.taxonomy.Lookup(codes, lang), nil
}

func (r *SpeciesResolver) PopularityCounts(locID string, month int) (map[string]int, error) {
	entries, err := r.species.Lookup(locID, month)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.SciName] = e.Count
	}
	return counts, nil
}

func (r *SpeciesResolver) Seasonality(locID string) (map[string]SpeciesSeason, error) {
	return r.species.Seasons(locID)
}
