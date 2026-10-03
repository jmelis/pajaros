package main

import "fmt"

// areaSpeciesSource resolves the species observed in an area (an area key, see
// area.go), in
// taxonomic order, plus their taxonomy and (separately) their popularity —
// for the whole year (month 0) or one calendar month (1..12) —
// all from the offline GBIF-derived bbolt data (see ARCHITECTURE.md), no
// live eBird or GBIF call involved. It's an interface (rather than a
// concrete *SpeciesResolver field on Server) purely for testability: tests
// want to hand the server a fixed set of made-up species codes without
// going through real area/bbolt/taxonomy data at all.
type areaSpeciesSource interface {
	Species(key, lang string, month int) (codes []string, taxa map[string]Taxon, err error)
	// PopularityCounts returns each species' observation count in an
	// area, keyed by scientific name (matching how callers already look
	// species up elsewhere — see main.go's "popularity" mode, also what
	// Learn's card deck orders by).
	PopularityCounts(key string, month int) (map[string]int, error)
	// Seasonality classifies each species as year-round, seasonal or
	// occasional in an area (all years pooled), keyed by scientific name.
	Seasonality(key string) (map[string]SpeciesSeason, error)
}

// SpeciesResolver is the real, production areaSpeciesSource: it pools an
// area's species from the offline grid data (species_store.go), with no live
// API call.
type SpeciesResolver struct {
	taxonomy *TaxonomyStore
	species  *SpeciesStore
}

func NewSpeciesResolver(taxonomy *TaxonomyStore, species *SpeciesStore) *SpeciesResolver {
	return &SpeciesResolver{taxonomy: taxonomy, species: species}
}

func (r *SpeciesResolver) Species(key, lang string, month int) ([]string, map[string]Taxon, error) {
	entries, err := r.species.Lookup(key, month)
	if err != nil {
		return nil, nil, fmt.Errorf("area %q: %w", key, err)
	}
	codes := make([]string, 0, len(entries))
	for _, e := range entries {
		codes = append(codes, e.Code)
	}
	r.taxonomy.SortByTaxonOrder(codes)
	return codes, r.taxonomy.Lookup(codes, lang), nil
}

func (r *SpeciesResolver) PopularityCounts(key string, month int) (map[string]int, error) {
	entries, err := r.species.Lookup(key, month)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]int, len(entries))
	for _, e := range entries {
		counts[e.SciName] = e.Count
	}
	return counts, nil
}

func (r *SpeciesResolver) Seasonality(key string) (map[string]SpeciesSeason, error) {
	return r.species.Seasons(key)
}
